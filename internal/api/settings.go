package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/store"
)

// Settings is the safe view of the configuration exposed by GET/PUT
// /api/v1/settings. It never contains secrets (API token, password hash,
// Tor control password, remote token, hoster options). Server and Network
// are read-only: commands (router script, system VPN) can only be changed
// in config.toml.
type Settings struct {
	DownloadDir string              `json:"download_dir"`
	Downloads   DownloadSettings    `json:"downloads"`
	Rotation    RotationSettings    `json:"rotation"`
	Clipboard   ClipboardSettings   `json:"clipboard"`
	PostProcess PostProcessSettings `json:"postprocess"`
	Desktop     DesktopSettings     `json:"desktop"`
	Appearance  Appearance          `json:"appearance"`

	// Read-only.
	Server  ServerInfo  `json:"server"`
	Network NetworkInfo `json:"network"`
	// RestartRequired is true after a change that only applies on restart.
	RestartRequired bool `json:"restart_required"`
}

// DownloadSettings mirrors config.DownloadsConfig (editable part).
type DownloadSettings struct {
	MaxActive             int    `json:"max_active"`
	MaxPerHoster          int    `json:"max_per_hoster"`
	MaxPerEgress          int    `json:"max_per_egress"`
	ConnsPerFile          int    `json:"conns_per_file"`
	SpeedLimit            int64  `json:"speed_limit"`
	PerDownloadSpeedLimit int64  `json:"per_download_speed_limit"`
	MaxRetries            int    `json:"max_retries"`
	ResumeOnStart         bool   `json:"resume_on_start"`
	ExistsPolicy          string `json:"exists_policy"`
}

// RotationSettings mirrors config.RotationConfig. Durations are Go
// duration strings ("5m", "1h").
type RotationSettings struct {
	Enabled           bool   `json:"enabled"`
	Strategy          string `json:"strategy"`
	GlobalRotation    bool   `json:"global_rotation"`
	MinGlobalInterval string `json:"min_global_interval"`
	VerifyIPChange    bool   `json:"verify_ip_change"`
	DefaultRetryAfter string `json:"default_retry_after"`
}

// ClipboardSettings mirrors config.ClipboardConfig.
type ClipboardSettings struct {
	Enabled     bool     `json:"enabled"`
	Action      string   `json:"action"`
	Hosters     []string `json:"hosters"`
	IgnoreKnown bool     `json:"ignore_known"`
	UseWlPaste  bool     `json:"use_wl_paste"`
}

// PostProcessSettings mirrors config.PostProcessConfig.
type PostProcessSettings struct {
	VerifyChecksums bool `json:"verify_checksums"`
	Extract         bool `json:"extract"`
	// Passwords is the archive password list used for extraction.
	Passwords      []string `json:"passwords"`
	DeleteArchives bool     `json:"delete_archives"`
}

// DesktopSettings mirrors the editable part of config.DesktopConfig.
type DesktopSettings struct {
	CloseToTray   bool `json:"close_to_tray"`
	Autostart     bool `json:"autostart"`
	Notifications bool `json:"notifications"`
	StartHidden   bool `json:"start_hidden"`
}

// Appearance is the UI preference (PLAN.md section 7.1), stored in the
// settings table, not in config.toml.
type Appearance struct {
	Estilo string `json:"estilo"` // grafito | terminal | medianoche | monocromo | enfoque
	Tema   string `json:"tema"`   // sistema | oscuro | claro
	Acento string `json:"acento"` // "" = theme default, else #rrggbb
}

// ServerInfo is read-only.
type ServerInfo struct {
	Listen         string   `json:"listen"`
	PasswordSet    bool     `json:"password_set"`
	TLS            bool     `json:"tls"`
	AllowedOrigins []string `json:"allowed_origins"`
	AllowedHosts   []string `json:"allowed_hosts"`
}

