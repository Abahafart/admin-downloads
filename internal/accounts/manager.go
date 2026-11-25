package accounts

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

// Manager errors.
var (
	// ErrAccountNotFound is returned (wrapped) when no account has that ID.
	// Repo implementations must wrap it for missing rows.
	ErrAccountNotFound = errors.New("accounts: account not found")
	// ErrNoAccount means Pick found no usable account for the hoster (none
	// configured, all disabled/invalid, or all over quota; see
	// *NoAccountError for the earliest retry time).
	ErrNoAccount = errors.New("accounts: no usable account")
	// ErrInvalidAccount wraps validation failures of Create/Update input.
	ErrInvalidAccount = errors.New("accounts: invalid account")
	// ErrNoChecker means the manager has no Checker for that hoster.
	ErrNoChecker = errors.New("accounts: account checks are not supported")
	// ErrNotLockable is returned by Unlock/Lock on stores without a master
	// password (OS keyring, memory).
	ErrNotLockable = errors.New("accounts: the secret store has no master password")
	// ErrInvalidMFACode means the 2FA code is not 6-8 digits.
	ErrInvalidMFACode = errors.New("accounts: the 2FA code must have 6 to 8 digits")
)

// NoAccountError is returned by Pick when no account is usable. Until is
// the earliest time a quota-blocked account becomes usable again (zero if
// none is just waiting for quota). It matches ErrNoAccount with errors.Is.
type NoAccountError struct {
	Hoster string
	Until  time.Time
}

func (e *NoAccountError) Error() string {
	if e.Until.IsZero() {
		return fmt.Sprintf("accounts: no usable account for %s", e.Hoster)
	}
	return fmt.Sprintf("accounts: every %s account is over quota until %s", e.Hoster, e.Until.Format(time.RFC3339))
}

// Is makes errors.Is(err, ErrNoAccount) true.
func (e *NoAccountError) Is(target error) bool { return target == ErrNoAccount }

// MFAExtraKey is the Secret.Extra key holding a pending 2FA code while the
// check (login) runs. Plugins read it in CheckAccount; the manager removes it
// afterwards.
const MFAExtraKey = "mfa"

// QuotaMark is a persisted "account over quota until" mark (table
// quota_marks, scope 'account').
type QuotaMark struct {
	Hoster    string
	AccountID string
	Until     time.Time
	Reason    string
}

// Repo persists account metadata (implemented by *store.Store in
// internal/store/accounts_repo.go). Missing rows must be reported with an
// error wrapping ErrAccountNotFound.
type Repo interface {
	InsertAccount(ctx context.Context, a *Account) error
	GetAccount(ctx context.Context, id string) (*Account, error)
	// ListAccounts returns the accounts of hoster ("" = all), oldest first.
	ListAccounts(ctx context.Context, hoster string) ([]*Account, error)
	UpdateAccount(ctx context.Context, a *Account) error
	DeleteAccount(ctx context.Context, id string) error
	MarkAccountQuota(ctx context.Context, m QuotaMark) error
	ClearAccountQuota(ctx context.Context, hoster, accountID string) error
	// AccountQuotaMarks returns the marks whose Until is after now.
	AccountQuotaMarks(ctx context.Context, now time.Time) ([]QuotaMark, error)
}

// Checker logs in with the account and reports its status (integration
// implements it with hosters.AccountChecker over a Session whose Secrets is
// the manager's store). The secret can be read and updated through that
// Session. Errors are recorded as StatusError with the error text, so they
// must not contain secrets.
type Checker func(ctx context.Context, acct *Account) (*CheckResult, error)

// ChangeType says what happened in a ChangeEvent.
type ChangeType string

// Change types.
const (
	ChangeCreated  ChangeType = "created"
	ChangeUpdated  ChangeType = "updated" // metadata, credentials, status, quota
	ChangeDeleted  ChangeType = "deleted"
	ChangeLocked   ChangeType = "secrets_locked"   // vault locked (Account nil)
	ChangeUnlocked ChangeType = "secrets_unlocked" // vault unlocked (Account nil)
)

