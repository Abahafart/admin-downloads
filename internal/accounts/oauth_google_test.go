package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"admindescargas/internal/config"
)

// fakeGoogle is a token endpoint that validates the PKCE verifier against
// the challenge sent to the auth endpoint.
type fakeGoogle struct {
	mu         sync.Mutex
	challenges map[string]string // code -> challenge
	scope      string
	exchanges  int
}

func (f *fakeGoogle) issue(challenge string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := "code-" + challenge[:8]
	f.challenges[code] = challenge
	return code
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/token" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	f.exchanges++
	ch, ok := f.challenges[r.PostForm.Get("code")]
	delete(f.challenges, r.PostForm.Get("code"))
	f.mu.Unlock()
	id, secret, _ := r.BasicAuth()
	if id == "" {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	w.Header().Set("Content-Type", "application/json")
	if !ok || id != "cid" || secret != "csecret" || r.PostForm.Get("grant_type") != "authorization_code" ||
		oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != ch {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "ya29.access", "refresh_token": "1//refresh", "expires_in": 3599,
		"token_type": "Bearer", "scope": f.scope,
	})
}

type oauthHarness struct {
	h      *harness
	g      *GoogleOAuth
	google *fakeGoogle
	srv    *httptest.Server
	acct   *Account
	now    *fakeClock
}

const testRedirect = "http://127.0.0.1:7979/api/v1/oauth/google/callback"

