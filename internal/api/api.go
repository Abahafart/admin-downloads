package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/buildinfo"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
)

// AccountService manages hoster accounts (implemented by an adapter over
// the accounts manager). Errors should wrap core.ErrNotFound /
// core.ErrInvalidArgument / core.ErrInvalidState (or accounts.ErrLocked) so
// they map to HTTP codes. Returned accounts never carry secrets.
type AccountService interface {
	List(ctx context.Context) ([]accounts.Account, error)
	Create(ctx context.Context, req AccountCreateRequest) (accounts.Account, error)
	Update(ctx context.Context, id string, p AccountPatch) (accounts.Account, error)
	Delete(ctx context.Context, id string) error
	// Check logs in and refreshes status/quota.
	Check(ctx context.Context, id string) (accounts.Account, error)
	// Submit2FA completes a login waiting for a 2FA code (MEGA).
	Submit2FA(ctx context.Context, id, code string) (accounts.Account, error)
	// ImportCookies creates (or updates the account with the same hoster and
	// label) a cookie account from a Netscape cookies.txt or JSON export.
	ImportCookies(ctx context.Context, hoster, label, cookies string) (accounts.Account, error)
}

// GoogleOAuth runs the Google Drive OAuth flow with the loopback redirect
// served by the API.
type GoogleOAuth interface {
	// Start returns the consent URL for accountID. redirectURL is the
	// absolute URL of GET /api/v1/oauth/google/callback on this server; the
	// implementation must put an unguessable state in the URL.
	Start(ctx context.Context, accountID, redirectURL string) (string, error)
	// Callback handles the browser redirect (query: state, code, error). It
	// is called WITHOUT authentication (the redirect from Google carries no
	// credentials), so it must reject unknown or reused states.
	Callback(ctx context.Context, query url.Values) error
	// CompleteURL finishes a flow from the final redirect URL pasted by
	// the user when the browser could not reach the callback (remote
	// daemon). Same state checks as Callback.
	CompleteURL(ctx context.Context, pastedURL string) error
}

// SettingsService reads and writes the safe settings view. ConfigSettings
// is the standard implementation.
type SettingsService interface {
	Get(ctx context.Context) (Settings, error)
	// Put applies the editable fields of s (read-only sections are
	// ignored) and returns the new view.
	Put(ctx context.Context, s Settings) (Settings, error)
}

// SelfTestRunner runs a plugin self-test. RegistrySelfTester is the
// standard implementation. An unknown hoster must return an error wrapping
// core.ErrNotFound; a failing test is reported in the result, not as error.
type SelfTestRunner interface {
	SelfTest(ctx context.Context, hoster string) (SelfTestResult, error)
}

// EgressService imports and tests egresses (implemented by the rotation
// manager or an adapter). Bad input should wrap core.ErrInvalidArgument.
type EgressService interface {
	ImportProxies(ctx context.Context, text string) (ImportResult, error)
	ImportWireGuard(ctx context.Context, name, conf string) (ImportResult, error)
	// Test checks one egress (health + public IP) and returns its status.
	Test(ctx context.Context, id string) (rotation.EgressStatus, error)
}

// PublicIPFunc queries the public IP seen through an egress (netx.PublicIP
// or a closure over config.Rotation.IPCheckServices).
type PublicIPFunc func(ctx context.Context, e netx.Egress) (netip.Addr, error)

// Deps are the collaborators of the API. Every service may be nil: the
// endpoints that need it answer 501.
type Deps struct {
	Engine   core.Engine
	Rotation rotation.Manager
	Accounts AccountService
	OAuth    GoogleOAuth
	Secrets  SecretsService
	Settings SettingsService
	SelfTest SelfTestRunner
	Egress   EgressService
	PublicIP PublicIPFunc

	// Server is config.Server (token, password hash, listen address,
	// allowed hosts/origins).
	Server config.ServerConfig
	Log    *slog.Logger
	// UI is the static web UI (web.Dist); nil = no UI.
	UI fs.FS
	// ExtraOrigins are allowed Origin values besides the server's own and
	// config server.allowed_origins (e.g. the desktop webview origin).
	ExtraOrigins []string

	// Context bounds background work (the SSE event history used for
	// Last-Event-ID replay). It should live as long as the handler and be
	// canceled at shutdown. If nil, each SSE client subscribes to the
	// engine directly and Last-Event-ID always produces a resync event.
	Context context.Context

	// SSEHeartbeat is the interval of SSE comment heartbeats (default 15s).
	SSEHeartbeat time.Duration
	// Now overrides time.Now (tests).
	Now func() time.Time
}

