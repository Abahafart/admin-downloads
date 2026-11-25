package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"admindescargas/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.DownloadDir = t.TempDir()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.Token = "test-token"
	cfg.Secrets.Backend = config.SecretsMemory // never touch the real OS keyring
	cfg.Egress.HealthInterval = config.D(0)
	return cfg
}

func quiet() Option { return WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func TestStartServeStop(t *testing.T) {
	ctx := context.Background()
	a, err := New(testConfig(t), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Store() == nil || a.Registry() == nil || a.Rotation() == nil || a.Handler() == nil {
		t.Fatal("core not wired")
	}

	resp, err := http.Get(a.BaseURL() + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
	resp, err = http.Get(a.BaseURL() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui status %d", resp.StatusCode)
	}

	in, err := ReadInstance(a.Config().DataDir)
	if err != nil || in.Addr != a.Addr() {
		t.Fatalf("instance = %+v, %v", in, err)
	}

	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(ctx); err != nil {
		t.Fatal("second Stop must be a no-op")
	}
	if _, err := ReadInstance(a.Config().DataDir); err == nil {
		t.Fatal("instance file not removed")
	}
}

func TestDataDirLock(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a, _ := New(cfg, quiet(), WithoutHTTP())
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Stop(ctx)

	cfg2 := *cfg
	b, _ := New(&cfg2, quiet(), WithoutHTTP())
	if err := b.Start(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second core: err = %v, want ErrAlreadyRunning", err)
	}

	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = b.Stop(ctx)
}

func TestNewLogger(t *testing.T) {
	if _, err := NewLogger(config.LogConfig{Level: "debug", Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLogger(config.LogConfig{Level: "loud"}); err == nil {
		t.Fatal("bad level accepted")
	}
	if _, err := NewLogger(config.LogConfig{Format: "xml"}); err == nil {
		t.Fatal("bad format accepted")
	}
}
