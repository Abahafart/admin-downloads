package mega

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"admindescargas/internal/hosters"
)

// DefaultAPIURL is MEGA's JSON API endpoint (override with the "api_url"
// option, used by tests).
const DefaultAPIURL = "https://g.api.mega.co.nz"

// MEGA API error codes (negative numbers returned instead of a result).
const (
	eINTERNAL           = -1
	eARGS               = -2
	eAGAIN              = -3
	eRATELIMIT          = -4
	eFAILED             = -5
	eTOOMANY            = -6
	eRANGE              = -7
	eEXPIRED            = -8
	eNOENT              = -9
	eCIRCULAR           = -10
	eACCESS             = -11
	eEXIST              = -12
	eINCOMPLETE         = -13
	eKEY                = -14
	eSID                = -15
	eBLOCKED            = -16
	eOVERQUOTA          = -17
	eTEMPUNAVAIL        = -18
	eTOOMANYCONNECTIONS = -19
	eGOINGOVERQUOTA     = -24
	eMFAREQUIRED        = -26
)

var apiErrNames = map[int]string{
	eINTERNAL: "EINTERNAL", eARGS: "EARGS", eAGAIN: "EAGAIN", eRATELIMIT: "ERATELIMIT",
	eFAILED: "EFAILED", eTOOMANY: "ETOOMANY", eRANGE: "ERANGE", eEXPIRED: "EEXPIRED",
	eNOENT: "ENOENT", eCIRCULAR: "ECIRCULAR", eACCESS: "EACCESS", eEXIST: "EEXIST",
	eINCOMPLETE: "EINCOMPLETE", eKEY: "EKEY", eSID: "ESID", eBLOCKED: "EBLOCKED",
	eOVERQUOTA: "EOVERQUOTA", eTEMPUNAVAIL: "ETEMPUNAVAIL", eTOOMANYCONNECTIONS: "ETOOMANYCONNECTIONS",
	eGOINGOVERQUOTA: "EGOINGOVERQUOTA", eMFAREQUIRED: "EMFAREQUIRED",
}

// APIError is a negative MEGA API result code.
type APIError struct {
	Code int
	// TimeLeft is the quota wait advertised with EOVERQUOTA ("tl"), if any.
	TimeLeft time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	if n, ok := apiErrNames[e.Code]; ok {
		return fmt.Sprintf("mega api error %d (%s)", e.Code, n)
	}
	return fmt.Sprintf("mega api error %d", e.Code)
}

// linkError maps an API error obtained while resolving or preparing a link
// to a hosters outcome. scope is the quota scope to report for EOVERQUOTA.
func (p *Plugin) linkError(err error, scope hosters.QuotaScope) error {
	var ae *APIError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Code {
	case eOVERQUOTA, eGOINGOVERQUOTA:
		ra := ae.TimeLeft
		if ra <= 0 {
			ra = p.retryAfter
		}
		return hosters.NewQuota(scope, ra, fmt.Errorf("mega transfer quota exceeded: %w", ae))
	case eAGAIN, eRATELIMIT, eTEMPUNAVAIL, eTOOMANYCONNECTIONS, eINTERNAL, eTOOMANY, eFAILED:
		return hosters.NewTemporary(0, fmt.Errorf("mega temporarily unavailable: %w", ae))
	case eNOENT:
		return hosters.NewNotFound(fmt.Errorf("mega: file or folder not found (deleted or wrong link): %w", ae))
	case eBLOCKED:
		return hosters.NewNotFound(fmt.Errorf("mega: link taken down by MEGA (terms of service / copyright report): %w", ae))
	case eACCESS:
		return hosters.NewFatal(fmt.Errorf("mega: access denied to this link: %w", ae))
	case eKEY:
		return hosters.NewFatal(fmt.Errorf("mega: decryption key rejected: %w", ae))
	case eEXPIRED:
		return hosters.NewExpired(fmt.Errorf("mega: link expired: %w", ae))
	case eSID:
		return hosters.NewNeedsAuth(fmt.Errorf("mega: session expired, log in again: %w", ae))
	case eMFAREQUIRED:
		return hosters.NewNeedsAuth(fmt.Errorf("%w: %w", ErrMFARequired, ae))
	default:
		return hosters.NewFatal(fmt.Errorf("mega: request rejected: %w", ae))
	}
}

// scrubURLError strips the request URL (it carries the sid or a signed
// download URL) from transport errors, keeping the cause chain intact.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request: %w", ue.Op, ue.Err)
	}
	return err
}

