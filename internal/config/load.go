package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/adrg/xdg"
	"github.com/pelletier/go-toml/v2"
)

// DefaultPath returns os.UserConfigDir()/admindescargas/config.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: user config dir: %w", err)
	}
	return filepath.Join(dir, AppName, "config.toml"), nil
}

// DefaultDataDir returns the platform data dir for the app (XDG_DATA_HOME on
// Linux, ~/Library/Application Support on macOS, %LOCALAPPDATA% on Windows).
func DefaultDataDir() string { return filepath.Join(xdg.DataHome, AppName) }

// DefaultDownloadDir returns the user's Downloads folder.
func DefaultDownloadDir() string {
	if xdg.UserDirs.Download != "" {
		return xdg.UserDirs.Download
	}
	return filepath.Join(xdg.Home, "Downloads")
}

// DefaultTeraBoxDomains are the TeraBox mirror domains known at the time of
// writing; users can extend them in [hosters.terabox].domains.
var DefaultTeraBoxDomains = []string{
	"terabox.com", "www.terabox.com", "1024terabox.com", "www.1024terabox.com",
	"teraboxapp.com", "www.teraboxapp.com", "freeterabox.com", "www.freeterabox.com",
	"4funbox.com", "www.4funbox.com", "mirrobox.com", "www.mirrobox.com",
	"nephobox.com", "www.nephobox.com", "terabox.app", "teraboxlink.com",
	"terasharelink.com", "1024tera.com", "momerybox.com", "tibibox.com",
}

// Default returns a configuration with every default filled in. It does not
// generate the API token (Load does that on first run).
func Default() *Config {
	return &Config{
		DataDir:     DefaultDataDir(),
		DownloadDir: DefaultDownloadDir(),
		Server: ServerConfig{
			Listen: "127.0.0.1:7979",
		},
		Log: LogConfig{Level: "info", Format: "text"},
		Downloads: DownloadsConfig{
			MaxActive:    3,
			MaxPerHoster: 2,
			// >= MaxActive so that a single (direct) egress does not cap
			// the global concurrency.
			MaxPerEgress:             4,
			ConnsPerFile:             8,
			MinSegmentSize:           1 << 20,
			MaxRetries:               5,
			RetryBackoff:             D(2 * time.Second),
			MaxRetryBackoff:          D(5 * time.Minute),
			ResumeOnStart:            true,
			ExistsPolicy:             ExistsSkip,
			FlushInterval:            D(time.Second),
			ResolveRequestsPerSecond: 4,
		},
		Rotation: RotationConfig{
			Enabled:           true,
			Strategy:          StrategyRoundRobin,
			GlobalRotation:    false,
			MinGlobalInterval: D(5 * time.Minute),
			VerifyIPChange:    true,
			DefaultRetryAfter: D(time.Hour),
		},
		Egress: EgressConfig{
			Direct:         true,
			HealthInterval: D(5 * time.Minute),
			Proxies:        ProxiesConfig{DedupeByIP: true},
			Tor: TorConfig{
				SocksAddr:   "127.0.0.1:9050",
				ControlAddr: "127.0.0.1:9051",
				Circuits:    4,
			},
			Router: RouterConfig{Method: "upnp", WaitAfter: D(10 * time.Second), Timeout: D(2 * time.Minute)},
		},
		Hosters: map[string]HosterConfig{
			"mega":      {RetryAfter: D(time.Hour)},
			"mediafire": {RetryAfter: D(10 * time.Minute)},
			"gdrive":    {RetryAfter: D(24 * time.Hour), Options: map[string]string{"copy_on_quota": "true", "delete_copy": "true"}},
			"terabox":   {RetryAfter: D(30 * time.Minute), Domains: slices.Clone(DefaultTeraBoxDomains)},
		},
		Clipboard: ClipboardConfig{
			Enabled:     true,
			Action:      ClipboardAsk,
			IgnoreKnown: true,
			UseWlPaste:  true,
		},
		PostProcess: PostProcessConfig{VerifyChecksums: true, Extract: false},
		Desktop: DesktopConfig{
			Mode:          DesktopIntegrated,
			CloseToTray:   true,
			Notifications: true,
		},
		Secrets: SecretsConfig{Backend: SecretsAuto},
	}
}