// NetworkInfo summarizes the egress configuration; read-only.
type NetworkInfo struct {
	Direct         bool            `json:"direct"`
	TorEnabled     bool            `json:"tor_enabled"`
	Proxies        int             `json:"proxies"` // inline entries + files + urls
	WireGuard      []string        `json:"wireguard"`
	Router         RouterInfo      `json:"router"`
	SystemVPN      []SystemVPNInfo `json:"system_vpn"`
	HealthInterval string          `json:"health_interval"`
}

// RouterInfo shows the router reconnect configuration (read-only).
type RouterInfo struct {
	Enabled bool     `json:"enabled"`
	Method  string   `json:"method"`
	Script  []string `json:"script"`
}

// SystemVPNInfo shows one system VPN command set (read-only).
type SystemVPNInfo struct {
	Name       string   `json:"name"`
	Connect    []string `json:"connect"`
	Rotate     []string `json:"rotate"`
	Disconnect []string `json:"disconnect"`
}

// Appearance values.
var (
	Estilos = []string{"grafito", "terminal", "medianoche", "monocromo", "enfoque"}
	Temas   = []string{"sistema", "oscuro", "claro"}
	hexRe   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// DefaultAppearance is used until the user picks one.
var DefaultAppearance = Appearance{Estilo: "grafito", Tema: "sistema"}

// AppearanceKey is the settings-table key holding the Appearance JSON.
const AppearanceKey = "ui.appearance"

// PrefStore persists UI preferences (*store.Store satisfies it).
type PrefStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// ConfigSettings is the standard SettingsService: it edits a private copy
// of the configuration, saves config.toml (re-reading it first so manual
// edits are kept) and stores the appearance in a PrefStore.
type ConfigSettings struct {
	// Path of config.toml; "" keeps changes in memory only.
	Path string
	// Prefs stores the appearance; nil keeps it in memory.
	Prefs PrefStore
	// OnChange, if set, is called with the new configuration after a
	// successful Put (to apply limits live). It must not modify cfg.
	OnChange func(cfg *config.Config)

	mu         sync.Mutex
	cfg        config.Config
	appearance *Appearance
	changed    bool
}

var (
	_ SettingsService = (*ConfigSettings)(nil)
	_ PrefStore       = (*store.Store)(nil)
)

// NewConfigSettings returns a ConfigSettings starting from cfg (copied).
func NewConfigSettings(path string, cfg *config.Config, prefs PrefStore) *ConfigSettings {
	return &ConfigSettings{Path: path, Prefs: prefs, cfg: *cfg}
}

// Get implements SettingsService.
func (c *ConfigSettings) Get(ctx context.Context) (Settings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ap, err := c.loadAppearance(ctx)
	if err != nil {
		return Settings{}, err
	}
	return viewOf(&c.cfg, ap, c.changed), nil
}

func (c *ConfigSettings) loadAppearance(ctx context.Context) (Appearance, error) {
	if c.appearance != nil {
		return *c.appearance, nil
	}
	ap := DefaultAppearance
	if c.Prefs != nil {
		v, err := c.Prefs.GetSetting(ctx, AppearanceKey)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return Appearance{}, err
		default:
			var stored Appearance
			if json.Unmarshal([]byte(v), &stored) == nil && validAppearance(stored) == nil {
				ap = stored
			}
		}
	}
	c.appearance = &ap
	return ap, nil
}

func validAppearance(a Appearance) error {
	if !slices.Contains(Estilos, a.Estilo) {
		return badRequest("appearance.estilo %q no válido", a.Estilo)
	}
	if !slices.Contains(Temas, a.Tema) {
		return badRequest("appearance.tema %q no válido", a.Tema)
	}
	if a.Acento != "" && !hexRe.MatchString(a.Acento) {
		return badRequest("appearance.acento %q no válido (#rrggbb)", a.Acento)
	}
	return nil
}