func newOAuthHarness(t *testing.T) *oauthHarness {
	t.Helper()
	h := newHarness(t, nil)
	fg := &fakeGoogle{challenges: map[string]string{}, scope: GoogleDriveScope + " openid"}
	srv := httptest.NewServer(fg)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Hosters = map[string]config.HosterConfig{"gdrive": {Options: map[string]string{"client_id": "cid", "client_secret": " csecret "}}}
	clock := &fakeClock{t: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	g, err := NewGoogleOAuth(GoogleOAuthOptions{
		Credentials: GoogleCredentialsFromConfig(cfg),
		Target:      h.m,
		HTTPClient:  srv.Client(),
		Endpoint:    oauth2.Endpoint{AuthURL: "https://accounts.example/o/oauth2/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams},
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.m.Create(context.Background(), NewAccount{Hoster: "gdrive", Kind: KindOAuth, Username: "me@gmail.com"})
	if err != nil {
		t.Fatal(err)
	}
	return &oauthHarness{h: h, g: g, google: fg, srv: srv, acct: a, now: clock}
}

// consent simulates the browser at Google: returns the callback URL.
func (o *oauthHarness) consent(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "cid", "redirect_uri": testRedirect, "response_type": "code", "access_type": "offline",
		"prompt": "consent", "code_challenge_method": "S256", "scope": GoogleDriveScope, "login_hint": "me@gmail.com",
	} {
		if q.Get(k) != want {
			t.Fatalf("auth URL %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("state")) < 32 || q.Get("code_challenge") == "" {
		t.Fatalf("missing state/PKCE: %s", authURL)
	}
	code := o.google.issue(q.Get("code_challenge"))
	return testRedirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=" + url.QueryEscape(code) + "&scope=" + url.QueryEscape(GoogleDriveScope)
}

func callback(g *GoogleOAuth, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	g.HandleCallback(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestGoogleOAuthFlow(t *testing.T) {
	ctx := context.Background()
	o := newOAuthHarness(t)
	authURL, err := o.g.Start(ctx, o.acct.ID, testRedirect)
	if err != nil {
		t.Fatal(err)
	}
	cb := o.consent(t, authURL)
	rec := callback(o.g, cb)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cuenta de Google conectada") {
		t.Fatalf("callback %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", rec.Header())
	}
	s, err := o.h.sec.Get(ctx, o.acct.ID)
	if err != nil || s.OAuthToken == nil || s.OAuthToken.AccessToken != "ya29.access" || s.OAuthToken.RefreshToken != "1//refresh" {
		t.Fatalf("token not stored: %v %+v", err, s.OAuthToken)
	}
	// Replaying the same callback fails: state is single use.
	rec = callback(o.g, cb)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "caducado") {
		t.Fatalf("replay %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGoogleOAuthPKCEMismatch(t *testing.T) {
	ctx := context.Background()
	o := newOAuthHarness(t)
	authURL, _ := o.g.Start(ctx, o.acct.ID, testRedirect)
	u, _ := url.Parse(authURL)
	// The code was issued for a different challenge (attacker's flow).
	code := o.google.issue("attacker-challenge-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	rec := callback(o.g, testRedirect+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code="+code)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("PKCE mismatch accepted: %d %s", rec.Code, rec.Body.String())
	}
	if s, _ := o.h.sec.Get(ctx, o.acct.ID); s.OAuthToken != nil {
		t.Fatal("token stored despite PKCE mismatch")
	}
}

func TestGoogleOAuthStateErrors(t *testing.T) {
	ctx := context.Background()
	o := newOAuthHarness(t)

	// Unknown state never reaches the token endpoint.
	rec := callback(o.g, testRedirect+"?state=forged&code=x")
	if rec.Code != http.StatusBadRequest || o.google.exchanges != 0 {
		t.Fatalf("forged state: %d, exchanges %d", rec.Code, o.google.exchanges)
	}

	// Expired state.
	authURL, _ := o.g.Start(ctx, o.acct.ID, testRedirect)
	cb := o.consent(t, authURL)
	o.now.Add(11 * time.Minute)
	if _, err := o.g.CompleteURL(ctx, cb); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("expired state: %v", err)
	}

	// User denied consent.
	authURL, _ = o.g.Start(ctx, o.acct.ID, testRedirect)
	u, _ := url.Parse(authURL)
	rec = callback(o.g, testRedirect+"?error=access_denied&state="+url.QueryEscape(u.Query().Get("state")))
	if !strings.Contains(rec.Body.String(), "Autorización cancelada") {
		t.Fatalf("denied page: %s", rec.Body.String())
	}

	// Drive scope unticked.
	o.google.scope = "openid"
	authURL, _ = o.g.Start(ctx, o.acct.ID, testRedirect)
	if _, err := o.g.CompleteURL(ctx, o.consent(t, authURL)); !errors.Is(err, ErrOAuthScope) {
		t.Fatalf("scope: %v", err)
	}

	// Pasted URL completes the flow (remote daemon).
	o.google.scope = GoogleDriveScope
	authURL, _ = o.g.Start(ctx, o.acct.ID, testRedirect)
	id, err := o.g.CompleteURL(ctx, "  "+o.consent(t, authURL)+"\n")
	if err != nil || id != o.acct.ID {
		t.Fatalf("CompleteURL: %s %v", id, err)
	}
}

func TestGoogleOAuthStartValidation(t *testing.T) {
	ctx := context.Background()
	o := newOAuthHarness(t)
	for _, bad := range []string{"https://evil.example/cb", "javascript:alert(1)", "http://user@127.0.0.1/cb", ""} {
		if _, err := o.g.Start(ctx, o.acct.ID, bad); !errors.Is(err, ErrBadRedirect) {
			t.Fatalf("redirect %q: %v", bad, err)
		}
	}
	for _, ok := range []string{"http://localhost:1234/cb", "http://[::1]:7979/cb"} {
		if _, err := o.g.Start(ctx, o.acct.ID, ok); err != nil {
			t.Fatalf("redirect %q: %v", ok, err)
		}
	}
	other, _ := o.h.m.Create(ctx, NewAccount{Hoster: "pixeldrain", Kind: KindAPIKey, Secret: Secret{APIKey: "k"}})
	if _, err := o.g.Start(ctx, other.ID, testRedirect); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("non-oauth account: %v", err)
	}
	if _, err := o.g.Start(ctx, "missing", testRedirect); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing account: %v", err)
	}

	g, _ := NewGoogleOAuth(GoogleOAuthOptions{Credentials: func() (string, string) { return "", "" }, Target: o.h.m})
	if g.Configured() {
		t.Fatal("should not be configured")
	}
	if _, err := g.Start(ctx, o.acct.ID, testRedirect); !errors.Is(err, ErrOAuthNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}

	// Pending flows are bounded.
	for range maxPendingFlows + 10 {
		if _, err := o.g.Start(ctx, o.acct.ID, testRedirect); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(o.g.pending); n > maxPendingFlows {
		t.Fatalf("%d pending flows", n)
	}
}