// ChangeEvent is delivered to ManagerOptions.OnChange. Account is a copy
// (nil for deleted accounts and store events).
type ChangeEvent struct {
	Type      ChangeType `json:"type"`
	AccountID string     `json:"account_id,omitempty"`
	Account   *Account   `json:"account,omitempty"`
}

// ManagerOptions configures NewManager.
type ManagerOptions struct {
	Repo    Repo        // required
	Secrets SecretStore // required (OpenSecretStore result)
	// Checker runs account checks; nil disables Check (ErrNoChecker).
	Checker Checker
	// HosterDomains returns the cookie domains of a hoster (used to filter
	// imported cookies when the caller does not pass domains).
	HosterDomains func(hoster string) []string
	// OnChange is called synchronously after every change, outside the
	// manager's locks; it must not block (publish to the event bus).
	OnChange func(ChangeEvent)
	Logger   *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time
	// DefaultQuotaWait is used by ReportQuota when retryAfter <= 0
	// (default 1h).
	DefaultQuotaWait time.Duration
}

// Manager implements account CRUD, selection (Pick), quota tracking,
// checks and 2FA on top of a Repo (metadata) and a SecretStore (secrets).
// It is safe for concurrent use.
type Manager struct {
	repo     Repo
	secrets  SecretStore
	checker  Checker
	domains  func(string) []string
	onChange func(ChangeEvent)
	log      *slog.Logger
	now      func() time.Time
	quotaDef time.Duration

	checks singleflight.Group

	mu    sync.Mutex
	quota map[string]time.Time // account ID -> blocked until
	rr    map[string]uint64    // hoster -> round-robin counter
}