// apiCall sends one command and returns its raw result. query may carry
// "n" (folder handle). sid may be empty. Request-level and command-level
// error codes are returned as *APIError (untyped: callers map them with
// linkError or loginError, since the meaning depends on the context).
// Transport problems and non-200 statuses come back as *hosters.Error.
func (p *Plugin) apiCall(ctx context.Context, s *hosters.Session, sid string, query url.Values, cmd any) (json.RawMessage, error) {
	body, err := json.Marshal([]any{cmd})
	if err != nil {
		return nil, fmt.Errorf("mega: encoding request: %w", err)
	}
	var (
		lastErr  error
		hashcash string
		backoff  = 250 * time.Millisecond
		noSleep  bool
	)
	for attempt := 0; attempt < p.apiRetries; attempt++ {
		if attempt > 0 && !noSleep {
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
			backoff *= 2
		}
		noSleep = false
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("id", strconv.FormatUint(p.seq.Add(1), 10))
		if sid != "" {
			q.Set("sid", sid)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/cs?"+q.Encode(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("mega: building request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if hashcash != "" {
			req.Header.Set("X-Hashcash", hashcash)
		}
		resp, err := s.Client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = hosters.NewTemporary(0, fmt.Errorf("mega api: %w", scrubURLError(err)))
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusPaymentRequired {
			// Proof-of-work challenge (X-Hashcash: 1:<easiness>:<ts>:<token>).
			hc, herr := solveHashcashHeader(ctx, resp.Header.Get("X-Hashcash"), p.hashcashTimeout)
			if herr != nil {
				lastErr = hosters.NewTemporary(0, fmt.Errorf("mega api: hashcash challenge: %w", herr))
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			hashcash = hc
			noSleep = true
			continue
		}
		if resp.StatusCode != http.StatusOK {
			if he := p.Classify(resp, data, nil); he != nil {
				lastErr = fmt.Errorf("mega api: %w", he)
				if hosters.IsTemporary(he) {
					continue
				}
				return nil, lastErr
			}
		}
		if rerr != nil {
			lastErr = hosters.NewTemporary(0, fmt.Errorf("mega api: reading response: %w", rerr))
			continue
		}
		res, aerr := parseAPIResponse(data)
		if aerr != nil {
			var ae *APIError
			if errors.As(aerr, &ae) && ae.Code == eAGAIN {
				lastErr = aerr
				continue
			}
			return nil, aerr
		}
		return res, nil
	}
	return nil, lastErr
}

// parseAPIResponse extracts the first result of a response: a bare number
// is a request-level error, an array holds one result per command (a
// number there is a command error), and objects may embed {"e": code}.
func parseAPIResponse(data []byte) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, hosters.NewTemporary(0, errors.New("mega api: empty response"))
	}
	if code, ok := asCode(data); ok {
		if code < 0 {
			return nil, &APIError{Code: code}
		}
		return data, nil
	}
	if data[0] != '[' {
		return nil, hosters.NewTemporary(0, errors.New("mega api: unexpected response"))
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil || len(arr) == 0 {
		return nil, hosters.NewTemporary(0, errors.New("mega api: malformed response"))
	}
	first := bytes.TrimSpace(arr[0])
	if code, ok := asCode(first); ok {
		if code < 0 {
			return nil, &APIError{Code: code}
		}
		return first, nil
	}
	if len(first) > 0 && first[0] == '{' {
		var e struct {
			E  json.RawMessage `json:"e"`
			TL int64           `json:"tl"`
		}
		if json.Unmarshal(first, &e) == nil && len(e.E) > 0 {
			if code, ok := asCode(e.E); ok && code < 0 {
				return nil, &APIError{Code: code, TimeLeft: time.Duration(e.TL) * time.Second}
			}
		}
	}
	return first, nil
}

// asCode parses b as a JSON integer.
func asCode(b []byte) (int, bool) {
	if len(b) == 0 || (b[0] != '-' && (b[0] < '0' || b[0] > '9')) {
		return 0, false
	}
	n, err := strconv.Atoi(string(b))
	return n, err == nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// parseTimeLeft reads MEGA's quota wait headers (X-MEGA-Time-Left, seconds)
// and falls back to Retry-After.
func parseTimeLeft(h http.Header) time.Duration {
	if v := strings.TrimSpace(h.Get("X-MEGA-Time-Left")); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return hosters.ParseRetryAfter(h, time.Now())
}
