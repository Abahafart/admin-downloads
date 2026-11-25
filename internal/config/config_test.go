package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFirstRunCreatesFileWithToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.Token) < 40 {
		t.Fatalf("token too short: %q", cfg.Server.Token)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", st.Mode().Perm())
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Server.Token != cfg.Server.Token {
		t.Fatal("token must be stable across loads")
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
download_dir = "/srv/dl"

[server]
token = "abc"

[downloads]
max_active = 7
retry_backoff = "750ms"

[hosters.mega]
retry_after = "2h"

[hosters.terabox]
domains = ["terabox.example"]

[[egress.wireguard]]
name = "mullvad"
confs = ["/etc/wg/*.conf"]
max_simultaneous = 5

[[egress.system_vpn]]
name = "mullvad"
rotate = ["mullvad", "reconnect"]
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DownloadDir != "/srv/dl" || cfg.Server.Token != "abc" {
		t.Fatalf("overrides lost: %+v", cfg.Server)
	}
	if cfg.Downloads.MaxActive != 7 || cfg.Downloads.ConnsPerFile != 8 {
		t.Fatalf("downloads = %+v", cfg.Downloads)
	}
	if cfg.Downloads.RetryBackoff.Duration != 750*time.Millisecond {
		t.Fatalf("retry_backoff = %v", cfg.Downloads.RetryBackoff)
	}
	if got := cfg.Hoster("mega").RetryAfter.Duration; got != 2*time.Hour {
		t.Fatalf("mega retry_after = %v", got)
	}
	if got := cfg.Hoster("terabox").Domains; len(got) != 1 || got[0] != "terabox.example" {
		t.Fatalf("terabox domains = %v", got)
	}
	if _, ok := cfg.Hosters["gdrive"]; !ok {
		t.Fatal("default hoster entries must survive a partial [hosters] table")
	}
	if len(cfg.Egress.WireGuard) != 1 || cfg.Egress.WireGuard[0].MaxSimultaneous != 5 {
		t.Fatalf("wireguard = %+v", cfg.Egress.WireGuard)
	}
	if cfg.Egress.SystemVPN[0].Rotate[1] != "reconnect" {
		t.Fatalf("system_vpn = %+v", cfg.Egress.SystemVPN)
	}
	if cfg.Server.Listen != "127.0.0.1:7979" {
		t.Fatalf("listen default lost: %q", cfg.Server.Listen)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	cfg.Server.Token = "tok"
	cfg.Rotation.MinGlobalInterval = D(90 * time.Second)
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `min_global_interval = '1m30s'`) && !strings.Contains(string(data), `min_global_interval = "1m30s"`) {
		t.Fatalf("duration not encoded as string:\n%s", data)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rotation.MinGlobalInterval.Duration != 90*time.Second || got.Server.Token != "tok" {
		t.Fatalf("round trip mismatch: %+v", got.Rotation)
	}
}

func TestValidateRejectsPublicListenWithoutPassword(t *testing.T) {
	cfg := Default()
	cfg.Server.Listen = "0.0.0.0:7979"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error")
	}
	cfg.Server.PasswordHash = "$argon2id$..."
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(path, []byte("this is = = not toml"), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
