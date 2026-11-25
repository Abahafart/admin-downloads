package accounts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"admindescargas/internal/config"
	"admindescargas/internal/netx"
)

// GoogleDriveScope is the full Drive scope; it is needed to copy a shared
// file into the user's Drive when the original is over its download quota.
const GoogleDriveScope = "https://www.googleapis.com/auth/drive"

// GoogleHoster is the hoster name whose config section holds the OAuth
// client ([hosters.gdrive.options] client_id / client_secret).
const GoogleHoster = "gdrive"

// OAuth errors.
var (
	// ErrOAuthNotConfigured means client_id/client_secret are missing.
	ErrOAuthNotConfigured = errors.New("accounts: Google OAuth client is not configured (hosters.gdrive.options client_id / client_secret)")
	// ErrOAuthState means the state is unknown, already used or expired.
	ErrOAuthState = errors.New("accounts: OAuth state is invalid or expired")
	// ErrOAuthDenied means the user refused consent (or Google returned an
	// error).
	ErrOAuthDenied = errors.New("accounts: Google authorization was denied")
	// ErrOAuthScope means the user did not grant the Drive scope.
	ErrOAuthScope = errors.New("accounts: the Google Drive permission was not granted")
	// ErrBadRedirect means the redirect URL is not acceptable.
	ErrBadRedirect = errors.New("accounts: invalid OAuth redirect URL")
)

// OAuthTarget is what GoogleOAuth needs from the account manager
// (*Manager implements it).
type OAuthTarget interface {
	Get(ctx context.Context, id string) (*Account, error)
	SaveOAuthToken(ctx context.Context, id string, tok *oauth2.Token) error
}

// GoogleOAuthOptions configures NewGoogleOAuth.
type GoogleOAuthOptions struct {
	// Credentials returns the user's own "Desktop app" OAuth client. It is
	// called on every Start so config changes apply without restart.
	Credentials func() (clientID, clientSecret string)
	// Target stores the token (the Manager).
	Target OAuthTarget
	// HTTPClient performs the token exchange (default: netx direct egress).
	HTTPClient *http.Client
	// Endpoint defaults to google.Endpoint (tests use an httptest server).
	Endpoint oauth2.Endpoint
	// Scopes default to [GoogleDriveScope].
	Scopes []string
	// StateTTL is how long a started flow stays valid (default 10 minutes).
	StateTTL time.Duration
	// AllowNonLoopback accepts redirect URLs on other hosts (Google only
	// accepts loopback redirects for Desktop clients; keep false).
	AllowNonLoopback bool
	Logger           *slog.Logger
	Now              func() time.Time
}

// GoogleOAuth runs the OAuth 2.0 authorization-code flow with PKCE for
// Google Drive accounts, using the daemon itself as the loopback redirect
// target:
//
//  1. the UI calls Start(accountID, redirectURL) (API: GET
//     /api/v1/oauth/google/start?account_id=...) and opens the returned URL;
//  2. Google redirects the browser to redirectURL (the daemon, e.g.
//     http://127.0.0.1:7979/api/v1/oauth/google/callback), served by
//     HandleCallback. That route must NOT require the API token: the
//     one-time state is its CSRF protection;
//  3. if the browser cannot reach the daemon (remote daemon), the user can
//     paste the final URL into the UI (POST /api/v1/oauth/google/complete,
//     `adesc accounts oauth-complete`) and the API calls CompleteURL.
type GoogleOAuth struct {
	opts GoogleOAuthOptions
	log  *slog.Logger
	now  func() time.Time

	mu      sync.Mutex
	pending map[string]*oauthFlow // state -> flow
}

type oauthFlow struct {
	accountID string
	verifier  string
	redirect  string
	expires   time.Time
}

// maxPendingFlows bounds memory if Start is spammed.
const maxPendingFlows = 32

// NewGoogleOAuth builds the flow handler.
func NewGoogleOAuth(opts GoogleOAuthOptions) (*GoogleOAuth, error) {
	if opts.Credentials == nil || opts.Target == nil {
		return nil, errors.New("accounts: NewGoogleOAuth needs Credentials and Target")
	}
	if opts.Endpoint.AuthURL == "" {
		opts.Endpoint = google.Endpoint
	}
	if len(opts.Scopes) == 0 {
		opts.Scopes = []string{GoogleDriveScope}
	}
	if opts.StateTTL <= 0 {
		opts.StateTTL = 10 * time.Minute
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = netx.NewHTTPClient(netx.NewDirect(), nil)
	}
	g := &GoogleOAuth{opts: opts, log: opts.Logger, now: opts.Now, pending: map[string]*oauthFlow{}}
	if g.log == nil {
		g.log = slog.Default()
	}
	if g.now == nil {
		g.now = time.Now
	}
	return g, nil
}

