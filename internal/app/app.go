// Package app boots the core shared by the daemon (`adesc daemon`) and the
// desktop app (adesc-desktop, integrated mode): data-dir lock, SQLite store,
// secret store, hoster registry, accounts manager, Google OAuth, rotation
// manager, post-processor, engine and the HTTP API.
//
// Only one core may run per data directory: Start takes an exclusive file
// lock (gofrs/flock) and fails with ErrAlreadyRunning otherwise; the desktop
// app then reads the Instance file and offers to connect as a client.
//
// Start order: lock, store, secret store, registry, accounts manager (and
// Google OAuth), rotation manager, post-processor, engine (whose global
// rotation hooks are then installed in the rotation manager), API. Stop
// runs in reverse: HTTP, API background work, engine (flushes offsets),
// rotation (closes tunnels / Tor), store, lock.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"admindescargas/internal/accounts"
	"admindescargas/internal/api"
	"admindescargas/internal/buildinfo"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/hosters"
	_ "admindescargas/internal/hosters/all" // register every plugin factory
	"admindescargas/internal/netx"
	"admindescargas/internal/postprocess"
	"admindescargas/internal/rotation"
	"admindescargas/internal/store"
	"admindescargas/web"
)

// ErrAlreadyRunning is returned by Start when another process holds the
// data-dir lock.
var ErrAlreadyRunning = errors.New("app: another instance is already using this data directory")

// LockFileName and InstanceFileName live in config.DataDir.
const (
	LockFileName     = "adesc.lock"
	InstanceFileName = "instance.json"
)

// Option customizes New.
type Option func(*App)

// WithLogger sets the logger (default: built from cfg.Log).
func WithLogger(l *slog.Logger) Option { return func(a *App) { a.log = l } }

// WithListener makes the API serve on ln instead of cfg.Server.Listen (the
// desktop app may pass a 127.0.0.1:0 listener).
func WithListener(ln net.Listener) Option { return func(a *App) { a.ln = ln } }

// WithoutHTTP starts the core without serving HTTP (tests, embedders that
// mount Handler themselves).
func WithoutHTTP() Option { return func(a *App) { a.noHTTP = true } }

// WithConfigPath is the config.toml the configuration was loaded from: the
// settings endpoint (PUT /api/v1/settings) saves its changes there. Without
// it settings changes are kept in memory only.
func WithConfigPath(path string) Option { return func(a *App) { a.cfgPath = path } }

// WithExtraOrigins adds Origin values the API accepts (CORS) besides the
// server's own origin and server.allowed_origins, without writing them to
// the configuration. The desktop app passes its webview origins
// (wails://wails, http://wails.localhost, http://localhost:34115 in dev).
func WithExtraOrigins(origins ...string) Option {
	return func(a *App) { a.extraOrigins = append(a.extraOrigins, origins...) }
}

// WithHosters registers extra plugin instances besides the registered
// factories (tests and embedders). Names must not collide.
func WithHosters(hs ...hosters.Hoster) Option {
	return func(a *App) { a.extraHosters = append(a.extraHosters, hs...) }
}

// App is one running core.
type App struct {
	cfg          *config.Config
	log          *slog.Logger
	ln           net.Listener
	noHTTP       bool
	cfgPath      string
	extraOrigins []string
	extraHosters []hosters.Hoster

	mu       sync.Mutex
	started  bool
	lock     *flock.Flock
	store    *store.Store
	secrets  *accounts.OpenedStore
	registry *hosters.Registry
	accounts *accounts.Manager
	oauth    *accounts.GoogleOAuth
	rotator  *rotation.Rotator
	post     *postprocess.Processor
	bus      *core.Bus
	engine   *core.Service
	settings *api.ConfigSettings
	handler  http.Handler
	srv      *http.Server
	srvDone  chan error
	// life bounds background work of the API (SSE replay history).
	life       context.Context
	lifeCancel context.CancelFunc
}

