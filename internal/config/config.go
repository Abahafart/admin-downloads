// Package config loads and saves config.toml, located by default at
// os.UserConfigDir()/admindescargas/config.toml.
//
// Zero values in per-hoster sections mean "use the plugin default". Every
// section has a defaults entry in Default(); Load decodes the file on top of
// those defaults, so a partial file is fine.
//
// Security: commands (router script, system VPN argv) are only read from this
// file. The HTTP API must never write them (see PLAN.md section 6), which is
// why Save is only called by the CLI/daemon itself (first run, token
// generation) and by settings endpoints that whitelist editable fields.
package config

import (
	"time"
)

// AppName is the directory name used under the user config/data dirs.
const AppName = "admindescargas"

// Config is the full content of config.toml.
type Config struct {
	// DataDir holds the SQLite database, the lock file and the vault.
	DataDir string `toml:"data_dir" json:"data_dir"`
	// DownloadDir is the default destination for new packages.
	DownloadDir string `toml:"download_dir" json:"download_dir"`

	Server      ServerConfig            `toml:"server" json:"server"`
	Log         LogConfig               `toml:"log" json:"log"`
	Downloads   DownloadsConfig         `toml:"downloads" json:"downloads"`
	Rotation    RotationConfig          `toml:"rotation" json:"rotation"`
	Egress      EgressConfig            `toml:"egress" json:"egress"`
	Hosters     map[string]HosterConfig `toml:"hosters" json:"hosters"`
	Clipboard   ClipboardConfig         `toml:"clipboard" json:"clipboard"`
	PostProcess PostProcessConfig       `toml:"postprocess" json:"postprocess"`
	Desktop     DesktopConfig           `toml:"desktop" json:"desktop"`
	Secrets     SecretsConfig           `toml:"secrets" json:"secrets"`
}

// Secret store backends ([secrets] backend).
const (
	SecretsAuto    = "auto"    // OS keyring if it works, else the encrypted vault
	SecretsKeyring = "keyring" // OS keyring only
	SecretsVault   = "vault"   // encrypted file in data_dir (headless servers)
	SecretsMemory  = "memory"  // in memory, lost on exit (tests, demos)
)

// SecretsConfig selects where account credentials are kept.
type SecretsConfig struct {
	// Backend is auto | keyring | vault | memory.
	Backend string `toml:"backend" json:"backend"`
	// VaultPath overrides <data_dir>/secrets.vault.
	VaultPath string `toml:"vault_path" json:"vault_path"`
	// VaultPasswordFile, if set, is read at start-up to unlock the vault
	// without user interaction (services). Otherwise the vault starts
	// locked and is unlocked from the UI / `adesc secrets unlock`.
	VaultPasswordFile string `toml:"vault_password_file" json:"vault_password_file"`
}

// ServerConfig configures the REST/SSE API and the embedded Web UI.
type ServerConfig struct {
	// Listen is the TCP address, 127.0.0.1 by default. Listening on a
	// non-loopback address requires PasswordHash (enforced by Validate).
	Listen string `toml:"listen" json:"listen"`
	// Token is the bearer token used by the CLI and the browser extension.
	// Generated on first run.
	Token string `toml:"token" json:"-"`
	// PasswordHash is the Argon2id hash (PHC string) of the Web UI password.
	// Empty = no Web UI login (only allowed on loopback).
	PasswordHash string `toml:"password_hash" json:"-"`
	// AllowedOrigins are extra Origin values accepted besides the server's
	// own origin (e.g. the Wails webview origin, the extension origin).
	AllowedOrigins []string `toml:"allowed_origins" json:"allowed_origins"`
	// AllowedHosts are extra Host header values accepted (DNS rebinding
	// protection); loopback names and the listen host are always allowed.
	AllowedHosts []string `toml:"allowed_hosts" json:"allowed_hosts"`
	TLSCert      string   `toml:"tls_cert" json:"tls_cert"`
	TLSKey       string   `toml:"tls_key" json:"tls_key"`
}

