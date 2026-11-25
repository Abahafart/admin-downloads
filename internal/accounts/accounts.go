// Package accounts defines hoster accounts and the SecretStore that keeps
// their credentials.
//
// Only metadata (Account) is stored in SQLite (table accounts); secrets
// (Secret) live in a SecretStore: the OS keyring (zalando/go-keyring) or, on
// headless machines, an encrypted vault (AES-256-GCM, Argon2id key). Those
// implementations live in keyring.go / vault.go (accounts agent). This file
// only holds the contracts plus MemoryStore for tests.
//
// Secrets must never be logged: Secret implements fmt.Stringer,
// fmt.GoStringer and slog.LogValuer to print "[REDACTED]".
package accounts

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/oauth2"
)

// Kind is the type of credential an account uses.
type Kind string

// Account kinds.
const (
	KindPassword Kind = "password" // MEGA (login v1/v2, 2FA)
	KindCookie   Kind = "cookie"   // TeraBox (ndus), MediaFire (optional)
	KindOAuth    Kind = "oauth"    // Google Drive
	KindAPIKey   Kind = "apikey"   // Pixeldrain, 1fichier
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindPassword, KindCookie, KindOAuth, KindAPIKey:
		return true
	}
	return false
}

// Status is the result of the last account check.
type Status string

// Account statuses.
const (
	StatusUnknown  Status = "unknown"   // never checked
	StatusOK       Status = "ok"        // login works
	StatusInvalid  Status = "invalid"   // wrong credentials
	StatusExpired  Status = "expired"   // cookie/token/premium expired
	StatusQuota    Status = "quota"     // traffic quota exhausted for now
	StatusNeeds2FA Status = "needs_2fa" // login requires a 2FA code from the UI
	StatusError    Status = "error"     // check failed for another reason
	StatusDisabled Status = "disabled"  // disabled by the user
	StatusChecking Status = "checking"  // check in progress
)

// Account is the non-secret metadata of a hoster account (table accounts).
type Account struct {
	ID       string `json:"id"`       // random, stable (UUID); also the SecretStore key
	Hoster   string `json:"hoster"`   // hosters.Hoster.Name(), e.g. "mega"
	Label    string `json:"label"`    // user-visible name
	Kind     Kind   `json:"kind"`     // credential type
	Username string `json:"username"` // login/e-mail if any (not secret); may be empty
	Enabled  bool   `json:"enabled"`
	// AllowRotation permits IP rotation while downloading with this account.
	// Off by default: account quotas do not depend on IP and rotating may get
	// the account flagged (PLAN.md section 15).
	AllowRotation bool `json:"allow_rotation"`

	Status       Status    `json:"status"`
	StatusMsg    string    `json:"status_msg,omitempty"`
	Premium      bool      `json:"premium"`
	PremiumUntil time.Time `json:"premium_until,omitzero"`
	QuotaTotal   int64     `json:"quota_total"` // bytes; -1 = unknown
	QuotaUsed    int64     `json:"quota_used"`  // bytes; -1 = unknown
	CheckedAt    time.Time `json:"checked_at,omitzero"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// QuotaLeft returns the remaining quota in bytes, or -1 if unknown.
func (a *Account) QuotaLeft() int64 {
	if a.QuotaTotal < 0 || a.QuotaUsed < 0 {
		return -1
	}
	if a.QuotaUsed >= a.QuotaTotal {
		return 0
	}
	return a.QuotaTotal - a.QuotaUsed
}

// CheckResult is what a hoster's account check reports (see
// hosters.AccountChecker). It is merged into Account by the account manager.
type CheckResult struct {
	Status       Status
	Message      string
	Premium      bool
	PremiumUntil time.Time
	QuotaTotal   int64 // -1 = unknown
	QuotaUsed    int64 // -1 = unknown
}

// Secret holds the credentials of one account. Only the fields relevant to
// the account Kind are set. It is serialized as JSON by SecretStore
// implementations.
type Secret struct {
	Password string `json:"password,omitempty"`
	// Cookies holds imported cookies, as pasted (Netscape cookies.txt or JSON).
	Cookies    string        `json:"cookies,omitempty"`
	APIKey     string        `json:"api_key,omitempty"`
	OAuthToken *oauth2.Token `json:"oauth_token,omitempty"`
	// Session is a hoster session token cached to avoid re-sending the
	// password (e.g. MEGA sid). Hosters update it via Session.SetSecret.
	Session string `json:"session,omitempty"`
	// Extra holds hoster-specific secret values (e.g. MEGA master key).
	Extra map[string]string `json:"extra,omitempty"`
}

const redacted = "[REDACTED]"

// String implements fmt.Stringer and never reveals the secret.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer ("%#v") and never reveals the secret.
func (Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer and never reveals the secret.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// IsZero reports whether no credential is set.
func (s Secret) IsZero() bool {
	return s.Password == "" && s.Cookies == "" && s.APIKey == "" && s.OAuthToken == nil && s.Session == "" && len(s.Extra) == 0
}

// ErrSecretNotFound is returned by SecretStore.Get when nothing is stored for
// the account.
var ErrSecretNotFound = errors.New("accounts: secret not found")

// ErrLocked is returned by vault-backed stores while the master password has
// not been provided.
var ErrLocked = errors.New("accounts: secret store is locked")

// SecretStore persists account secrets keyed by Account.ID. Implementations
// must be safe for concurrent use.
type SecretStore interface {
	// Get returns ErrSecretNotFound if nothing is stored for accountID.
	Get(ctx context.Context, accountID string) (Secret, error)
	// Set creates or replaces the secret.
	Set(ctx context.Context, accountID string, s Secret) error
	// Delete removes the secret; deleting a missing secret is not an error.
	Delete(ctx context.Context, accountID string) error
}