// NewManager builds a manager and loads the persisted quota marks.
func NewManager(ctx context.Context, opts ManagerOptions) (*Manager, error) {
	if opts.Repo == nil || opts.Secrets == nil {
		return nil, errors.New("accounts: NewManager needs a Repo and a SecretStore")
	}
	m := &Manager{
		repo:     opts.Repo,
		secrets:  opts.Secrets,
		checker:  opts.Checker,
		domains:  opts.HosterDomains,
		onChange: opts.OnChange,
		log:      opts.Logger,
		now:      opts.Now,
		quotaDef: opts.DefaultQuotaWait,
		quota:    map[string]time.Time{},
		rr:       map[string]uint64{},
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.quotaDef <= 0 {
		m.quotaDef = time.Hour
	}
	marks, err := m.repo.AccountQuotaMarks(ctx, m.now())
	if err != nil {
		return nil, fmt.Errorf("accounts: load quota marks: %w", err)
	}
	for _, q := range marks {
		m.quota[q.AccountID] = q.Until
	}
	return m, nil
}

// Secrets returns the secret store (to put in hosters.Session.Secrets).
func (m *Manager) Secrets() SecretStore { return m.secrets }

func (m *Manager) emit(t ChangeType, id string, a *Account) {
	if m.onChange == nil {
		return
	}
	var cp *Account
	if a != nil {
		c := *a
		cp = &c
	}
	m.onChange(ChangeEvent{Type: t, AccountID: id, Account: cp})
}

// NewAccount is the input of Create.
type NewAccount struct {
	Hoster   string `json:"hoster"`
	Label    string `json:"label"`
	Kind     Kind   `json:"kind"`
	Username string `json:"username"`
	// Enabled defaults to true when nil.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowRotation defaults to false (PLAN.md section 15).
	AllowRotation bool `json:"allow_rotation"`
	// Secret: Password for KindPassword, Cookies for KindCookie (any
	// supported format; normalized on save), APIKey for KindAPIKey; empty
	// for KindOAuth (the token comes from the OAuth flow).
	Secret Secret `json:"-"`
	// CookieDomains filters imported cookies; nil = HosterDomains(hoster).
	CookieDomains []string `json:"-"`
}

// AccountPatch is the input of Update; nil fields are left unchanged.
type AccountPatch struct {
	Label         *string `json:"label,omitempty"`
	Username      *string `json:"username,omitempty"`
	Enabled       *bool   `json:"enabled,omitempty"`
	AllowRotation *bool   `json:"allow_rotation,omitempty"`
	// Password / Cookies / APIKey replace that credential (and drop the
	// cached hoster session). Cookies are normalized like in Create.
	Password      *string  `json:"-"`
	Cookies       *string  `json:"-"`
	APIKey        *string  `json:"-"`
	CookieDomains []string `json:"-"`
}

// List returns the accounts of hoster ("" = all).
func (m *Manager) List(ctx context.Context, hoster string) ([]*Account, error) {
	return m.repo.ListAccounts(ctx, hoster)
}

// Get returns one account (error wrapping ErrAccountNotFound if missing).
func (m *Manager) Get(ctx context.Context, id string) (*Account, error) {
	return m.repo.GetAccount(ctx, id)
}

// QuotaBlockedUntil returns when the account's quota block ends (zero if
// not blocked).
func (m *Manager) QuotaBlockedUntil(id string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.quota[id]; ok && u.After(m.now()) {
		return u
	}
	return time.Time{}
}

// Create validates in, stores the secret and then the metadata row.
func (m *Manager) Create(ctx context.Context, in NewAccount) (*Account, error) {
	in.Hoster = strings.TrimSpace(in.Hoster)
	if in.Hoster == "" {
		return nil, fmt.Errorf("%w: hoster is required", ErrInvalidAccount)
	}
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidAccount, in.Kind)
	}
	sec := cloneSecret(in.Secret)
	switch in.Kind {
	case KindPassword:
		if sec.Password == "" {
			return nil, fmt.Errorf("%w: a password is required", ErrInvalidAccount)
		}
		if strings.TrimSpace(in.Username) == "" {
			return nil, fmt.Errorf("%w: a username or e-mail is required", ErrInvalidAccount)
		}
	case KindCookie:
		norm, err := m.normalizeCookies(in.Hoster, sec.Cookies, in.CookieDomains)
		if err != nil {
			return nil, err
		}
		sec.Cookies = norm
	case KindAPIKey:
		sec.APIKey = strings.TrimSpace(sec.APIKey)
		if sec.APIKey == "" {
			return nil, fmt.Errorf("%w: an API key is required", ErrInvalidAccount)
		}
	case KindOAuth:
	}
	now := m.now().UTC()
	a := &Account{
		ID:            NewID(),
		Hoster:        in.Hoster,
		Label:         strings.TrimSpace(in.Label),
		Kind:          in.Kind,
		Username:      strings.TrimSpace(in.Username),
		Enabled:       in.Enabled == nil || *in.Enabled,
		AllowRotation: in.AllowRotation,
		Status:        StatusUnknown,
		QuotaTotal:    -1,
		QuotaUsed:     -1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if a.Label == "" {
		a.Label = cmp.Or(a.Username, a.Hoster)
	}
	if !a.Enabled {
		a.Status = StatusDisabled
	}
	if !sec.IsZero() {
		if err := m.secrets.Set(ctx, a.ID, sec); err != nil {
			return nil, fmt.Errorf("accounts: save credentials: %w", err)
		}
	}
	if err := m.repo.InsertAccount(ctx, a); err != nil {
		_ = m.secrets.Delete(ctx, a.ID)
		return nil, fmt.Errorf("accounts: create: %w", err)
	}
	m.log.Info("account created", "account", a.ID, "hoster", a.Hoster, "kind", a.Kind)
	m.emit(ChangeCreated, a.ID, a)
	return a, nil
}

func (m *Manager) normalizeCookies(hoster, text string, domains []string) (string, error) {
	if domains == nil && m.domains != nil {
		domains = m.domains(hoster)
	}
	imp, err := ImportCookies(text, domains, m.now())
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidAccount, err)
	}
	return imp.Normalized, nil
}