// LogConfig configures log/slog.
type LogConfig struct {
	Level  string `toml:"level" json:"level"`   // debug | info | warn | error
	Format string `toml:"format" json:"format"` // text | json
	File   string `toml:"file" json:"file"`     // empty = stderr
}

// ExistsPolicy decides what to do when the destination file already exists.
type ExistsPolicy string

// Exists policies.
const (
	ExistsSkip      ExistsPolicy = "skip"      // same size (and hash if known): skip
	ExistsOverwrite ExistsPolicy = "overwrite" // replace
	ExistsRename    ExistsPolicy = "rename"    // save as "name (1).ext"
)

// DownloadsConfig holds scheduler and downloader limits.
type DownloadsConfig struct {
	MaxActive    int `toml:"max_active" json:"max_active"`         // global simultaneous downloads
	MaxPerHoster int `toml:"max_per_hoster" json:"max_per_hoster"` // default; hosters.<name>.max_parallel overrides
	MaxPerEgress int `toml:"max_per_egress" json:"max_per_egress"` // simultaneous downloads per egress
	// ConnsPerFile is the default number of segments/connections per file,
	// capped by the hoster's Caps.MaxConnsPerFile.
	ConnsPerFile int `toml:"conns_per_file" json:"conns_per_file"`
	// MinSegmentSize avoids splitting below this many bytes.
	MinSegmentSize int64 `toml:"min_segment_size" json:"min_segment_size"`
	// SpeedLimit is the global limit in bytes/second (0 = unlimited).
	SpeedLimit int64 `toml:"speed_limit" json:"speed_limit"`
	// PerDownloadSpeedLimit is the per-download limit in bytes/s (0 = none).
	PerDownloadSpeedLimit int64 `toml:"per_download_speed_limit" json:"per_download_speed_limit"`
	// MaxRetries is the number of automatic retries before "failed".
	MaxRetries int `toml:"max_retries" json:"max_retries"`
	// RetryBackoff is the initial backoff (exponential with jitter).
	RetryBackoff Duration `toml:"retry_backoff" json:"retry_backoff"`
	// MaxRetryBackoff caps the backoff.
	MaxRetryBackoff Duration `toml:"max_retry_backoff" json:"max_retry_backoff"`
	// ResumeOnStart requeues downloads that were active when the process
	// stopped.
	ResumeOnStart bool `toml:"resume_on_start" json:"resume_on_start"`
	// ExistsPolicy applies when the destination file already exists.
	ExistsPolicy ExistsPolicy `toml:"exists_policy" json:"exists_policy"`
	// FlushInterval is how often segment offsets are persisted (after fsync).
	FlushInterval Duration `toml:"flush_interval" json:"flush_interval"`
	// ResolveRequestsPerSecond limits folder listing requests per hoster.
	ResolveRequestsPerSecond float64 `toml:"resolve_requests_per_second" json:"resolve_requests_per_second"`
}

// Rotation strategies.
const (
	StrategyRoundRobin = "round-robin"
	StrategyLeastUsed  = "least-used"
	StrategyRandom     = "random"
	StrategyFixed      = "fixed"
)

// RotationConfig configures the RotationManager.
type RotationConfig struct {
	// Enabled turns IP rotation on. When false, only the direct egress (or
	// the first configured one) is used and quotas lead to "waiting".
	Enabled bool `toml:"enabled" json:"enabled"`
	// Strategy is one of round-robin | least-used | random | fixed.
	Strategy string `toml:"strategy" json:"strategy"`
	// GlobalRotation allows using router/system-VPN egresses (pausing all
	// downloads while the machine IP changes).
	GlobalRotation bool `toml:"global_rotation" json:"global_rotation"`
	// MinGlobalInterval is the minimum time between two global rotations.
	MinGlobalInterval Duration `toml:"min_global_interval" json:"min_global_interval"`
	// VerifyIPChange checks the public IP changed after a global rotation.
	VerifyIPChange bool `toml:"verify_ip_change" json:"verify_ip_change"`
	// IPCheckServices overrides netx.DefaultIPEchoServices when non-empty.
	IPCheckServices []string `toml:"ip_check_services" json:"ip_check_services"`
	// DefaultRetryAfter is used when a hoster reports a quota without a
	// duration and has no per-hoster retry_after.
	DefaultRetryAfter Duration `toml:"default_retry_after" json:"default_retry_after"`
}

