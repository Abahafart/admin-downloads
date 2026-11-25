package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"admindescargas/internal/config"
)

func (h *harness) login(t *testing.T, password string) (*http.Cookie, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/api/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var lr LoginResponse
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	var c *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == SessionCookie {
			c = ck
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("session cookie = %+v", c)
	}
	if lr.CSRF == "" {
		t.Fatal("no csrf token")
	}
	return c, lr.CSRF
}

func TestAuthMatrix(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t, "correct horse")
	sameOrigin := h.srv.URL
	cases := []struct {
		name         string
		method, path string
		body         string
		opts         []reqOpt
		want         int
		code         string
	}{
		{"token GET", "GET", "/api/v1/stats", "", nil, 200, ""},
		{"token POST", "POST", "/api/v1/pause-all", "", nil, 200, ""},
		{"token lowercase scheme", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Authorization", "bearer "+testToken)}, 200, ""},
		{"wrong token", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Authorization", "Bearer nope")}, 401, CodeUnauthorized},
		{"basic auth", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Authorization", "Basic Zm9vOmJhcg==")}, 401, CodeUnauthorized},
		{"missing auth", "GET", "/api/v1/stats", "", []reqOpt{noAuth()}, 401, CodeUnauthorized},
		{"missing auth POST", "POST", "/api/v1/links", `{"text":"x"}`, []reqOpt{noAuth()}, 401, CodeUnauthorized},
		{"cookie GET", "GET", "/api/v1/stats", "", []reqOpt{withCookie(cookie)}, 200, ""},
		{"cookie POST no csrf", "POST", "/api/v1/pause-all", "", []reqOpt{withCookie(cookie)}, 403, CodeCSRF},
		{"cookie POST bad csrf", "POST", "/api/v1/pause-all", "", []reqOpt{withCookie(cookie), withHeader(CSRFHeader, "x")}, 403, CodeCSRF},
		{"cookie POST csrf", "POST", "/api/v1/pause-all", "", []reqOpt{withCookie(cookie), withHeader(CSRFHeader, csrf), withHeader("Origin", sameOrigin)}, 200, ""},
		{"cookie forged", "GET", "/api/v1/stats", "", []reqOpt{withCookie(&http.Cookie{Name: SessionCookie, Value: "forged"})}, 401, CodeUnauthorized},
		{"bad origin", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Origin", "https://evil.example")}, 403, CodeBadOrigin},
		{"null origin", "POST", "/api/v1/pause-all", "", []reqOpt{withHeader("Origin", "null")}, 403, CodeBadOrigin},
		{"bad origin on login", "POST", "/api/v1/auth/login", `{"password":"x"}`, []reqOpt{noAuth(), withHeader("Origin", "http://evil.example")}, 403, CodeBadOrigin},
		{"same origin", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Origin", sameOrigin)}, 200, ""},
		{"extension origin", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Origin", "chrome-extension://abcdef")}, 200, ""},
		{"desktop origin", "GET", "/api/v1/stats", "", []reqOpt{withHeader("Origin", "wails://wails.localhost")}, 200, ""},
		{"rebinding host", "GET", "/api/v1/stats", "", []reqOpt{func(r *http.Request) { r.Host = "evil.example:7979" }}, 403, CodeBadHost},
		{"rebinding host on UI", "GET", "/", "", []reqOpt{noAuth(), func(r *http.Request) { r.Host = "attacker.test" }}, 403, CodeBadHost},
		{"localhost host", "GET", "/api/v1/stats", "", []reqOpt{func(r *http.Request) { r.Host = "localhost:7979" }}, 200, ""},
		{"ipv6 host", "GET", "/api/v1/stats", "", []reqOpt{func(r *http.Request) { r.Host = "[::1]:7979" }}, 200, ""},
	}
	for _, c := range cases {
		status, body, _ := h.do(c.method, c.path, c.body, c.opts...)
		if status != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, status, body, c.want)
			continue
		}
		if c.code != "" && errCode(t, body) != c.code {
			t.Errorf("%s: code %s, want %s", c.name, errCode(t, body), c.code)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	h := newHarness(t)
	status, _, hdr := h.do("OPTIONS", "/api/v1/links", "", noAuth(),
		withHeader("Origin", "wails://wails.localhost"), withHeader("Access-Control-Request-Method", "POST"))
	if status != 204 || hdr.Get("Access-Control-Allow-Origin") != "wails://wails.localhost" ||
		!strings.Contains(hdr.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight = %d %v", status, hdr)
	}
	status, _, hdr = h.do("GET", "/api/v1/stats", "", withHeader("Origin", "chrome-extension://abcdef"))
	if status != 200 || hdr.Get("Access-Control-Allow-Origin") != "chrome-extension://abcdef" {
		t.Fatalf("cors GET = %d %v", status, hdr)
	}
	status, _, _ = h.do("OPTIONS", "/api/v1/links", "", noAuth(),
		withHeader("Origin", "https://evil.example"), withHeader("Access-Control-Request-Method", "POST"))
	if status != 403 {
		t.Fatalf("evil preflight = %d", status)
	}
}

func TestLoginLogoutSession(t *testing.T) {
	h := newHarness(t)
	status, body, _ := h.do("GET", "/api/v1/auth/session", "", noAuth())
	if status != 200 || !strings.Contains(body, `"authenticated":false`) || !strings.Contains(body, `"password_required":true`) {
		t.Fatalf("anon session = %d %s", status, body)
	}
	cookie, csrf := h.login(t, "correct horse")
	_, body, _ = h.do("GET", "/api/v1/auth/session", "", withCookie(cookie))
	if !strings.Contains(body, `"method":"cookie"`) || !strings.Contains(body, csrf) {
		t.Fatalf("cookie session = %s", body)
	}
	if status, _, _ := h.do("POST", "/api/v1/auth/logout", "", withCookie(cookie)); status != 200 {
		t.Fatalf("logout = %d", status)
	}
	if status, _, _ := h.do("GET", "/api/v1/stats", "", withCookie(cookie)); status != 401 {
		t.Fatalf("after logout = %d", status)
	}
	// Expiry.
	cookie, _ = h.login(t, "correct horse")
	h.clock.Add(sessionTTL + time.Second)
	if status, _, _ := h.do("GET", "/api/v1/stats", "", withCookie(cookie)); status != 401 {
		t.Fatalf("expired session = %d", status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < freeLoginFails; i++ {
		if status, _, _ := h.do("POST", "/api/v1/auth/login", `{"password":"wrong"}`, noAuth()); status != 401 {
			t.Fatalf("attempt %d = %d", i, status)
		}
	}
	status, body, hdr := h.do("POST", "/api/v1/auth/login", `{"password":"correct horse"}`, noAuth())
	if status != 429 || errCode(t, body) != CodeRateLimited || hdr.Get("Retry-After") == "" {
		t.Fatalf("blocked login = %d %s", status, body)
	}
	h.clock.Add(loginBlockBase + time.Second)
	h.login(t, "correct horse")
	// Success resets the counter.
	if status, _, _ := h.do("POST", "/api/v1/auth/login", `{"password":"wrong"}`, noAuth()); status != 401 {
		t.Fatalf("after reset = %d", status)
	}
}

func TestLoginWithoutPasswordLoopbackOnly(t *testing.T) {
	h := newHarness(t, func(d *Deps) { d.Server.PasswordHash = "" })
	_, body, _ := h.do("GET", "/api/v1/auth/session", "", noAuth())
	if !strings.Contains(body, `"password_required":false`) {
		t.Fatalf("session = %s", body)
	}
	h.login(t, "") // httptest listens on 127.0.0.1 and the config listen is loopback

	h2 := newHarness(t, func(d *Deps) { d.Server.PasswordHash = ""; d.Server.Listen = "0.0.0.0:7979" })
	if status, _, _ := h2.do("POST", "/api/v1/auth/login", `{"password":""}`, noAuth()); status != 403 {
		t.Fatalf("non-loopback listen without password = %d", status)
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash = %s", hash)
	}
	if ok, err := VerifyPassword(hash, "s3cr3t"); !ok || err != nil {
		t.Fatalf("verify = %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(hash, "nope"); ok {
		t.Fatal("wrong password accepted")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=1024,t=1,p=1$!!$!!"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("accepted bad hash %q", bad)
		}
	}
}

func TestGuardNormalizeOrigin(t *testing.T) {
	g := newGuard(config.ServerConfig{Listen: "127.0.0.1:7979", AllowedHosts: []string{"nas.lan"}, AllowedOrigins: []string{"HTTP://Desk.Example:80/"}}, nil)
	if !g.origins["http://desk.example"] {
		t.Fatalf("origins = %v", g.origins)
	}
	for host, want := range map[string]bool{"nas.lan:7979": true, "NAS.LAN.": true, "127.0.0.1": true, "10.1.2.3:80": true, "evil.lan": false, "": false} {
		if got := g.hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v", host, got)
		}
	}
}