// Update applies patch to the account. Changing a credential resets the
// status to unknown (the UI should run Check).
func (m *Manager) Update(ctx context.Context, id string, p AccountPatch) (*Account, error) {
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Label != nil {
		a.Label = strings.TrimSpace(*p.Label)
	}
	if p.Username != nil {
		a.Username = strings.TrimSpace(*p.Username)
	}
	if p.AllowRotation != nil {
		a.AllowRotation = *p.AllowRotation
	}
	if p.Enabled != nil && *p.Enabled != a.Enabled {
		a.Enabled = *p.Enabled
		if a.Enabled {
			a.Status, a.StatusMsg = StatusUnknown, ""
		} else {
			a.Status, a.StatusMsg = StatusDisabled, ""
		}
	}
	if p.Password != nil || p.Cookies != nil || p.APIKey != nil {
		sec, err := m.secrets.Get(ctx, id)
		if err != nil && !errors.Is(err, ErrSecretNotFound) {
			return nil, fmt.Errorf("accounts: read credentials: %w", err)
		}
		if p.Password != nil {
			if *p.Password == "" {
				return nil, fmt.Errorf("%w: empty password", ErrInvalidAccount)
			}
			sec.Password = *p.Password
		}
		if p.Cookies != nil {
			norm, err := m.normalizeCookies(a.Hoster, *p.Cookies, p.CookieDomains)
			if err != nil {
				return nil, err
			}
			sec.Cookies = norm
		}
		if p.APIKey != nil {
			k := strings.TrimSpace(*p.APIKey)
			if k == "" {
				return nil, fmt.Errorf("%w: empty API key", ErrInvalidAccount)
			}
			sec.APIKey = k
		}
		// A cached hoster session belongs to the old credentials.
		sec.Session = ""
		delete(sec.Extra, MFAExtraKey)
		if err := m.secrets.Set(ctx, id, sec); err != nil {
			return nil, fmt.Errorf("accounts: save credentials: %w", err)
		}
		if a.Enabled {
			a.Status, a.StatusMsg = StatusUnknown, ""
		}
	}
	a.UpdatedAt = m.now().UTC()
	if err := m.repo.UpdateAccount(ctx, a); err != nil {
		return nil, fmt.Errorf("accounts: update: %w", err)
	}
	m.emit(ChangeUpdated, a.ID, a)
	return a, nil
}

// ImportCookies replaces the cookies of an account with a pasted export,
// filtered to domains (nil = HosterDomains(hoster)). It returns the import
// summary (cookie values are not meant for display).
func (m *Manager) ImportCookies(ctx context.Context, id, text string, domains []string) (*CookieImport, error) {
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if domains == nil && m.domains != nil {
		domains = m.domains(a.Hoster)
	}
	imp, err := ImportCookies(text, domains, m.now())
	if err != nil {
		return nil, err
	}
	norm := imp.Normalized
	if _, err := m.Update(ctx, id, AccountPatch{Cookies: &norm, CookieDomains: domains}); err != nil {
		return nil, err
	}
	return imp, nil
}

// SaveOAuthToken stores tok as the account's OAuth token (keeping other
// secret fields) and resets its status to unknown. Used by GoogleOAuth.
func (m *Manager) SaveOAuthToken(ctx context.Context, id string, tok *oauth2.Token) error {
	if tok == nil || tok.AccessToken == "" {
		return errors.New("accounts: empty OAuth token")
	}
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	sec, err := m.secrets.Get(ctx, id)
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		return fmt.Errorf("accounts: read credentials: %w", err)
	}
	t := *tok
	if t.RefreshToken == "" && sec.OAuthToken != nil {
		// Google omits the refresh token on re-consent sometimes; keep the
		// previous one.
		t.RefreshToken = sec.OAuthToken.RefreshToken
	}
	sec.OAuthToken = &t
	if err := m.secrets.Set(ctx, id, sec); err != nil {
		return fmt.Errorf("accounts: save OAuth token: %w", err)
	}
	if a.Enabled {
		a.Status, a.StatusMsg = StatusUnknown, ""
	}
	a.UpdatedAt = m.now().UTC()
	if err := m.repo.UpdateAccount(ctx, a); err != nil {
		return fmt.Errorf("accounts: update: %w", err)
	}
	m.log.Info("oauth token stored", "account", a.ID, "hoster", a.Hoster)
	m.emit(ChangeUpdated, a.ID, a)
	return nil
}

