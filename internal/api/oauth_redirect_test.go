package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"admindescargas/internal/config"
)

func TestOAuthRedirectAlwaysLoopback(t *testing.T) {
	s := &server{d: Deps{Server: config.ServerConfig{Listen: "0.0.0.0:7979"}}}
	cases := map[string]string{
		"127.0.0.1:7979": "http://127.0.0.1:7979" + callbackPath,
		"localhost:7979": "http://localhost:7979" + callbackPath,
		"[::1]:7979":     "http://[::1]:7979" + callbackPath,
		"nas:7979":       "http://127.0.0.1:7979" + callbackPath,
		"192.168.1.5":    "http://127.0.0.1:7979" + callbackPath,
	}
	for host, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/google/start?account_id=a", nil)
		r.Host = host
		if got := s.oauthRedirect(r); got != want {
			t.Errorf("host %q: got %q, want %q", host, got, want)
		}
	}
}
