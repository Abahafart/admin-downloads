package mega

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/hosters"
)

// Account errors (wrapped in hosters NeedsAuth errors).
var (
	// ErrMFARequired means the account has 2FA enabled: the UI must ask
	// for the 6-digit code and store it in Secret.Extra["mfa"] (it is used
	// once and then removed), then retry the check.
	ErrMFARequired = errors.New("mega: two-factor authentication code required (set it and check the account again)")
	// ErrInvalidMFA means the 2FA code was rejected.
	ErrInvalidMFA = errors.New("mega: two-factor authentication code rejected")
	// ErrInvalidCredentials means the e-mail or password is wrong.
	ErrInvalidCredentials = errors.New("mega: wrong e-mail or password")
	// ErrAccountBlocked means MEGA suspended the account.
	ErrAccountBlocked = errors.New("mega: account blocked or suspended by MEGA")
)

// SecretExtraMFA is the Secret.Extra key holding a one-time 2FA code.
const SecretExtraMFA = "mfa"

// Session value keys.
const (
	valSID     = "mega.sid"
	valPremium = "mega.premium"
)

// call runs an API command with the session's sid (logging in first if
// needed), re-logs in once on ESID, and maps API errors to outcomes.
func (p *Plugin) call(ctx context.Context, s *hosters.Session, q url.Values, cmd any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		sid, err := p.sessionID(ctx, s)
		if err != nil {
			return nil, err
		}
		res, err := p.apiCall(ctx, s, sid, q, cmd)
		if err == nil {
			return res, nil
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == eSID && sid != "" && attempt == 0 {
			p.dropSession(ctx, s, sid)
			continue
		}
		if errors.As(err, &ae) && (ae.Code == eOVERQUOTA || ae.Code == eGOINGOVERQUOTA) && sid != "" {
			p.learnPremium(ctx, s, sid)
		}
		return nil, p.linkError(err, p.quotaScope(s))
	}
}

// learnPremium asks "uq" for the account level when it is not known yet, so
// quota errors of Pro accounts are reported as account-scoped.
func (p *Plugin) learnPremium(ctx context.Context, s *hosters.Session, sid string) {
	if _, ok := s.Value(valPremium); ok {
		return
	}
	res, err := p.apiCall(ctx, s, sid, nil, map[string]any{"a": "uq", "pro": 1})
	if err != nil {
		return
	}
	var uq uqResponse
	if json.Unmarshal(res, &uq) == nil {
		s.SetValue(valPremium, uq.UType > 0)
	}
}

// quotaScope is IP for anonymous and free sessions, Account for Pro ones.
func (p *Plugin) quotaScope(s *hosters.Session) hosters.QuotaScope {
	if s == nil || s.Anonymous() {
		return hosters.ScopeIP
	}
	if v, ok := s.Value(valPremium); ok {
		if premium, _ := v.(bool); premium {
			return hosters.ScopeAccount
		}
	}
	return hosters.ScopeIP
}

// sessionID returns the sid to use ("" for anonymous sessions), logging in
// when no session is cached. The sid is persisted in Secret.Session so the
// password is not resent on every start.
func (p *Plugin) sessionID(ctx context.Context, s *hosters.Session) (string, error) {
	if s == nil || s.Anonymous() {
		return "", nil
	}
	if v, ok := s.Value(valSID); ok {
		if sid, _ := v.(string); sid != "" {
			return sid, nil
		}
	}
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	if v, ok := s.Value(valSID); ok {
		if sid, _ := v.(string); sid != "" {
			return sid, nil
		}
	}
	sec, err := s.Secret(ctx)
	if err != nil {
		return "", hosters.NewNeedsAuth(fmt.Errorf("mega: loading account credentials: %w", err))
	}
	if sec.Session != "" {
		s.SetValue(valSID, sec.Session)
		return sec.Session, nil
	}
	email := strings.TrimSpace(s.Account.Username)
	if email == "" || sec.Password == "" {
		return "", hosters.NewNeedsAuth(errors.New("mega: the account needs an e-mail and a password"))
	}
	mfa := strings.TrimSpace(sec.Extra[SecretExtraMFA])
	sid, lerr := p.login(ctx, s, email, sec.Password, mfa)
	if mfa != "" && !hosters.IsTemporary(lerr) {
		// 2FA codes are single use: drop it whether it worked or not.
		delete(sec.Extra, SecretExtraMFA)
	}
	if lerr != nil {
		if mfa != "" && !hosters.IsTemporary(lerr) {
			if err := s.SetSecret(ctx, sec); err != nil && s.Log != nil {
				s.Log.Warn("mega: could not clear used 2FA code", "err", err)
			}
		}
		return "", lerr
	}
	sec.Session = sid
	if err := s.SetSecret(ctx, sec); err != nil && s.Log != nil {
		s.Log.Warn("mega: could not persist session", "err", err)
	}
	s.SetValue(valSID, sid)
	if s.Log != nil {
		s.Log.Info("mega: logged in")
	}
	return sid, nil
}