// Load reads path (DefaultPath() if empty) on top of Default(). If the file
// does not exist it is created with the defaults. If the API token is empty
// a new one is generated and the file is saved. The returned config is
// validated.
func Load(path string) (*Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// First run: defaults + token, written below.
	case err != nil:
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	default:
		if err := toml.NewDecoder(bytes.NewReader(data)).Decode(cfg); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	dirty := errors.Is(err, os.ErrNotExist)
	if cfg.Server.Token == "" {
		tok, err := GenerateToken()
		if err != nil {
			return nil, err
		}
		cfg.Server.Token = tok
		dirty = true
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if dirty {
		if err := Save(path, cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// Save writes cfg to path atomically with 0600 permissions (it contains the
// API token), creating parent directories as needed.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	// os.CreateTemp already creates the file with mode 0600.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// GenerateToken returns a random 256-bit token, base64url encoded.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("config: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Validate checks invariants that would otherwise surface as confusing
// runtime errors.
func (c *Config) Validate() error {
	var errs []error
	host, _, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		errs = append(errs, fmt.Errorf("server.listen %q: %w", c.Server.Listen, err))
	} else if !IsLoopbackHost(host) && c.Server.PasswordHash == "" {
		errs = append(errs, fmt.Errorf("server.listen %q is not loopback: server.password_hash is required", c.Server.Listen))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir is empty"))
	}
	if c.DownloadDir == "" {
		errs = append(errs, errors.New("download_dir is empty"))
	}
	if c.Downloads.MaxActive < 1 {
		errs = append(errs, errors.New("downloads.max_active must be >= 1"))
	}
	if c.Downloads.ConnsPerFile < 1 {
		errs = append(errs, errors.New("downloads.conns_per_file must be >= 1"))
	}
	if c.Downloads.SpeedLimit < 0 || c.Downloads.PerDownloadSpeedLimit < 0 {
		errs = append(errs, errors.New("downloads speed limits must be >= 0"))
	}
	switch c.Downloads.ExistsPolicy {
	case ExistsSkip, ExistsOverwrite, ExistsRename:
	default:
		errs = append(errs, fmt.Errorf("downloads.exists_policy %q: want skip|overwrite|rename", c.Downloads.ExistsPolicy))
	}
	switch c.Rotation.Strategy {
	case StrategyRoundRobin, StrategyLeastUsed, StrategyRandom, StrategyFixed:
	default:
		errs = append(errs, fmt.Errorf("rotation.strategy %q: want round-robin|least-used|random|fixed", c.Rotation.Strategy))
	}
	if c.Egress.Router.Enabled {
		switch c.Egress.Router.Method {
		case "upnp":
		case "script":
			if len(c.Egress.Router.Script) == 0 {
				errs = append(errs, errors.New("egress.router.script is empty"))
			}
		default:
			errs = append(errs, fmt.Errorf("egress.router.method %q: want upnp|script", c.Egress.Router.Method))
		}
	}
	for i, v := range c.Egress.SystemVPN {
		if v.Name == "" || len(v.Rotate) == 0 && len(v.Connect) == 0 {
			errs = append(errs, fmt.Errorf("egress.system_vpn[%d]: name and connect or rotate are required", i))
		}
	}
	switch c.Clipboard.Action {
	case ClipboardAsk, ClipboardAdd, ClipboardLinkGrabber:
	default:
		errs = append(errs, fmt.Errorf("clipboard.action %q: want ask|add|linkgrabber", c.Clipboard.Action))
	}
	switch c.Secrets.Backend {
	case "", SecretsAuto, SecretsKeyring, SecretsVault, SecretsMemory:
	default:
		errs = append(errs, fmt.Errorf("secrets.backend %q: want auto|keyring|vault|memory", c.Secrets.Backend))
	}
	switch c.Desktop.Mode {
	case DesktopIntegrated:
	case DesktopRemote:
		if c.Desktop.RemoteURL == "" {
			errs = append(errs, errors.New("desktop.remote_url is required in remote mode"))
		}
	default:
		errs = append(errs, fmt.Errorf("desktop.mode %q: want integrated|remote", c.Desktop.Mode))
	}
	return errors.Join(errs...)
}

// IsLoopbackHost reports whether host (no port) is a loopback name or IP.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