// Delete removes the secret and then the account row. With a locked vault
// it fails with ErrLocked (so no orphaned secret stays in the vault).
func (m *Manager) Delete(ctx context.Context, id string) error {
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if err := m.secrets.Delete(ctx, id); err != nil {
		return fmt.Errorf("accounts: delete credentials: %w", err)
	}
	if err := m.repo.ClearAccountQuota(ctx, a.Hoster, id); err != nil {
		m.log.Warn("clear account quota mark", "account", id, "err", err)
	}
	if err := m.repo.DeleteAccount(ctx, id); err != nil {
		return fmt.Errorf("accounts: delete: %w", err)
	}
	m.mu.Lock()
	delete(m.quota, id)
	m.mu.Unlock()
	m.log.Info("account deleted", "account", id, "hoster", a.Hoster)
	m.emit(ChangeDeleted, id, nil)
	return nil
}

// usable reports whether a can be picked, ignoring quota blocks.
func usable(a *Account) bool {
	if !a.Enabled {
		return false
	}
	switch a.Status {
	case StatusInvalid, StatusExpired, StatusNeeds2FA, StatusDisabled:
		return false
	}
	return a.QuotaLeft() != 0
}

// Pick returns the enabled account of hoster with the most quota left
// (known quota beats unknown; premium breaks ties; round-robin among
// equals), skipping accounts over quota, invalid, expired, waiting for 2FA
// and those in exclude. With none usable it returns a *NoAccountError
// (errors.Is(err, ErrNoAccount)); the engine then downloads anonymously or
// waits until NoAccountError.Until.
func (m *Manager) Pick(ctx context.Context, hoster string, exclude ...string) (*Account, error) {
	list, err := m.repo.ListAccounts(ctx, hoster)
	if err != nil {
		return nil, err
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		cands []*Account
		until time.Time
	)
	for _, a := range list {
		if a.Hoster != hoster || !usable(a) || slices.Contains(exclude, a.ID) {
			continue
		}
		if u, ok := m.quota[a.ID]; ok {
			if u.After(now) {
				if until.IsZero() || u.Before(until) {
					until = u
				}
				continue
			}
			delete(m.quota, a.ID)
		}
		cands = append(cands, a)
	}
	if len(cands) == 0 {
		return nil, &NoAccountError{Hoster: hoster, Until: until}
	}
	rank := func(a *Account) (int64, int) {
		p := 0
		if a.Premium {
			p = 1
		}
		return a.QuotaLeft(), p
	}
	// Best first; stable by ID for a deterministic round-robin order.
	slices.SortStableFunc(cands, func(x, y *Account) int {
		qx, px := rank(x)
		qy, py := rank(y)
		if c := cmp.Compare(qy, qx); c != 0 {
			return c
		}
		if c := cmp.Compare(py, px); c != 0 {
			return c
		}
		return cmp.Compare(x.ID, y.ID)
	})
	qb, pb := rank(cands[0])
	n := 1
	for n < len(cands) {
		q, p := rank(cands[n])
		if q != qb || p != pb {
			break
		}
		n++
	}
	i := m.rr[hoster] % uint64(n)
	m.rr[hoster]++
	return cands[i], nil
}