// dropSession forgets a sid MEGA rejected (ESID) so the next call logs in
// again with the password.
func (p *Plugin) dropSession(ctx context.Context, s *hosters.Session, sid string) {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	if v, ok := s.Value(valSID); ok && v == sid {
		s.DeleteValue(valSID)
	}
	sec, err := s.Secret(ctx)
	if err != nil || sec.Session != sid {
		return
	}
	sec.Session = ""
	if err := s.SetSecret(ctx, sec); err != nil && s.Log != nil {
		s.Log.Warn("mega: could not clear expired session", "err", err)
	}
}

// loginError maps API errors returned by us0/us.
func loginError(err error, mfa string) error {
	var ae *APIError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Code {
	case eMFAREQUIRED:
		if mfa != "" {
			return hosters.NewNeedsAuth(ErrInvalidMFA)
		}
		return hosters.NewNeedsAuth(ErrMFARequired)
	case eNOENT, eARGS, eACCESS, eKEY:
		return hosters.NewNeedsAuth(fmt.Errorf("%w (%v)", ErrInvalidCredentials, ae))
	case eFAILED:
		if mfa != "" {
			return hosters.NewNeedsAuth(ErrInvalidMFA)
		}
		return hosters.NewNeedsAuth(fmt.Errorf("%w (%v)", ErrInvalidCredentials, ae))
	case eBLOCKED:
		return hosters.NewNeedsAuth(ErrAccountBlocked)
	case eAGAIN, eRATELIMIT, eTEMPUNAVAIL, eINTERNAL, eTOOMANY:
		return hosters.NewTemporary(0, fmt.Errorf("mega login: %w", ae))
	default:
		return hosters.NewNeedsAuth(fmt.Errorf("mega login failed: %w", ae))
	}
}

// login performs prelogin (us0) and login (us), v1 or v2, with an optional
// 2FA code, and returns the session id.
func (p *Plugin) login(ctx context.Context, s *hosters.Session, email, password, mfa string) (string, error) {
	email = strings.ToLower(email)
	res, err := p.apiCall(ctx, s, "", nil, map[string]any{"a": "us0", "user": email})
	if err != nil {
		return "", loginError(err, mfa)
	}
	var pre struct {
		V int    `json:"v"`
		S string `json:"s"`
	}
	if err := json.Unmarshal(res, &pre); err != nil {
		return "", hosters.NewTemporary(0, fmt.Errorf("mega prelogin: %w", err))
	}
	cmd := map[string]any{"a": "us", "user": email}
	var passKey []byte
	switch pre.V {
	case 1:
		passKey, err = prepareKeyV1(password)
		if err != nil {
			return "", err
		}
		uh, err := stringHash(email, passKey)
		if err != nil {
			return "", err
		}
		cmd["uh"] = uh
	case 2:
		salt, err := b64Decode(pre.S)
		if err != nil || len(salt) == 0 {
			return "", hosters.NewTemporary(0, errors.New("mega prelogin: missing salt"))
		}
		var uh string
		passKey, uh = deriveV2(password, salt)
		cmd["uh"] = uh
		sek := make([]byte, 16)
		_, _ = rand.Read(sek)
		cmd["sek"] = b64Encode(sek)
	default:
		return "", hosters.NewFatal(fmt.Errorf("mega prelogin: unsupported account version %d", pre.V))
	}
	if mfa != "" {
		cmd["mfa"] = mfa
	}
	res, err = p.apiCall(ctx, s, "", nil, cmd)
	if err != nil {
		return "", loginError(err, mfa)
	}
	var lr struct {
		K     string `json:"k"`
		Privk string `json:"privk"`
		Csid  string `json:"csid"`
		Tsid  string `json:"tsid"`
	}
	if err := json.Unmarshal(res, &lr); err != nil {
		return "", hosters.NewTemporary(0, fmt.Errorf("mega login: %w", err))
	}
	encMK, err := b64Decode(lr.K)
	if err != nil || len(encMK) != 16 {
		return "", hosters.NewNeedsAuth(errors.New("mega login: missing master key in response"))
	}
	masterKey, err := ecbDecrypt(passKey, encMK)
	if err != nil {
		return "", hosters.NewNeedsAuth(fmt.Errorf("mega login: %w", err))
	}
	switch {
	case lr.Csid != "" && lr.Privk != "":
		sid, err := decryptSID(masterKey, lr.Privk, lr.Csid)
		if err != nil {
			return "", hosters.NewNeedsAuth(fmt.Errorf("%w: %v", ErrInvalidCredentials, err))
		}
		return sid, nil
	case lr.Tsid != "":
		// Temporary session (account without RSA key): tsid[0:16]
		// encrypted with the master key must equal tsid[16:32].
		t, err := b64Decode(lr.Tsid)
		if err != nil || len(t) < 32 {
			return "", hosters.NewNeedsAuth(errors.New("mega login: malformed tsid"))
		}
		enc, _ := ecbEncrypt(masterKey, t[:aes.BlockSize])
		if subtle.ConstantTimeCompare(enc, t[len(t)-aes.BlockSize:]) != 1 {
			return "", hosters.NewNeedsAuth(ErrInvalidCredentials)
		}
		return lr.Tsid, nil
	}
	return "", hosters.NewNeedsAuth(errors.New("mega login: no session in response"))
}