// GoogleCredentialsFromConfig reads client_id / client_secret from
// cfg.Hosters["gdrive"].Options on every call.
func GoogleCredentialsFromConfig(cfg *config.Config) func() (string, string) {
	return func() (string, string) {
		o := cfg.Hoster(GoogleHoster).Options
		return strings.TrimSpace(o["client_id"]), strings.TrimSpace(o["client_secret"])
	}
}

// Configured reports whether an OAuth client is set.
func (g *GoogleOAuth) Configured() bool {
	id, secret := g.opts.Credentials()
	return id != "" && secret != ""
}

func (g *GoogleOAuth) config(redirect string) (*oauth2.Config, error) {
	id, secret := g.opts.Credentials()
	if id == "" || secret == "" {
		return nil, ErrOAuthNotConfigured
	}
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Endpoint:     g.opts.Endpoint,
		RedirectURL:  redirect,
		Scopes:       g.opts.Scopes,
	}, nil
}

func (g *GoogleOAuth) checkRedirect(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: %q", ErrBadRedirect, raw)
	}
	if g.opts.AllowNonLoopback {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: Google only accepts loopback addresses (127.0.0.1, [::1], localhost) for Desktop clients", ErrBadRedirect)
}

// Start begins a flow for an OAuth account and returns the Google consent
// URL. redirectURL must point to the daemon's callback route.
func (g *GoogleOAuth) Start(ctx context.Context, accountID, redirectURL string) (string, error) {
	if err := g.checkRedirect(redirectURL); err != nil {
		return "", err
	}
	cfg, err := g.config(redirectURL)
	if err != nil {
		return "", err
	}
	a, err := g.opts.Target.Get(ctx, accountID)
	if err != nil {
		return "", err
	}
	if a.Kind != KindOAuth {
		return "", fmt.Errorf("%w: account %s is not an OAuth account", ErrInvalidAccount, accountID)
	}
	var sb [32]byte
	_, _ = rand.Read(sb[:])
	state := base64.RawURLEncoding.EncodeToString(sb[:])
	verifier := oauth2.GenerateVerifier()

	now := g.now()
	g.mu.Lock()
	for k, f := range g.pending {
		if !now.Before(f.expires) {
			delete(g.pending, k)
		}
	}
	if len(g.pending) >= maxPendingFlows {
		// Drop the oldest flow.
		var oldest string
		for k, f := range g.pending {
			if oldest == "" || f.expires.Before(g.pending[oldest].expires) {
				oldest = k
			}
		}
		delete(g.pending, oldest)
	}
	g.pending[state] = &oauthFlow{accountID: accountID, verifier: verifier, redirect: redirectURL, expires: now.Add(g.opts.StateTTL)}
	g.mu.Unlock()

	opts := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce, // always return a refresh token
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("include_granted_scopes", "true"),
	}
	if strings.Contains(a.Username, "@") {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", a.Username))
	}
	g.log.Info("google oauth started", "account", accountID)
	return cfg.AuthCodeURL(state, opts...), nil
}

// takeFlow removes and returns the flow for state if it is still valid.
func (g *GoogleOAuth) takeFlow(state string) (*oauthFlow, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, ok := g.pending[state]
	if !ok || state == "" {
		return nil, ErrOAuthState
	}
	delete(g.pending, state) // one-time use
	if !g.now().Before(f.expires) {
		return nil, ErrOAuthState
	}
	return f, nil
}

// complete validates the callback parameters, exchanges the code and stores
// the token. It returns the account ID.
func (g *GoogleOAuth) complete(ctx context.Context, q url.Values) (string, error) {
	state := q.Get("state")
	if e := q.Get("error"); e != "" {
		// Consume the state so it cannot be replayed.
		_, _ = g.takeFlow(state)
		return "", fmt.Errorf("%w: %s", ErrOAuthDenied, e)
	}
	f, err := g.takeFlow(state)
	if err != nil {
		return "", err
	}
	code := q.Get("code")
	if code == "" {
		return f.accountID, fmt.Errorf("%w: missing code", ErrOAuthDenied)
	}
	cfg, err := g.config(f.redirect)
	if err != nil {
		return f.accountID, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.opts.HTTPClient)
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return f.accountID, fmt.Errorf("accounts: token exchange failed: %s %s", re.ErrorCode, re.ErrorDescription)
		}
		return f.accountID, fmt.Errorf("accounts: token exchange failed: %w", err)
	}
	if !scopeGranted(tok, g.opts.Scopes) {
		return f.accountID, ErrOAuthScope
	}
	if err := g.opts.Target.SaveOAuthToken(ctx, f.accountID, tok); err != nil {
		return f.accountID, err
	}
	g.log.Info("google oauth completed", "account", f.accountID, "refresh_token", tok.RefreshToken != "")
	return f.accountID, nil
}