// ReportQuota marks the account as over quota for retryAfter (<= 0 means
// DefaultQuotaWait), persists the mark and updates the account status, so
// Pick skips it and the engine moves to the next account.
func (m *Manager) ReportQuota(ctx context.Context, id string, retryAfter time.Duration) error {
	if retryAfter <= 0 {
		retryAfter = m.quotaDef
	}
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	until := m.now().Add(retryAfter).UTC()
	m.mu.Lock()
	m.quota[id] = until
	m.mu.Unlock()
	if err := m.repo.MarkAccountQuota(ctx, QuotaMark{Hoster: a.Hoster, AccountID: id, Until: until, Reason: "quota"}); err != nil {
		m.log.Warn("persist account quota mark", "account", id, "err", err)
	}
	a.Status = StatusQuota
	a.StatusMsg = "Cuota agotada hasta " + until.Local().Format("2006-01-02 15:04")
	a.UpdatedAt = m.now().UTC()
	if err := m.repo.UpdateAccount(ctx, a); err != nil {
		return fmt.Errorf("accounts: update: %w", err)
	}
	m.log.Info("account over quota", "account", id, "hoster", a.Hoster, "until", until)
	m.emit(ChangeUpdated, a.ID, a)
	return nil
}

// ClearQuota removes a quota block early (e.g. the user bought premium).
func (m *Manager) ClearQuota(ctx context.Context, id string) error {
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.quota, id)
	m.mu.Unlock()
	if err := m.repo.ClearAccountQuota(ctx, a.Hoster, id); err != nil {
		return fmt.Errorf("accounts: clear quota: %w", err)
	}
	if a.Status == StatusQuota {
		a.Status, a.StatusMsg = StatusUnknown, ""
		a.UpdatedAt = m.now().UTC()
		if err := m.repo.UpdateAccount(ctx, a); err != nil {
			return fmt.Errorf("accounts: update: %w", err)
		}
		m.emit(ChangeUpdated, a.ID, a)
	}
	return nil
}

// Check runs the Checker for the account and merges the result. Concurrent
// checks of the same account share one run. A checker error is recorded as
// StatusError (or StatusError with a lock message for ErrLocked) and also
// returned.
func (m *Manager) Check(ctx context.Context, id string) (*Account, error) {
	if m.checker == nil {
		return nil, ErrNoChecker
	}
	v, err, _ := m.checks.Do(id, func() (any, error) { return m.check(ctx, id) })
	if v == nil {
		return nil, err
	}
	a := *v.(*Account)
	return &a, err
}

func (m *Manager) check(ctx context.Context, id string) (*Account, error) {
	a, err := m.repo.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	prev := a.Status
	a.Status, a.StatusMsg = StatusChecking, ""
	m.emit(ChangeUpdated, a.ID, a)

	var res *CheckResult
	if _, serr := m.secrets.Get(ctx, id); errors.Is(serr, ErrLocked) {
		err = ErrLocked
	} else {
		cp := *a
		cp.Status = prev
		res, err = m.checker(ctx, &cp)
	}
	now := m.now().UTC()
	switch {
	case err != nil:
		a.Status = StatusError
		a.StatusMsg = checkErrorMessage(err)
	case res == nil:
		a.Status, a.StatusMsg = StatusError, "la comprobación no devolvió resultado"
	default:
		a.Status = cmp.Or(res.Status, StatusOK)
		a.StatusMsg = res.Message
		a.Premium = res.Premium
		a.PremiumUntil = res.PremiumUntil
		a.QuotaTotal = res.QuotaTotal
		a.QuotaUsed = res.QuotaUsed
	}
	if !a.Enabled {
		a.Status = StatusDisabled
	}
	a.CheckedAt = now
	a.UpdatedAt = now
	// Keep the quota block in sync with what the hoster says.
	if err == nil && res != nil && a.Status == StatusOK && a.QuotaLeft() != 0 {
		m.mu.Lock()
		_, blocked := m.quota[id]
		delete(m.quota, id)
		m.mu.Unlock()
		if blocked {
			if cerr := m.repo.ClearAccountQuota(ctx, a.Hoster, id); cerr != nil {
				m.log.Warn("clear account quota mark", "account", id, "err", cerr)
			}
		}
	} else if a.Status == StatusOK || a.Status == StatusQuota {
		if u := m.QuotaBlockedUntil(id); !u.IsZero() {
			a.Status = StatusQuota
			a.StatusMsg = cmp.Or(a.StatusMsg, "Cuota agotada hasta "+u.Local().Format("2006-01-02 15:04"))
		}
	}
	if uerr := m.repo.UpdateAccount(ctx, a); uerr != nil {
		return nil, fmt.Errorf("accounts: update: %w", uerr)
	}
	m.log.Info("account checked", "account", id, "hoster", a.Hoster, "status", a.Status)
	m.emit(ChangeUpdated, a.ID, a)
	return a, err
}