// Put implements SettingsService.
func (c *ConfigSettings) Put(ctx context.Context, s Settings) (Settings, error) {
	if err := validAppearance(s.Appearance); err != nil {
		return Settings{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	base := c.cfg
	if c.Path != "" {
		disk, err := config.Load(c.Path)
		if err != nil {
			return Settings{}, fmt.Errorf("settings: reload config: %w", err)
		}
		base = *disk
	}
	next := base
	if err := applySettings(&next, s); err != nil {
		return Settings{}, err
	}
	if err := next.Validate(); err != nil {
		return Settings{}, badRequest("%v", err)
	}
	cfgChanged := !sameEditable(&base, &next) || !sameEditable(&c.cfg, &next)
	if cfgChanged && c.Path != "" {
		if err := config.Save(c.Path, &next); err != nil {
			return Settings{}, err
		}
	}
	ap := s.Appearance
	if c.Prefs != nil {
		b, _ := json.Marshal(ap)
		if err := c.Prefs.SetSetting(ctx, AppearanceKey, string(b)); err != nil {
			return Settings{}, err
		}
	}
	c.appearance = &ap
	c.cfg = next
	if cfgChanged {
		if c.OnChange == nil {
			c.changed = true // applied on the next start
		} else {
			cp := next
			c.OnChange(&cp)
		}
	}
	return viewOf(&c.cfg, ap, c.changed), nil
}

// sameEditable compares the fields Settings can change.
func sameEditable(a, b *config.Config) bool {
	va, vb := viewOf(a, Appearance{}, false), viewOf(b, Appearance{}, false)
	ja, _ := json.Marshal(va)
	jb, _ := json.Marshal(vb)
	return string(ja) == string(jb)
}

func parseDur(field, v string) (config.Duration, error) {
	if v == "" {
		return config.Duration{}, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return config.Duration{}, badRequest("%s %q no es una duración válida (p. ej. 5m, 1h)", field, v)
	}
	return config.D(d), nil
}

func applySettings(c *config.Config, s Settings) error {
	if s.DownloadDir == "" {
		return badRequest("download_dir no puede estar vacío")
	}
	c.DownloadDir = s.DownloadDir

	d := s.Downloads
	if d.MaxPerHoster < 0 || d.MaxPerEgress < 0 || d.MaxRetries < 0 {
		return badRequest("los límites de descargas no pueden ser negativos")
	}
	c.Downloads.MaxActive = d.MaxActive
	c.Downloads.MaxPerHoster = d.MaxPerHoster
	c.Downloads.MaxPerEgress = d.MaxPerEgress
	c.Downloads.ConnsPerFile = d.ConnsPerFile
	c.Downloads.SpeedLimit = d.SpeedLimit
	c.Downloads.PerDownloadSpeedLimit = d.PerDownloadSpeedLimit
	c.Downloads.MaxRetries = d.MaxRetries
	c.Downloads.ResumeOnStart = d.ResumeOnStart
	c.Downloads.ExistsPolicy = config.ExistsPolicy(d.ExistsPolicy)

	rt := s.Rotation
	minGlobal, err := parseDur("rotation.min_global_interval", rt.MinGlobalInterval)
	if err != nil {
		return err
	}
	retryAfter, err := parseDur("rotation.default_retry_after", rt.DefaultRetryAfter)
	if err != nil {
		return err
	}
	c.Rotation.Enabled = rt.Enabled
	c.Rotation.Strategy = rt.Strategy
	c.Rotation.GlobalRotation = rt.GlobalRotation
	c.Rotation.MinGlobalInterval = minGlobal
	c.Rotation.VerifyIPChange = rt.VerifyIPChange
	c.Rotation.DefaultRetryAfter = retryAfter

	c.Clipboard.Enabled = s.Clipboard.Enabled
	c.Clipboard.Action = s.Clipboard.Action
	c.Clipboard.Hosters = slices.Clone(s.Clipboard.Hosters)
	c.Clipboard.IgnoreKnown = s.Clipboard.IgnoreKnown
	c.Clipboard.UseWlPaste = s.Clipboard.UseWlPaste

	c.PostProcess.VerifyChecksums = s.PostProcess.VerifyChecksums
	c.PostProcess.Extract = s.PostProcess.Extract
	c.PostProcess.Passwords = slices.Clone(s.PostProcess.Passwords)
	c.PostProcess.DeleteArchives = s.PostProcess.DeleteArchives

	c.Desktop.CloseToTray = s.Desktop.CloseToTray
	c.Desktop.Autostart = s.Desktop.Autostart
	c.Desktop.Notifications = s.Desktop.Notifications
	c.Desktop.StartHidden = s.Desktop.StartHidden
	return nil
}

func durString(d config.Duration) string {
	if d.Duration == 0 {
		return ""
	}
	return d.String()
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return slices.Clone(v)
}

func viewOf(c *config.Config, ap Appearance, changed bool) Settings {
	s := Settings{
		DownloadDir: c.DownloadDir,
		Downloads: DownloadSettings{
			MaxActive:             c.Downloads.MaxActive,
			MaxPerHoster:          c.Downloads.MaxPerHoster,
			MaxPerEgress:          c.Downloads.MaxPerEgress,
			ConnsPerFile:          c.Downloads.ConnsPerFile,
			SpeedLimit:            c.Downloads.SpeedLimit,
			PerDownloadSpeedLimit: c.Downloads.PerDownloadSpeedLimit,
			MaxRetries:            c.Downloads.MaxRetries,
			ResumeOnStart:         c.Downloads.ResumeOnStart,
			ExistsPolicy:          string(c.Downloads.ExistsPolicy),
		},
		Rotation: RotationSettings{
			Enabled:           c.Rotation.Enabled,
			Strategy:          c.Rotation.Strategy,
			GlobalRotation:    c.Rotation.GlobalRotation,
			MinGlobalInterval: durString(c.Rotation.MinGlobalInterval),
			VerifyIPChange:    c.Rotation.VerifyIPChange,
			DefaultRetryAfter: durString(c.Rotation.DefaultRetryAfter),
		},
		Clipboard: ClipboardSettings{
			Enabled:     c.Clipboard.Enabled,
			Action:      c.Clipboard.Action,
			Hosters:     orEmpty(c.Clipboard.Hosters),
			IgnoreKnown: c.Clipboard.IgnoreKnown,
			UseWlPaste:  c.Clipboard.UseWlPaste,
		},
		PostProcess: PostProcessSettings{
			VerifyChecksums: c.PostProcess.VerifyChecksums,
			Extract:         c.PostProcess.Extract,
			Passwords:       orEmpty(c.PostProcess.Passwords),
			DeleteArchives:  c.PostProcess.DeleteArchives,
		},
		Desktop: DesktopSettings{
			CloseToTray:   c.Desktop.CloseToTray,
			Autostart:     c.Desktop.Autostart,
			Notifications: c.Desktop.Notifications,
			StartHidden:   c.Desktop.StartHidden,
		},
		Appearance: ap,
		Server: ServerInfo{
			Listen:         c.Server.Listen,
			PasswordSet:    c.Server.PasswordHash != "",
			TLS:            c.Server.TLSCert != "",
			AllowedOrigins: orEmpty(c.Server.AllowedOrigins),
			AllowedHosts:   orEmpty(c.Server.AllowedHosts),
		},
		Network: NetworkInfo{
			Direct:         c.Egress.Direct,
			TorEnabled:     c.Egress.Tor.Enabled,
			Proxies:        len(c.Egress.Proxies.List) + len(c.Egress.Proxies.Files) + len(c.Egress.Proxies.URLs),
			WireGuard:      []string{},
			Router:         RouterInfo{Enabled: c.Egress.Router.Enabled, Method: c.Egress.Router.Method, Script: orEmpty(c.Egress.Router.Script)},
			SystemVPN:      []SystemVPNInfo{},
			HealthInterval: durString(c.Egress.HealthInterval),
		},
		RestartRequired: changed,
	}
	for _, g := range c.Egress.WireGuard {
		s.Network.WireGuard = append(s.Network.WireGuard, g.Name)
	}
	for _, v := range c.Egress.SystemVPN {
		s.Network.SystemVPN = append(s.Network.SystemVPN, SystemVPNInfo{
			Name: v.Name, Connect: orEmpty(v.Connect), Rotate: orEmpty(v.Rotate), Disconnect: orEmpty(v.Disconnect),
		})
	}
	return s
}
