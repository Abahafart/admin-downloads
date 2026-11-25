package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"admindescargas/internal/config"
)

func TestDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad token"})
			return
		}
		switch r.URL.Path {
		case "/api/v1/health":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/echo":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(map[string]string{"got": in["x"], "q": r.URL.Query().Get("q")})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "tok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := c.Do(ctx, http.MethodPost, "echo", map[string][]string{"q": {"1"}}, map[string]string{"x": "y"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["got"] != "y" || out["q"] != "1" {
		t.Fatalf("out = %v", out)
	}
	if err := c.Do(ctx, http.MethodGet, "/missing", nil, nil, nil); !IsStatus(err, 404) {
		t.Fatalf("err = %v", err)
	}
	c.Token = "wrong"
	err = c.Health(ctx)
	var apiErr *Error
	if !IsStatus(err, 401) || !errors.As(err, &apiErr) || apiErr.Message != "bad token" {
		t.Fatalf("err = %v", err)
	}
}

func TestFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Listen = "0.0.0.0:8080"
	cfg.Server.Token = "t"
	c, err := FromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL.String() != "http://127.0.0.1:8080" || c.Token != "t" {
		t.Fatalf("base = %s", c.BaseURL)
	}
	cfg.Server.Listen = "[::1]:9"
	cfg.Server.TLSCert = "cert.pem"
	c, _ = FromConfig(cfg)
	if c.BaseURL.String() != "https://[::1]:9" {
		t.Fatalf("base = %s", c.BaseURL)
	}
	if _, err := New("ftp://x", ""); err == nil {
		t.Fatal("ftp accepted")
	}
}