func checkErrorMessage(err error) string {
	if errors.Is(err, ErrLocked) {
		return "El almacén de credenciales está bloqueado: desbloquéalo con la contraseña maestra."
	}
	if errors.Is(err, ErrSecretNotFound) {
		return "No hay credenciales guardadas para esta cuenta."
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

var mfaCode = regexp.MustCompile(`^[0-9]{6,8}$`)

// SubmitMFA stores a 2FA code in Secret.Extra[MFAExtraKey], re-runs the
// check (the plugin logs in with it) and then removes the code from the
// secret. The account usually reported StatusNeeds2FA before.
func (m *Manager) SubmitMFA(ctx context.Context, id, code string) (*Account, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if !mfaCode.MatchString(code) {
		return nil, ErrInvalidMFACode
	}
	if m.checker == nil {
		return nil, ErrNoChecker
	}
	if _, err := m.repo.GetAccount(ctx, id); err != nil {
		return nil, err
	}
	sec, err := m.secrets.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("accounts: read credentials: %w", err)
	}
	if sec.Extra == nil {
		sec.Extra = map[string]string{}
	}
	sec.Extra[MFAExtraKey] = code
	if err := m.secrets.Set(ctx, id, sec); err != nil {
		return nil, fmt.Errorf("accounts: save credentials: %w", err)
	}
	a, cerr := m.Check(ctx, id)
	// The plugin may have stored a new session meanwhile: re-read.
	if sec, err := m.secrets.Get(ctx, id); err == nil {
		if _, ok := sec.Extra[MFAExtraKey]; ok {
			delete(sec.Extra, MFAExtraKey)
			if len(sec.Extra) == 0 {
				sec.Extra = nil
			}
			if err := m.secrets.Set(ctx, id, sec); err != nil {
				m.log.Warn("remove 2FA code from secret", "account", id, "err", err)
			}
		}
	}
	return a, cerr
}

// StoreStatus reports the secret store backend and lock state.
func (m *Manager) StoreStatus() StoreStatus { return StatusOf(m.secrets) }

// Unlock opens a vault-backed store (creating it with password on first
// use). It returns ErrNotLockable for the keyring.
func (m *Manager) Unlock(ctx context.Context, password string) error {
	l, ok := AsLockable(m.secrets)
	if !ok {
		return ErrNotLockable
	}
	if err := l.Unlock(ctx, password); err != nil {
		m.log.Warn("secret store unlock failed", "err", err)
		return err
	}
	m.log.Info("secret store unlocked")
	m.emit(ChangeUnlocked, "", nil)
	return nil
}

// Lock locks a vault-backed store.
func (m *Manager) Lock() error {
	l, ok := AsLockable(m.secrets)
	if !ok {
		return ErrNotLockable
	}
	l.Lock()
	m.log.Info("secret store locked")
	m.emit(ChangeLocked, "", nil)
	return nil
}

// ChangeMasterPassword re-encrypts a vault-backed store.
func (m *Manager) ChangeMasterPassword(ctx context.Context, oldPassword, newPassword string) error {
	l, ok := AsLockable(m.secrets)
	if !ok {
		return ErrNotLockable
	}
	if err := l.ChangePassword(ctx, oldPassword, newPassword); err != nil {
		return err
	}
	m.log.Info("secret store master password changed")
	m.emit(ChangeUnlocked, "", nil)
	return nil
}

// QuotaMarks returns a snapshot of the active account quota blocks.
func (m *Manager) QuotaMarks() map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := maps.Clone(m.quota)
	maps.DeleteFunc(out, func(_ string, u time.Time) bool { return !u.After(now) })
	return out
}

// NewID returns a random RFC 4122 version 4 UUID.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
