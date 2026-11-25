package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/app"
	"admindescargas/internal/config"
)

// Mode is how the desktop app reaches a core.
type Mode string

// Modes. ModeIntegrated and ModeRemote are persisted in config.Desktop.Mode;
// ModeLocalDaemon is chosen at start-up when a daemon already owns the data
// directory.
const (
	ModeIntegrated  Mode = config.DesktopIntegrated
	ModeRemote      Mode = config.DesktopRemote
	ModeLocalDaemon Mode = "local-daemon"
)

// Connection is what the web UI needs to talk to the API: the body of
// GET /__adesc/connection.
type Connection struct {
	BaseURL string `json:"baseURL"`
	Token   string `json:"token"`
	Mode    Mode   `json:"mode"`
}

// Webview origins of Wails (sent as Origin by the embedded UI): Linux and
// macOS serve the assets on wails://wails or wails://localhost, Windows (WebView2) on
// http://wails.localhost. `wails dev` uses http://localhost:34115.
const (
	OriginUnix      = "wails://wails"
	OriginUnixLocal = "wails://localhost"
	OriginWindows   = "http://wails.localhost"
	OriginDev       = "http://localhost:34115"
)

// WebviewOrigins are the Origin values the API must accept for the desktop
// UI (all platforms; they cannot be produced by a normal web page).
func WebviewOrigins() []string { return []string{OriginUnix, OriginUnixLocal, OriginWindows} }

// ErrDaemonRunning wraps app.ErrAlreadyRunning when another process owns
// the data directory.
var ErrDaemonRunning = app.ErrAlreadyRunning

// StartIntegrated starts the core in this process (config.Desktop.Mode
// "integrated"). The API listens on cfg.Server.Listen so the CLI and the
// browser extension keep working; if that port is taken by something else
// a random loopback port is used (instance.json records it). It fails with
// an error matching app.ErrAlreadyRunning when a daemon owns the data dir.
//
// extraOrigins (the webview origins) are accepted by the API through
// app.WithExtraOrigins, never written to the configuration. opts are
// passed to app.New (e.g. app.WithConfigPath so the settings endpoint saves
// config.toml).
func StartIntegrated(ctx context.Context, cfg *config.Config, log *slog.Logger, extraOrigins []string, opts ...app.Option) (*app.App, Connection, error) {
	c := *cfg

	ln, err := listen(ctx, c.Server.Listen)
	if err != nil {
		return nil, Connection{}, err
	}
	opts = append([]app.Option{app.WithListener(ln), app.WithExtraOrigins(extraOrigins...)}, opts...)
	if log != nil {
		opts = append(opts, app.WithLogger(log))
	}
	a, err := app.New(&c, opts...)
	if err != nil {
		_ = ln.Close()
		return nil, Connection{}, err
	}
	if err := a.Start(ctx); err != nil {
		_ = ln.Close() // the app only owns the listener once it serves
		return nil, Connection{}, err
	}
	return a, Connection{BaseURL: clientURL(a.Addr(), c.Server.TLSCert != ""), Token: c.Server.Token, Mode: ModeIntegrated}, nil
}

// listen binds addr; when the port is busy and addr is loopback it falls
// back to a random loopback port.
func listen(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err == nil {
		return ln, nil
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil || !config.IsLoopbackHost(host) {
		return nil, fmt.Errorf("desktop: listen %s: %w", addr, err)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ln, err2 := lc.Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
	if err2 != nil {
		return nil, fmt.Errorf("desktop: listen %s: %w", addr, errors.Join(err, err2))
	}
	return ln, nil
}

// clientURL turns a listen address into a URL usable from this machine
// (wildcard addresses are reached through loopback).
func clientURL(addr string, tls bool) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// DetectLocalDaemon looks for a live core owning cfg.DataDir (instance.json
// plus a health check) and returns a client connection to it. The token is
// the one in config.toml, shared with the daemon.
func DetectLocalDaemon(ctx context.Context, cfg *config.Config) (Connection, error) {
	in, err := app.ReadInstance(cfg.DataDir)
	if err != nil {
		return Connection{}, fmt.Errorf("desktop: no running daemon found: %w", err)
	}
	conn := Connection{BaseURL: clientURL(in.Addr, in.TLS), Token: cfg.Server.Token, Mode: ModeLocalDaemon}
	if conn.BaseURL == "" {
		return Connection{}, fmt.Errorf("desktop: instance file: bad address %q", in.Addr)
	}
	if err := CheckServer(ctx, conn.BaseURL, conn.Token); err != nil {
		return Connection{}, err
	}
	return conn, nil
}

// ErrBadToken is returned by CheckServer when the server rejects the token.
var ErrBadToken = errors.New("desktop: the server rejected the token")

// CheckServer verifies that baseURL answers /api/v1/health and accepts
// token (an authenticated GET /api/v1/stats must not be 401/403).
func CheckServer(ctx context.Context, baseURL, token string) error {
	c, err := apiclient.New(baseURL, token)
	if err != nil {
		return err
	}
	c.HTTP = &http.Client{Timeout: 10 * time.Second}
	if err := c.Health(ctx); err != nil {
		return fmt.Errorf("desktop: %s no responde: %w", baseURL, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = c.Do(ctx, http.MethodGet, "/stats", nil, nil, nil)
	if apiclient.IsStatus(err, http.StatusUnauthorized) || apiclient.IsStatus(err, http.StatusForbidden) {
		return ErrBadToken
	}
	return nil // other errors (501 without engine...) still prove the auth
}

// NormalizeRemoteURL validates a user-typed daemon URL ("nas:7979",
// "http://nas:7979/") and returns it as scheme://host[:port].
func NormalizeRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("la URL está vacía")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	c, err := apiclient.New(raw, "")
	if err != nil {
		return "", err
	}
	u := *c.BaseURL
	u.Path, u.RawQuery, u.Fragment = strings.TrimSuffix(u.Path, "/"), "", ""
	return u.String(), nil
}
