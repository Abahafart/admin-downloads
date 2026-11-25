package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"admindescargas/internal/accounts"
	"admindescargas/internal/rotation"
)

type fakeSecrets struct {
	mu     sync.Mutex
	locked bool
	pw     string
}

func (f *fakeSecrets) Status(context.Context) (accounts.StoreStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return accounts.StoreStatus{Backend: "vault", Lockable: true, Initialized: true, Locked: f.locked}, nil
}
func (f *fakeSecrets) Unlock(_ context.Context, pw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pw != f.pw {
		return fmt.Errorf("unlock: %w", accounts.ErrWrongPassword)
	}
	f.locked = false
	return nil
}
func (f *fakeSecrets) Lock(context.Context) error {
	f.mu.Lock()
	f.locked = true
	f.mu.Unlock()
	return nil
}
func (f *fakeSecrets) ChangePassword(_ context.Context, old, nw string) error {
	if old != f.pw {
		return accounts.ErrWrongPassword
	}
	if len(nw) < 8 {
		return accounts.ErrWeakPassword
	}
	f.pw = nw
	return nil
}

func TestSecretsRoutes(t *testing.T) {
	fs := &fakeSecrets{locked: true, pw: "master-password"}
	h := newHarness(t, func(d *Deps) { d.Secrets = fs })
	if st, body, _ := h.do("GET", "/api/v1/secrets", ""); st != 200 || !strings.Contains(body, `"locked":true`) {
		t.Fatalf("status: %d %s", st, body)
	}
	if st, body, _ := h.do("GET", "/api/v1/secrets", "", noAuth()); st != 401 {
		t.Fatalf("unauthenticated status: %d %s", st, body)
	}
	if st, body, _ := h.do("POST", "/api/v1/secrets/unlock", `{"password":"master-password"}`); st != 200 || !strings.Contains(body, `"locked":false`) {
		t.Fatalf("unlock: %d %s", st, body)
	}
	if st, body, _ := h.do("POST", "/api/v1/secrets/lock", ``); st != 200 || !strings.Contains(body, `"locked":true`) {
		t.Fatalf("lock: %d %s", st, body)
	}
	if st, body, _ := h.do("POST", "/api/v1/secrets/password", `{"old_password":"master-password","new_password":"short"}`); st != 400 {
		t.Fatalf("weak password: %d %s", st, body)
	}
	// Wrong passwords are rate limited per client.
	var last int
	for i := 0; i < freeLoginFails+1; i++ {
		var body string
		last, body, _ = h.do("POST", "/api/v1/secrets/unlock", `{"password":"nope"}`)
		if i < freeLoginFails && (last != 403 || errCode(t, body) != CodeWrongPassword) {
			t.Fatalf("wrong password #%d: %d %s", i, last, body)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("unlock not rate limited: %d", last)
	}
}

func TestOAuthCompleteRoute(t *testing.T) {
	h := newHarness(t)
	if st, body, _ := h.do("POST", "/api/v1/oauth/google/complete", `{"url":"http://127.0.0.1:1/cb?state=good&code=x"}`); st != 200 {
		t.Fatalf("complete: %d %s", st, body)
	}
	if st, _, _ := h.do("POST", "/api/v1/oauth/google/complete", `{"url":"http://127.0.0.1:1/cb?state=bad"}`); st != 400 {
		t.Fatalf("bad state: %d", st)
	}
	if st, _, _ := h.do("POST", "/api/v1/oauth/google/complete", `{"url":"x"}`, noAuth()); st != 401 {
		t.Fatalf("complete without auth: %d", st)
	}
}

func TestExtensionOriginsBearerOnly(t *testing.T) {
	h := newHarness(t)
	for _, origin := range []string{"chrome-extension://zzzzunlisted", "moz-extension://1234-uuid"} {
		st, _, hdr := h.do("GET", "/api/v1/stats", "", withHeader("Origin", origin))
		if st != 200 || hdr.Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("%s with bearer: %d acao=%q", origin, st, hdr.Get("Access-Control-Allow-Origin"))
		}
		// Preflight.
		st, _, hdr = h.do("OPTIONS", "/api/v1/links", "", noAuth(), withHeader("Origin", origin),
			withHeader("Access-Control-Request-Method", "POST"))
		if st != 204 || !strings.Contains(hdr.Get("Access-Control-Allow-Headers"), "Authorization") {
			t.Fatalf("%s preflight: %d %v", origin, st, hdr)
		}
		if hdr.Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("credentials must not be allowed cross-origin")
		}
		// No login and no cookie sessions from extensions.
		if st, _, _ := h.do("POST", "/api/v1/auth/login", `{"password":"correct horse"}`, noAuth(), withHeader("Origin", origin)); st != 403 {
			t.Fatalf("%s login: %d", origin, st)
		}
	}
	// A valid cookie session is ignored when the Origin is an extension.
	st, _, hdr := h.do("POST", "/api/v1/auth/login", `{"password":"correct horse"}`, noAuth())
	if st != 200 {
		t.Fatalf("login: %d", st)
	}
	var sess *http.Cookie
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Name == SessionCookie {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("no session cookie")
	}
	if st, _, _ := h.do("GET", "/api/v1/stats", "", withCookie(sess)); st != 200 {
		t.Fatalf("cookie session: %d", st)
	}
	if st, _, _ := h.do("GET", "/api/v1/stats", "", withCookie(sess), withHeader("Origin", "chrome-extension://evil")); st != 401 {
		t.Fatalf("cookie from extension origin: %d", st)
	}
	// Other foreign origins are still refused.
	if st, _, _ := h.do("GET", "/api/v1/stats", "", withHeader("Origin", "https://evil.example")); st != 403 {
		t.Fatalf("foreign origin: %d", st)
	}
}

func TestRotationErrorMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("x: %w", rotation.ErrUnknownEgress), 404},
		{fmt.Errorf("x: %w", rotation.ErrExists), 409},
		{fmt.Errorf("x: %w", rotation.ErrNotImported), 409},
		{fmt.Errorf("x: %w", rotation.ErrNoEgress), 503},
		{fmt.Errorf("x: %w", accounts.ErrAccountNotFound), 404},
		{fmt.Errorf("x: %w", accounts.ErrWrongPassword), 403},
		{fmt.Errorf("x: %w", accounts.ErrNotLockable), 409},
		{accounts.ErrInvalidCookies, 400},
	} {
		if got, _ := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
