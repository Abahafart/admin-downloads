package mediafire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"admindescargas/internal/hosters"
)

// Classify implements hosters.Hoster.
//
//	429 / 509 / captcha page        -> Quota{IP}, short RetryAfter
//	error.php?errno=... / removed   -> NotFound (Fatal for blocked files)
//	HTML page instead of the file   -> Expired (Prepare scrapes a new link)
//	403/404/410 on downloadNNNN     -> Expired (direct links are temporary)
//	api/1.5 JSON error              -> mapped by error code
//	otherwise                       -> hosters.DefaultClassify
func (h *Hoster) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil || resp == nil {
		return hosters.DefaultClassify(resp, body, redactURLError(err))
	}
	code := resp.StatusCode
	if code == http.StatusTooManyRequests || code == 509 {
		ra := hosters.ParseRetryAfter(resp.Header, time.Now())
		if ra == 0 {
			ra = captchaRetryAfter
		}
		return hosters.NewQuota(hosters.ScopeIP, ra, fmt.Errorf("mediafire: HTTP %d (too many requests from this IP)", code))
	}
	var reqHost, reqPath, reqQuery string
	if resp.Request != nil && resp.Request.URL != nil {
		reqHost = resp.Request.URL.Hostname()
		reqPath = resp.Request.URL.Path
		reqQuery = resp.Request.URL.RawQuery
	}
	if strings.Contains(reqPath, "error.php") {
		if m := reErrno.FindStringSubmatch(reqQuery); m != nil {
			return errnoError(m[1])
		}
	}
	if len(body) > 0 && looksJSON(body) {
		var env apiEnvelope
		if json.Unmarshal(body, &env) == nil && strings.EqualFold(env.Response.Result, "error") {
			return apiError("", int(env.Response.Error), env.Response.Message)
		}
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.Contains(ct, "text/html")
	attachment := strings.Contains(strings.ToLower(resp.Header.Get("Content-Disposition")), "attachment")
	if isHTML && !attachment && len(body) > 0 {
		if isCaptcha(body) {
			return captchaError()
		}
		lower := strings.ToLower(string(body))
		if containsAny(lower, markersRemoved) {
			return hosters.NewNotFound(errors.New("mediafire: the file was removed"))
		}
		if code < 400 && reDirectHost.MatchString(reqHost) {
			return hosters.NewExpired(errors.New("mediafire: download server returned a page instead of the file"))
		}
	}
	if reDirectHost.MatchString(reqHost) {
		switch code {
		case http.StatusForbidden, http.StatusNotFound, http.StatusGone:
			return hosters.NewExpired(fmt.Errorf("mediafire: direct link rejected (HTTP %d)", code))
		}
	}
	return hosters.DefaultClassify(resp, body, nil)
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
