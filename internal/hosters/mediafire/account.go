package mediafire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"admindescargas/internal/accounts"
	"admindescargas/internal/hosters"
)

// Session value keys.
const (
	valLoggedIn     = "mediafire:cookies_loaded"
	valSessionToken = "mediafire:session_token" //nolint:gosec // G101: key name, not a credential (the value is a secret: never log)
)

// ensureLogin loads the account cookies into the session jar once per
// session. Anonymous sessions are left untouched.
func (h *Hoster) ensureLogin(ctx context.Context, s *hosters.Session) error {
	if s == nil || s.Anonymous() {
		return nil
	}
	if _, ok := s.Value(valLoggedIn); ok {
		return nil
	}
	if s.Account.Kind != accounts.KindCookie {
		return hosters.NewNeedsAuth(fmt.Errorf("mediafire: unsupported account kind %q (use cookie)", s.Account.Kind))
	}
	sec, err := s.Secret(ctx)
	if err != nil {
		return hosters.NewNeedsAuth(fmt.Errorf("mediafire: load account secret: %w", err))
	}
	cookies, err := parseCookies(sec.Cookies, ".mediafire.com")
	if err != nil {
		return hosters.NewNeedsAuth(fmt.Errorf("mediafire: account cookies: %w", err))
	}
	if s.Jar == nil {
		return hosters.NewFatal(errors.New("mediafire: session has no cookie jar"))
	}
	n := loadCookies(s.Jar, cookies)
	if base, err := url.Parse(h.webBase); err == nil && !h.hostOK(base.Hostname()) {
		// Tests (and custom bases): make the cookies visible there too.
		hostOnly := make([]*http.Cookie, 0, len(cookies))
		for _, c := range cookies {
			cc := *c
			cc.Domain = ""
			hostOnly = append(hostOnly, &cc)
		}
		s.Jar.SetCookies(base, hostOnly)
	}
	s.SetValue(valLoggedIn, true)
	logOf(s).Debug("mediafire: account cookies loaded", "count", n)
	return nil
}

// CheckAccount implements hosters.AccountChecker: it exchanges the cookies
// for an API session token and reads user/get_info (premium flag, storage).
func (h *Hoster) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	res := &accounts.CheckResult{QuotaTotal: -1, QuotaUsed: -1}
	if err := h.ensureLogin(ctx, s); err != nil {
		res.Status, res.Message = accounts.StatusInvalid, err.Error()
		return res, nil //nolint:nilerr // the failure is reported as the account status
	}
	if s.Anonymous() {
		res.Status, res.Message = accounts.StatusError, "anonymous session"
		return res, nil
	}
	tok, err := h.sessionToken(ctx, s)
	if err != nil {
		if hosters.IsNeedsAuth(err) {
			res.Status, res.Message = accounts.StatusExpired, "MediaFire rejected the cookies (log in again and re-export them)"
			return res, nil
		}
		res.Status, res.Message = accounts.StatusError, err.Error()
		return res, err
	}
	s.SetValue(valSessionToken, tok)
	env, err := h.apiCall(ctx, s, pathUserInfo, url.Values{})
	if err != nil {
		if hosters.IsNeedsAuth(err) {
			res.Status, res.Message = accounts.StatusExpired, "MediaFire session expired"
			return res, nil
		}
		res.Status, res.Message = accounts.StatusError, err.Error()
		return res, err
	}
	res.Status = accounts.StatusOK
	if u := env.Response.UserInfo; u != nil {
		res.Premium = strings.EqualFold(u.Premium, "yes")
		res.Message = u.Email
		if v, err := strconv.ParseInt(u.StorageLim, 10, 64); err == nil {
			res.QuotaTotal = v
		}
		if v, err := strconv.ParseInt(u.UsedStorage, 10, 64); err == nil {
			res.QuotaUsed = v
		}
	}
	return res, nil
}

// sessionToken asks the web app for an API session token using the cookies
// in the jar.
func (h *Hoster) sessionToken(ctx context.Context, s *hosters.Session) (string, error) {
	form := url.Values{"response_format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.webBase+pathSessionToken, strings.NewReader(form.Encode()))
	if err != nil {
		return "", hosters.NewFatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", h.Classify(nil, nil, fmt.Errorf("mediafire: session token: %w", redactURLError(err)))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var env apiEnvelope
	if json.Unmarshal(body, &env) != nil {
		if err := h.Classify(resp, head(body), nil); err != nil {
			return "", err
		}
		return "", hosters.NewNeedsAuth(errors.New("mediafire: session token: unexpected reply (not logged in?)"))
	}
	if strings.EqualFold(env.Response.Result, "error") || env.Response.SessionToken == "" {
		return "", hosters.NewNeedsAuth(fmt.Errorf("mediafire: session token refused: %s", env.Response.Message))
	}
	return env.Response.SessionToken, nil
}