// Account levels reported by "uq" (utype).
var proLevels = map[int]string{0: "Free", 1: "Pro I", 2: "Pro II", 3: "Pro III", 4: "Pro Lite", 100: "Business", 101: "Pro Flexi"}

// uqResponse is the answer to {"a":"uq"}.
type uqResponse struct {
	UType  int   `json:"utype"`
	SUntil int64 `json:"suntil"`
	MXfer  int64 `json:"mxfer"`
	CAXfer int64 `json:"caxfer"`
	CSXfer int64 `json:"csxfer"`
	MStrg  int64 `json:"mstrg"`
	CStrg  int64 `json:"cstrg"`
}

// CheckAccount implements hosters.AccountChecker: logs in (reusing the
// stored sid) and reports the account level and transfer quota ("uq").
func (p *Plugin) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	if s == nil || s.Anonymous() {
		return nil, hosters.ErrNoAccount
	}
	res, err := p.call(ctx, s, nil, map[string]any{"a": "uq", "xfer": 1, "strg": 1, "pro": 1})
	if err != nil {
		switch {
		case errors.Is(err, ErrMFARequired):
			return &accounts.CheckResult{Status: accounts.StatusNeeds2FA, Message: ErrMFARequired.Error(), QuotaTotal: -1, QuotaUsed: -1}, nil
		case errors.Is(err, ErrInvalidMFA):
			return &accounts.CheckResult{Status: accounts.StatusNeeds2FA, Message: ErrInvalidMFA.Error(), QuotaTotal: -1, QuotaUsed: -1}, nil
		case hosters.IsNeedsAuth(err):
			return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: err.Error(), QuotaTotal: -1, QuotaUsed: -1}, nil
		}
		return nil, err
	}
	var uq uqResponse
	if err := json.Unmarshal(res, &uq); err != nil {
		return nil, hosters.NewTemporary(0, fmt.Errorf("mega: decoding quota: %w", err))
	}
	premium := uq.UType > 0
	s.SetValue(valPremium, premium)
	level, ok := proLevels[uq.UType]
	if !ok {
		level = fmt.Sprintf("level %d", uq.UType)
	}
	r := &accounts.CheckResult{Status: accounts.StatusOK, Premium: premium, QuotaTotal: -1, QuotaUsed: -1, Message: "MEGA " + level}
	if premium && uq.SUntil > 0 {
		r.PremiumUntil = time.Unix(uq.SUntil, 0).UTC()
	}
	if uq.MXfer > 0 {
		r.QuotaTotal = uq.MXfer
		r.QuotaUsed = uq.CAXfer + uq.CSXfer
		if r.QuotaUsed >= r.QuotaTotal {
			r.Status = accounts.StatusQuota
		}
	}
	return r, nil
}

// SelfTest implements hosters.SelfTester: resolves the configured public
// link and asks for a download URL of its first file (nothing is
// downloaded).
func (p *Plugin) SelfTest(ctx context.Context, s *hosters.Session) error {
	if p.selfTestLink == "" {
		return errors.New("mega: set [hosters.mega.options] selftest_link to a public MEGA link")
	}
	res, err := p.Resolve(ctx, s, p.selfTestLink)
	if err != nil {
		return fmt.Errorf("mega selftest: resolve: %w", err)
	}
	if len(res.Files) == 0 {
		return errors.New("mega selftest: the link has no files")
	}
	spec, err := p.Prepare(ctx, s, res.Files[0])
	if err != nil {
		return fmt.Errorf("mega selftest: prepare: %w", err)
	}
	if spec.URL == "" || spec.Decryptor == nil {
		return errors.New("mega selftest: incomplete download spec")
	}
	return nil
}