// server holds the state of one API handler.
type server struct {
	d      Deps
	log    *slog.Logger
	now    func() time.Time
	auth   *authenticator
	guard  *guard
	events *hub
	mux    *http.ServeMux
}

// New returns the HTTP handler serving the API under /api/v1 and the web UI
// at /.
func New(d Deps) http.Handler {
	s := &server{d: d, log: d.Log, now: d.Now}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.d.SSEHeartbeat <= 0 {
		s.d.SSEHeartbeat = 15 * time.Second
	}
	s.auth = newAuthenticator(d.Server, s.now, s.log)
	s.guard = newGuard(d.Server, d.ExtraOrigins)
	if d.Engine != nil && d.Context != nil {
		s.events = newHub(d.Context, d.Engine, 1024)
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s.guard.wrap(s.mux)
}

func (s *server) routes() {
	const p = "/api/v1"
	pub := func(pattern string, h http.HandlerFunc) { s.mux.Handle(pattern, h) }
	priv := func(pattern string, h http.HandlerFunc) { s.mux.Handle(pattern, s.auth.require(h)) }

	// Public.
	pub("GET "+p+"/health", s.health)
	pub("POST "+p+"/auth/login", s.auth.login)
	pub("POST "+p+"/auth/logout", s.auth.logout)
	pub("GET "+p+"/auth/session", s.auth.session)
	pub("GET "+p+"/oauth/google/callback", s.oauthCallback)

	// Events and LinkGrabber.
	priv("GET "+p+"/events", s.eventsHandler)
	priv("POST "+p+"/links", s.addLinks)
	priv("POST "+p+"/links/known", s.knownLinks)
	priv("GET "+p+"/linkgrabber", s.listGrabber)
	priv("POST "+p+"/linkgrabber/confirm", s.confirmGrabber)
	priv("POST "+p+"/linkgrabber/remove", s.removeGrabber)

	// Downloads.
	priv("GET "+p+"/downloads", s.listDownloads)
	priv("GET "+p+"/downloads/{id}", s.getDownload)
	priv("POST "+p+"/downloads/retry-failed", s.retryFailed)
	priv("POST "+p+"/downloads/{action}", s.downloadAction)

	// Packages.
	priv("GET "+p+"/packages", s.listPackages)
	priv("GET "+p+"/packages/{id}", s.getPackage)
	priv("POST "+p+"/packages/{action}", s.packageAction)
	priv("POST "+p+"/packages/{id}/rescan", s.rescanPackage)

	// Global.
	priv("POST "+p+"/pause-all", s.pauseAll)
	priv("POST "+p+"/resume-all", s.resumeAll)
	priv("GET "+p+"/stats", s.stats)

	// Accounts.
	priv("GET "+p+"/accounts", s.listAccounts)
	priv("POST "+p+"/accounts", s.createAccount)
	priv("POST "+p+"/accounts/import-cookies", s.importCookies)
	priv("PATCH "+p+"/accounts/{id}", s.updateAccount)
	priv("DELETE "+p+"/accounts/{id}", s.deleteAccount)
	priv("POST "+p+"/accounts/{id}/check", s.checkAccount)
	priv("POST "+p+"/accounts/{id}/2fa", s.submit2FA)
	priv("GET "+p+"/oauth/google/start", s.oauthStart)
	priv("POST "+p+"/oauth/google/complete", s.oauthComplete)

	// Secret store (vault).
	priv("GET "+p+"/secrets", s.secretsStatus)
	priv("POST "+p+"/secrets/unlock", s.secretsUnlock)
	priv("POST "+p+"/secrets/lock", s.secretsLock)
	priv("POST "+p+"/secrets/password", s.secretsPassword)

	// Network.
	priv("GET "+p+"/egress", s.listEgress)
	priv("POST "+p+"/egress/import", s.importProxies)
	priv("POST "+p+"/egress/wireguard", s.importWireGuard)
	priv("POST "+p+"/egress/{id}/test", s.testEgress)
	priv("POST "+p+"/rotation/rotate", s.rotate)
	priv("GET "+p+"/ip", s.publicIPs)

	// Settings and diagnostics.
	priv("GET "+p+"/settings", s.getSettings)
	priv("PUT "+p+"/settings", s.putSettings)
	priv("POST "+p+"/selftest/{hoster}", s.selfTest)

	// Unknown API paths are JSON 404s, never the SPA.
	pub("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "ruta desconocida")
	})
	// The desktop app answers this itself; here it does not exist.
	pub("/__adesc/", http.NotFound)
	pub("/", s.static)
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: "ok", Version: buildinfo.Version, Engine: s.d.Engine != nil})
}