// scopeGranted checks the "scope" field of the token response when Google
// sends it (it does, space separated).
func scopeGranted(tok *oauth2.Token, want []string) bool {
	s, _ := tok.Extra("scope").(string)
	if s == "" {
		return true
	}
	got := strings.Fields(s)
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

// Complete finishes a flow from the callback query (state, code, error):
// it is the transport-neutral version of HandleCallback. It returns the
// account ID.
func (g *GoogleOAuth) Complete(ctx context.Context, q url.Values) (string, error) {
	return g.complete(ctx, q)
}

// CompleteURL finishes a flow from the final redirect URL pasted by the
// user (when the browser could not reach the daemon). It returns the
// account ID.
func (g *GoogleOAuth) CompleteURL(ctx context.Context, pasted string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || u.RawQuery == "" {
		return "", fmt.Errorf("%w: paste the full address shown by the browser after authorizing", ErrOAuthState)
	}
	return g.complete(ctx, u.Query())
}

// HandleCallback serves the redirect from Google and answers with a small
// Spanish HTML page. Mount it without API-token authentication.
func (g *GoogleOAuth) HandleCallback(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer") // the URL carries the code
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		renderOAuthPage(w, "Método no permitido", "Esta dirección solo la usa Google para volver a la aplicación.", false)
		return
	}
	_, err := g.complete(r.Context(), r.URL.Query())
	if err == nil {
		renderOAuthPage(w, "Cuenta de Google conectada",
			"AdminDescargas ya puede usar tu Google Drive. Puedes cerrar esta pestaña y volver a la aplicación.", true)
		return
	}
	g.log.Warn("google oauth callback failed", "err", err)
	status, title, msg := http.StatusBadRequest, "No se pudo conectar la cuenta", ""
	switch {
	case errors.Is(err, ErrOAuthState):
		msg = "El enlace de autorización no es válido o ha caducado. Vuelve a pulsar «Conectar con Google» en Cuentas."
	case errors.Is(err, ErrOAuthDenied):
		title = "Autorización cancelada"
		msg = "No se concedió el acceso a Google Drive. Si fue un error, vuelve a intentarlo desde Cuentas."
	case errors.Is(err, ErrOAuthScope):
		msg = "Hay que marcar el permiso de Google Drive en la pantalla de Google: sin él no se pueden copiar archivos con la cuota agotada."
	case errors.Is(err, ErrOAuthNotConfigured):
		status = http.StatusInternalServerError
		msg = "Falta el client_id o el client_secret de Google en la configuración (sección hosters.gdrive)."
	case errors.Is(err, ErrLocked):
		status = http.StatusServiceUnavailable
		msg = "El almacén de credenciales está bloqueado. Desbloquéalo con la contraseña maestra y vuelve a conectar la cuenta."
	case errors.Is(err, ErrAccountNotFound):
		status = http.StatusNotFound
		msg = "La cuenta ya no existe. Créala de nuevo en Cuentas."
	default:
		status = http.StatusBadGateway
		msg = "Google rechazó el intercambio del código. Comprueba que el cliente OAuth es de tipo «Aplicación de escritorio» y vuelve a intentarlo."
	}
	w.WriteHeader(status)
	renderOAuthPage(w, title, msg, false)
}

var oauthPage = template.Must(template.New("oauth").Parse(`<!doctype html>
<html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · AdminDescargas</title>
<style>
:root{color-scheme:light dark}
body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;margin:0;min-height:100vh;display:grid;place-items:center;background:Canvas;color:CanvasText}
main{max-width:32rem;padding:2rem;border:1px solid color-mix(in srgb,CanvasText 20%,transparent);border-radius:12px;margin:1rem}
h1{font-size:1.3rem;margin:0 0 .75rem}
p{line-height:1.5;margin:0}
.ok h1{color:#1a7f37}.err h1{color:#cf222e}
</style></head>
<body><main class="{{if .OK}}ok{{else}}err{{end}}"><h1>{{.Title}}</h1><p>{{.Message}}</p></main></body></html>
`))

func renderOAuthPage(w http.ResponseWriter, title, msg string, ok bool) {
	_ = oauthPage.Execute(w, struct {
		Title, Message string
		OK             bool
	}{title, msg, ok})
}
