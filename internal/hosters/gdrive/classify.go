package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"admindescargas/internal/hosters"
)

// Classify implements hosters.Hoster.
//
//	Drive API JSON errors        -> by reason (downloadQuotaExceeded =
//	                                Quota{File}, rate limits = Temporary,
//	                                notFound, auth/permission = NeedsAuth...)
//	HTML "quota exceeded" page   -> Quota{File} (rotating the IP won't help)
//	HTML virus-scan form         -> Expired (Prepare follows the form)
//	HTML sign-in / no access     -> NeedsAuth
//	HTML "does not exist"        -> NotFound
//	HTML "unusual traffic"       -> Quota{IP}
//	other HTML instead of a file -> Expired
//	OAuth refresh failure        -> NeedsAuth
//	otherwise                    -> hosters.DefaultClassify
func (h *Hoster) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil || resp == nil {
		if err == nil {
			return hosters.DefaultClassify(resp, body, err)
		}
		return classifyTransport(err)
	}
	if resp.Request != nil && resp.Request.URL != nil && strings.EqualFold(resp.Request.URL.Hostname(), loginHost) {
		return hosters.NewNeedsAuth(errors.New("gdrive: Google asks to sign in: the file is private or needs a Google account with access"))
	}
	if len(body) > 0 && looksJSON(body) && resp.StatusCode >= 400 {
		var env struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Errors  []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
				Status string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &env) == nil && (env.Error.Code != 0 || env.Error.Message != "") {
			reasons := make([]string, 0, len(env.Error.Errors))
			for _, e := range env.Error.Errors {
				reasons = append(reasons, e.Reason)
			}
			return reasonError(resp.StatusCode, reasons, "gdrive: "+env.Error.Message, resp.Header)
		}
	}
	if isHTML(resp.Header) && !isAttachment(resp.Header) && len(body) > 0 {
		if err := pageError(body); err != nil {
			return err
		}
		if resp.StatusCode < 400 {
			return hosters.NewExpired(errors.New("gdrive: got an HTML page instead of the file"))
		}
	}
	return hosters.DefaultClassify(resp, body, nil)
}

// pageError explains an HTML page received instead of a file; nil means
// "no known marker".
func pageError(body []byte) error {
	lower := strings.ToLower(string(body))
	switch {
	case hasDownloadForm(body):
		return hosters.NewExpired(errors.New("gdrive: virus-scan warning page (confirmation needed)"))
	case containsAny(lower, markersQuota):
		return fileQuotaError(false)
	case containsAny(lower, markersBlocked):
		return hosters.NewQuota(hosters.ScopeIP, 0, errors.New("gdrive: Google blocked this IP for unusual traffic"))
	case containsAny(lower, markersNotFound):
		return hosters.NewNotFound(errors.New("gdrive: the file does not exist or was deleted"))
	case containsAny(lower, markersLogin):
		return hosters.NewNeedsAuth(errors.New("gdrive: the file is private: connect a Google account with access"))
	}
	return nil
}

// reasonError maps a Drive API error (status code + reasons).
func reasonError(code int, reasons []string, msg string, hdr http.Header) error {
	err := errors.New(msg)
	ra := hosters.ParseRetryAfter(hdr, time.Now())
	for _, r := range reasons {
		switch r {
		case "downloadQuotaExceeded":
			return fileQuotaError(false)
		case "cannotDownloadAbusiveFile":
			return hosters.NewExpired(fmt.Errorf("%w: %s", errAbusive, msg))
		case "userRateLimitExceeded", "rateLimitExceeded", "sharingRateLimitExceeded", "backendError", "internalError":
			if ra == 0 {
				ra = 10 * time.Second
			}
			return hosters.NewTemporary(ra, err)
		case "dailyLimitExceeded", "quotaExceeded", "dailyLimitExceededUnreg", "storageQuotaExceeded":
			return hosters.NewQuota(hosters.ScopeAccount, ra, err)
		case "notFound":
			return hosters.NewNotFound(err)
		case "authError", "invalidCredentials", "keyInvalid", "keyExpired":
			return hosters.NewNeedsAuth(err)
		case "insufficientFilePermissions", "insufficientPermissions", "forbidden", "appNotAuthorizedToFile",
			"domainPolicy", "accessNotConfigured", "cannotDownloadFile":
			return hosters.NewNeedsAuth(err)
		case "fileNotDownloadable", "exportSizeLimitExceeded", "badRequest", "invalid":
			return hosters.NewFatal(err)
		}
	}
	switch {
	case code == http.StatusUnauthorized:
		return hosters.NewNeedsAuth(err)
	case code == http.StatusForbidden:
		return hosters.NewNeedsAuth(err)
	case code == http.StatusNotFound:
		return hosters.NewNotFound(err)
	case code == http.StatusTooManyRequests, code >= 500:
		return hosters.NewTemporary(ra, err)
	case code >= 400:
		return hosters.NewFatal(err)
	}
	return nil
}

// classifyTransport maps transport errors (including OAuth refresh errors
// surfaced by oauth2.Transport).
func classifyTransport(err error) error {
	err = redactURLError(err)
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		code := re.ErrorCode
		if code == "" && re.Response != nil {
			code = re.Response.Status
		}
		// Do not include the raw body: keep the message generic.
		return hosters.NewNeedsAuth(fmt.Errorf("gdrive: OAuth token refresh failed (%s): reconnect the Google account", code))
	}
	if errors.Is(err, context.Canceled) {
		return hosters.NewFatal(err)
	}
	if _, ok := hosters.AsError(err); ok {
		return err
	}
	return hosters.NewTemporary(0, err)
}

func isHTML(h http.Header) bool {
	mt, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	return mt == "text/html"
}

func isAttachment(h http.Header) bool {
	d, _, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	return d == "attachment"
}

func looksJSON(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\n', '\r', '\t':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
