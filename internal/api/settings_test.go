package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"admindescargas/internal/config"
	"admindescargas/internal/store"
)

type memPrefs struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *memPrefs) GetSetting(_ context.Context, k string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[k]
	if !ok {
		return "", fmt.Errorf("setting %q: %w", k, store.ErrNotFound)
	}
	return v, nil
}

func (p *memPrefs) SetSetting(_ context.Context, k, v string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[k] = v
	return nil
}

func TestConfigSettingsPersistAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.DownloadDir = filepath.Join(dir, "dl")
	cfg.Server.Token = "keep-this-token"
	cfg.Egress.Router = config.RouterConfig{Enabled: true, Method: "script", Script: []string{"/usr/bin/reconnect"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	prefs := &memPrefs{m: map[string]string{}}
	var applied *config.Config
	cs := NewConfigSettings(path, cfg, prefs)
	cs.OnChange = func(c *config.Config) { applied = c }

	h := newHarness(t, func(d *Deps) { d.Settings = cs })
	status, body, _ := h.do("PUT", "/api/v1/settings", `{
		"downloads": {"max_active": 7},
		"rotation": {"min_global_interval": "10m"},
		"appearance": {"estilo": "medianoche", "tema": "claro", "acento": "#4b52d6"},
		"network": {"router": {"script": ["rm", "-rf", "/"]}},
		"server": {"listen": "0.0.0.0:1"}
	}`)
	if status != 200 {
		t.Fatalf("put = %d %s", status, body)
	}
	if !strings.Contains(body, `"max_active":7`) || !strings.Contains(body, `"/usr/bin/reconnect"`) || strings.Contains(body, `"rm"`) {
		t.Fatalf("view = %s", body)
	}
	if !strings.Contains(body, `"listen":"127.0.0.1:7979"`) {
		t.Fatalf("server section was editable: %s", body)
	}
	disk, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Downloads.MaxActive != 7 || disk.Rotation.MinGlobalInterval.String() != "10m0s" ||
		disk.Server.Token != "keep-this-token" || disk.Egress.Router.Script[0] != "/usr/bin/reconnect" || disk.Server.Listen != "127.0.0.1:7979" {
		t.Fatalf("disk = %+v", disk)
	}
	if applied == nil || applied.Downloads.MaxActive != 7 {
		t.Fatal("OnChange not called")
	}
	if !strings.Contains(prefs.m[AppearanceKey], "medianoche") {
		t.Fatalf("prefs = %v", prefs.m)
	}

	// A fresh service reads the appearance back from the store.
	cs2 := NewConfigSettings(path, disk, prefs)
	s, err := cs2.Get(context.Background())
	if err != nil || s.Appearance.Estilo != "medianoche" || s.Appearance.Acento != "#4b52d6" {
		t.Fatalf("get = %+v %v", s.Appearance, err)
	}

	// Invalid values are rejected and nothing is written.
	before, _ := os.ReadFile(path)
	for _, bad := range []string{
		`{"rotation":{"strategy":"chaos"}}`,
		`{"rotation":{"default_retry_after":"soon"}}`,
		`{"downloads":{"exists_policy":"maybe"}}`,
		`{"appearance":{"estilo":"grafito","tema":"sistema","acento":"red"}}`,
		`{"download_dir":""}`,
	} {
		if status, body, _ := h.do("PUT", "/api/v1/settings", bad); status != 400 {
			t.Errorf("PUT %s = %d %s", bad, status, body)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid PUT modified config.toml")
	}
}

func TestConfigSettingsRestartRequired(t *testing.T) {
	cs := NewConfigSettings("", config.Default(), nil)
	s, _ := cs.Get(context.Background())
	if s.RestartRequired {
		t.Fatal("restart required at start")
	}
	s.Appearance.Tema = "oscuro"
	s, _ = cs.Put(context.Background(), s)
	if s.RestartRequired {
		t.Fatal("appearance change should not require a restart")
	}
	s.Downloads.MaxRetries = 9
	s, err := cs.Put(context.Background(), s)
	if err != nil || !s.RestartRequired || s.Downloads.MaxRetries != 9 {
		t.Fatalf("put = %+v %v", s, err)
	}
}
