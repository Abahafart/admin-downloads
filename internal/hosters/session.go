package hosters

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"sync"

	"golang.org/x/net/publicsuffix"

	"admindescargas/internal/accounts"
	"admindescargas/internal/netx"
)

// ErrNoAccount is returned by Session.Secret for anonymous sessions.
var ErrNoAccount = errors.New("hosters: anonymous session has no account")

// Session is the identity a plugin acts with: one per (hoster, egress,
// account). The engine creates it and reuses it for every file of that
// identity, so plugins may cache logins in it (SetValue). When the egress
// changes (rotation) the engine builds a new Session; cached values do not
// carry over, which is intended (a new IP is a new identity).
type Session struct {
	// Hoster is the plugin name this session belongs to.
	Hoster string
	// Client routes every request through Egress and uses Jar.
	Client *http.Client
	// Jar is private to this session so identities never mix.
	Jar http.CookieJar
	// Account is nil for anonymous downloads.
	Account *accounts.Account
	// Egress is the network exit used by Client.
	Egress netx.Egress
	// Secrets gives access to the account's credentials. Set by the engine
	// when Account != nil; use Secret / SetSecret instead of calling it.
	Secrets accounts.SecretStore
	// Log is a logger already annotated with hoster/egress/account ids.
	// Never log secrets, cookies or signed URLs through it.
	Log *slog.Logger

	mu     sync.Mutex
	values map[string]any
}

// NewSession builds a session that sends its traffic through e (required)
// with the given account (nil = anonymous) and cookie jar (nil = a fresh
// in-memory jar with the public suffix list).
func NewSession(e netx.Egress, acct *accounts.Account, jar http.CookieJar) *Session {
	if jar == nil {
		// cookiejar.New only fails on invalid options; publicsuffix is valid.
		jar, _ = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	}
	return &Session{
		Client:  netx.NewHTTPClient(e, jar),
		Jar:     jar,
		Account: acct,
		Egress:  e,
		Log:     slog.Default(),
	}
}

// Anonymous reports whether the session has no account.
func (s *Session) Anonymous() bool { return s.Account == nil }

// Secret loads the account's credentials. It returns ErrNoAccount for
// anonymous sessions.
func (s *Session) Secret(ctx context.Context) (accounts.Secret, error) {
	if s.Account == nil {
		return accounts.Secret{}, ErrNoAccount
	}
	if s.Secrets == nil {
		return accounts.Secret{}, accounts.ErrSecretNotFound
	}
	return s.Secrets.Get(ctx, s.Account.ID)
}

// SetSecret stores updated credentials (refreshed OAuth token, new MEGA sid).
func (s *Session) SetSecret(ctx context.Context, sec accounts.Secret) error {
	if s.Account == nil {
		return ErrNoAccount
	}
	if s.Secrets == nil {
		return errors.New("hosters: session has no secret store")
	}
	return s.Secrets.Set(ctx, s.Account.ID, sec)
}

// Value returns a plugin-cached value (login state, tokens scraped from a
// page). Keys should be prefixed with the plugin name.
func (s *Session) Value(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok
}

// SetValue caches a value for the lifetime of the session.
func (s *Session) SetValue(key string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]any)
	}
	s.values[key] = v
}

// DeleteValue removes a cached value (e.g. after the hoster rejected it).
func (s *Session) DeleteValue(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
}