// New validates cfg and prepares an App; nothing is opened until Start.
func New(cfg *config.Config, opts ...Option) (*App, error) {
	if cfg == nil {
		return nil, errors.New("app: nil config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	a := &App{cfg: cfg}
	for _, o := range opts {
		o(a)
	}
	if a.log == nil {
		l, err := NewLogger(cfg.Log)
		if err != nil {
			return nil, err
		}
		a.log = l
	}
	return a, nil
}

// Start acquires the data-dir lock, opens the store, wires the core and
// starts serving the API. On error everything opened so far is released.
func (a *App) Start(ctx context.Context) (err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return errors.New("app: already started")
	}
	defer func() {
		if err != nil {
			_ = a.cleanup(context.Background())
		}
	}()
	cfg := a.cfg

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("app: data dir: %w", err)
	}
	a.lock = flock.New(filepath.Join(cfg.DataDir, LockFileName))
	ok, err := a.lock.TryLock()
	if err != nil {
		return fmt.Errorf("app: lock: %w", err)
	}
	if !ok {
		a.lock = nil
		return ErrAlreadyRunning
	}

	a.store, err = store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		return err
	}

	a.secrets, err = accounts.OpenSecretStore(ctx, accounts.StoreOptions{
		Backend:           cfg.Secrets.Backend,
		VaultPath:         cfg.Secrets.VaultPath,
		VaultPasswordFile: cfg.Secrets.VaultPasswordFile,
		Logger:            a.log.With("component", "secrets"),
	}, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("app: secret store: %w", err)
	}

	a.registry, err = hosters.NewRegistryFromFactories(cfg.Hosters)
	if err != nil {
		return err
	}
	for _, h := range a.extraHosters {
		if err := a.registry.Register(h); err != nil {
			return err
		}
	}

	a.bus = core.NewBus(1024)
	a.accounts, err = accounts.NewManager(ctx, accounts.ManagerOptions{
		Repo:    a.store,
		Secrets: a.secrets,
		Checker: accountChecker(a.registry, func() rotation.Manager {
			if a.rotator == nil {
				return nil
			}
			return a.rotator
		}, a.secrets, a.log),
		HosterDomains: hosterDomains(cfg),
		OnChange:      accountEvents(a.bus),
		Logger:        a.log.With("component", "accounts"),
	})
	if err != nil {
		return err
	}
	a.oauth, err = accounts.NewGoogleOAuth(accounts.GoogleOAuthOptions{
		Credentials: accounts.GoogleCredentialsFromConfig(cfg),
		Target:      a.accounts,
		HTTPClient:  netx.NewHTTPClient(netx.NewDirect(), nil),
		Logger:      a.log.With("component", "oauth"),
	})
	if err != nil {
		return err
	}

	a.rotator, err = rotation.NewFromConfig(ctx, cfg, a.store, a.secrets, a.log.With("component", "rotation"), rotation.Hooks{})
	if err != nil {
		return fmt.Errorf("app: rotation: %w", err)
	}

	a.post = postprocess.New(cfg.PostProcess, a.log.With("component", "postprocess"))
	a.post.RegisterVerifier(hosters.AlgoMEGAMAC, megaMACVerifier)

	a.engine, err = core.New(core.Deps{
		Store:         a.store,
		Registry:      a.registry,
		Rotation:      a.rotator,
		Secrets:       a.secrets,
		Config:        cfg,
		Logger:        a.log,
		Accounts:      accountPicker{m: a.accounts, log: a.log.With("component", "accounts")},
		PostProcessor: postProcessor{p: a.post, log: a.log.With("component", "postprocess")},
		Bus:           a.bus,
	})
	if err != nil {
		return err
	}
	bus := a.bus
	a.rotator.SetHooks(rotation.Hooks{
		PauseAll:  a.engine.SuspendForRotation,
		ResumeAll: a.engine.ResumeFromRotation,
		Changed: func(id string) {
			bus.Publish(core.Event{Type: core.EventEgressUpdated, EgressID: id})
		},
	})
	if err := a.engine.Start(ctx); err != nil {
		return fmt.Errorf("app: engine: %w", err)
	}

	a.life, a.lifeCancel = context.WithCancel(context.WithoutCancel(ctx))
	a.settings = api.NewConfigSettings(a.cfgPath, cfg, a.store)
	ipServices := cfg.Rotation.IPCheckServices
	a.handler = api.New(api.Deps{
		Engine:   a.engine,
		Rotation: a.rotator,
		Accounts: accountService{m: a.accounts, reg: a.registry},
		OAuth:    googleOAuth{g: a.oauth},
		Secrets:  secretsService{m: a.accounts},
		Settings: a.settings,
		SelfTest: &api.RegistrySelfTester{Registry: a.registry, Rotation: a.rotator, Log: a.log},
		Egress:   egressService{a: a.rotator},
		PublicIP: func(ctx context.Context, e netx.Egress) (netip.Addr, error) {
			c := netx.NewHTTPClient(e, nil)
			defer c.CloseIdleConnections()
			return netx.PublicIPWith(ctx, c, orServices(ipServices))
		},
		Server:       cfg.Server,
		Log:          a.log.With("component", "api"),
		UI:           web.Dist,
		ExtraOrigins: slices.Clone(a.extraOrigins),
		Context:      a.life,
	})

	if !a.noHTTP {
		if err := a.serve(); err != nil {
			return err
		}
	}
	a.started = true
	st := a.secrets.Status
	a.log.Info("core started", "version", buildinfo.Version, "data_dir", cfg.DataDir, "addr", a.Addr(),
		"secrets", st.Backend, "secrets_locked", accounts.StatusOf(a.secrets).Locked, "hosters", len(a.registry.All()))
	return nil
}