// EgressConfig defines the available network exits.
type EgressConfig struct {
	// Direct includes the machine's own connection in the pool.
	Direct bool `toml:"direct" json:"direct"`
	// HealthInterval is how often egresses are health-checked.
	HealthInterval Duration           `toml:"health_interval" json:"health_interval"`
	Proxies        ProxiesConfig      `toml:"proxies" json:"proxies"`
	Tor            TorConfig          `toml:"tor" json:"tor"`
	WireGuard      []WireGuardGroup   `toml:"wireguard" json:"wireguard"`
	Router         RouterConfig       `toml:"router" json:"router"`
	SystemVPN      []SystemVPNCommand `toml:"system_vpn" json:"system_vpn"`
}

// ProxiesConfig lists HTTP/HTTPS/SOCKS5 proxies. Each entry is
// "scheme://user:pass@host:port" (schemes: http, https, socks5, socks5h).
type ProxiesConfig struct {
	List  []string `toml:"list" json:"list"`   // inline entries
	Files []string `toml:"files" json:"files"` // local files, one entry per line
	URLs  []string `toml:"urls" json:"urls"`   // remote lists fetched at start-up
	// DedupeByIP disables proxies that exit through an IP already in use.
	DedupeByIP bool `toml:"dedupe_by_ip" json:"dedupe_by_ip"`
}

// TorConfig configures the Tor egress.
type TorConfig struct {
	Enabled     bool   `toml:"enabled" json:"enabled"`
	SocksAddr   string `toml:"socks_addr" json:"socks_addr"`     // default 127.0.0.1:9050
	ControlAddr string `toml:"control_addr" json:"control_addr"` // default 127.0.0.1:9051
	// ControlPassword for HashedControlPassword auth; empty = cookie/none.
	ControlPassword string `toml:"control_password" json:"-"`
	// TorPath, if set, makes the app launch its own tor binary (bine).
	TorPath string `toml:"tor_path" json:"tor_path"`
	// Circuits is the number of isolated identities (IsolateSOCKSAuth)
	// exposed as separate egresses.
	Circuits int `toml:"circuits" json:"circuits"`
}

// WireGuardGroup is a set of wg-quick .conf files from one provider.
type WireGuardGroup struct {
	Name string `toml:"name" json:"name"`
	// Confs are paths or glob patterns to wg-quick .conf files.
	Confs []string `toml:"confs" json:"confs"`
	// MaxSimultaneous is the provider's limit of simultaneous tunnels
	// (e.g. 1 for Proton free). 0 = unlimited.
	MaxSimultaneous int `toml:"max_simultaneous" json:"max_simultaneous"`
}

// RouterConfig configures the router reconnect global egress.
type RouterConfig struct {
	Enabled bool `toml:"enabled" json:"enabled"`
	// Method is "upnp" (IGD ForceTermination + RequestConnection) or
	// "script".
	Method string `toml:"method" json:"method"`
	// Script is the argv executed for Method "script" (no shell).
	Script []string `toml:"script" json:"script"`
	// WaitAfter is how long to wait for the link to come back.
	WaitAfter Duration `toml:"wait_after" json:"wait_after"`
	Timeout   Duration `toml:"timeout" json:"timeout"`
}