func orServices(s []string) []string {
	if len(s) == 0 {
		return netx.DefaultIPEchoServices
	}
	return s
}

func (a *App) serve() error {
	if a.ln == nil {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", a.cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("app: listen %s: %w", a.cfg.Server.Listen, err)
		}
		a.ln = ln
	}
	a.srv = &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
	}
	a.srvDone = make(chan error, 1)
	go func() {
		var err error
		if a.cfg.Server.TLSCert != "" {
			err = a.srv.ServeTLS(a.ln, a.cfg.Server.TLSCert, a.cfg.Server.TLSKey)
		} else {
			err = a.srv.Serve(a.ln)
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		a.srvDone <- err
	}()
	return writeInstance(a.cfg.DataDir, Instance{PID: os.Getpid(), Addr: a.ln.Addr().String(), TLS: a.cfg.Server.TLSCert != "", Version: buildinfo.Version})
}

// Stop shuts down the API, stops the engine (flushing progress), closes the
// store and releases the lock. It is safe to call more than once.
func (a *App) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return nil
	}
	a.started = false
	err := a.cleanup(ctx)
	a.log.Info("core stopped")
	return err
}

// cleanup releases everything that was opened; callers hold a.mu.
func (a *App) cleanup(ctx context.Context) error {
	var errs []error
	if a.srv != nil {
		if err := a.srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("http shutdown: %w", err))
		}
		if err := <-a.srvDone; err != nil {
			errs = append(errs, fmt.Errorf("http serve: %w", err))
		}
		a.srv = nil
		a.ln = nil
		_ = os.Remove(filepath.Join(a.cfg.DataDir, InstanceFileName))
	}
	if a.lifeCancel != nil {
		a.lifeCancel()
		a.lifeCancel = nil
	}
	if a.engine != nil {
		if err := a.engine.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("engine stop: %w", err))
		}
		a.engine = nil
	}
	if a.rotator != nil {
		if err := a.rotator.Close(); err != nil {
			errs = append(errs, fmt.Errorf("rotation close: %w", err))
		}
		a.rotator = nil
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("store close: %w", err))
		}
		a.store = nil
	}
	if a.lock != nil {
		if err := a.lock.Unlock(); err != nil {
			errs = append(errs, fmt.Errorf("unlock: %w", err))
		}
		a.lock = nil
	}
	return errors.Join(errs...)
}

// Handler returns the HTTP handler (API + UI). Valid after Start; the
// desktop app may mount it in its webview asset server.
func (a *App) Handler() http.Handler {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.handler
}

// Addr returns the address the API listens on ("" when not serving).
func (a *App) Addr() string {
	if a.ln == nil {
		return ""
	}
	return a.ln.Addr().String()
}

// BaseURL returns the URL clients should use (http[s]://addr), or "".
func (a *App) BaseURL() string {
	addr := a.Addr()
	if addr == "" {
		return ""
	}
	scheme := "http"
	if a.cfg.Server.TLSCert != "" {
		scheme = "https"
	}
	return scheme + "://" + addr
}

// Config returns the configuration the app was built with.
func (a *App) Config() *config.Config { return a.cfg }

// Logger returns the app logger.
func (a *App) Logger() *slog.Logger { return a.log }

// Engine returns the engine (nil before Start).
func (a *App) Engine() core.Engine {
	if a.engine == nil {
		return nil
	}
	return a.engine
}

// Accounts returns the accounts manager (nil before Start).
func (a *App) Accounts() *accounts.Manager { return a.accounts }

// Bus returns the engine's event bus (nil before Start).
func (a *App) Bus() *core.Bus { return a.bus }

// Store returns the store (nil before Start).
func (a *App) Store() *store.Store { return a.store }

// Registry returns the hoster registry (nil before Start).
func (a *App) Registry() *hosters.Registry { return a.registry }

// Rotation returns the rotation manager (nil before Start).
func (a *App) Rotation() rotation.Manager {
	if a.rotator == nil {
		return nil
	}
	return a.rotator
}

// Secrets returns the secret store (nil before Start).
func (a *App) Secrets() accounts.SecretStore {
	if a.secrets == nil {
		return nil
	}
	return a.secrets
}