// SystemVPNCommand drives a system VPN with argv commands (exec, no shell).
type SystemVPNCommand struct {
	Name       string   `toml:"name" json:"name"`
	Connect    []string `toml:"connect" json:"connect"`       // e.g. ["mullvad", "connect"]
	Rotate     []string `toml:"rotate" json:"rotate"`         // e.g. ["mullvad", "reconnect"]
	Disconnect []string `toml:"disconnect" json:"disconnect"` // e.g. ["mullvad", "disconnect"]
	Timeout    Duration `toml:"timeout" json:"timeout"`
	// WaitAfter is the settle time after a command before checking the IP.
	WaitAfter Duration `toml:"wait_after" json:"wait_after"`
}

// HosterConfig holds per-hoster settings ([hosters.<name>]). Zero values
// mean "plugin default".
type HosterConfig struct {
	Disabled bool `toml:"disabled" json:"disabled"`
	// Domains overrides/extends the hostnames the plugin matches (TeraBox).
	Domains []string `toml:"domains" json:"domains"`
	// RetryAfter is the default wait for a quota without explicit duration.
	RetryAfter      Duration `toml:"retry_after" json:"retry_after"`
	MaxConnsPerFile int      `toml:"max_conns_per_file" json:"max_conns_per_file"`
	MaxParallel     int      `toml:"max_parallel" json:"max_parallel"`
	// RequestsPerSecond limits API/page requests (folder listing etc.).
	RequestsPerSecond float64 `toml:"requests_per_second" json:"requests_per_second"`
	// Options are plugin specific (documented by each plugin), e.g. gdrive
	// "copy_on_quota" = "true", "delete_copy" = "true", "api_key" = "...".
	Options map[string]string `toml:"options" json:"-"`
}

// Clipboard actions.
const (
	ClipboardAsk         = "ask"         // notification with Add / Ignore
	ClipboardAdd         = "add"         // add straight to the queue
	ClipboardLinkGrabber = "linkgrabber" // keep in the LinkGrabber silently
)

// ClipboardConfig configures the desktop clipboard monitor.
type ClipboardConfig struct {
	Enabled bool   `toml:"enabled" json:"enabled"`
	Action  string `toml:"action" json:"action"` // ask | add | linkgrabber
	// Hosters restricts detection to these hoster names (empty = all).
	Hosters []string `toml:"hosters" json:"hosters"`
	// IgnoreKnown skips links already queued or in history.
	IgnoreKnown bool `toml:"ignore_known" json:"ignore_known"`
	// UseWlPaste enables `wl-paste --watch` on Wayland without XWayland.
	UseWlPaste bool `toml:"use_wl_paste" json:"use_wl_paste"`
}

// PostProcessConfig configures verification and extraction.
type PostProcessConfig struct {
	VerifyChecksums bool     `toml:"verify_checksums" json:"verify_checksums"`
	Extract         bool     `toml:"extract" json:"extract"`
	Passwords       []string `toml:"passwords" json:"passwords"`
	DeleteArchives  bool     `toml:"delete_archives" json:"delete_archives"`
}

// Desktop modes.
const (
	DesktopIntegrated = "integrated"
	DesktopRemote     = "remote"
)

// DesktopConfig configures adesc-desktop.
type DesktopConfig struct {
	Mode          string `toml:"mode" json:"mode"`             // integrated | remote
	RemoteURL     string `toml:"remote_url" json:"remote_url"` // for remote mode
	RemoteToken   string `toml:"remote_token" json:"-"`
	CloseToTray   bool   `toml:"close_to_tray" json:"close_to_tray"`
	Autostart     bool   `toml:"autostart" json:"autostart"`
	Notifications bool   `toml:"notifications" json:"notifications"`
	StartHidden   bool   `toml:"start_hidden" json:"start_hidden"`
}

// Hoster returns the settings for name (zero value if not configured).
func (c *Config) Hoster(name string) HosterConfig {
	if c.Hosters == nil {
		return HosterConfig{}
	}
	return c.Hosters[name]
}

// Duration is a time.Duration that (un)marshals as a Go duration string
// ("90s", "5m", "1h30m") in TOML.
type Duration struct{ time.Duration }

// D is a shorthand constructor.
func D(d time.Duration) Duration { return Duration{d} }

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}
