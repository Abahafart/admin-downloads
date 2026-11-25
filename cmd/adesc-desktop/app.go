package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"admindescargas/internal/app"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/desktop"
	"admindescargas/internal/desktop/clipboard"
	"admindescargas/internal/hosters"
	"admindescargas/internal/linkgrabber"
)

// desktopApp is the native shell around one connection to a core.
type desktopApp struct {
	cfgPath     string
	log         *slog.Logger
	coreLog     *slog.Logger
	args        cliArgs
	startupErr  error
	exitCode    int
	closeToTray bool
	systemDark  bool

	cfgMu sync.Mutex
	cfg   *config.Config

	ctx    context.Context // Wails runtime context, valid after startup
	bg     context.Context // background work, canceled in shutdown
	cancel context.CancelFunc

	mu         sync.Mutex
	appearance desktop.Appearance
	ready      chan struct{} // closed when the current connection attempt ends
	conn       desktop.Connection
	connErr    error
	core       *app.App
	api        *desktop.API
	svcCancel  context.CancelFunc
	svcWG      sync.WaitGroup
	pending    []pendingLinks
	stats      core.Stats
	monitor    *clipboard.Monitor
	clipErr    error

	tray     *trayUI
	notify   *notifications
	quitting atomic.Bool
}

type pendingLinks struct {
	links  []string
	text   string
	source string
}

func newDesktopApp(cfgPath string, cfg *config.Config, log *slog.Logger, args cliArgs) *desktopApp {
	st := desktop.LoadState(cfg.DataDir)
	d := &desktopApp{
		cfgPath:    cfgPath,
		cfg:        cfg,
		log:        log,
		args:       args,
		appearance: st.Appearance,
		systemDark: systemPrefersDark(),
		ready:      make(chan struct{}),
	}
	d.closeToTray = cfg.Desktop.CloseToTray && trayAvailable()
	if len(args.links) > 0 || args.text != "" {
		d.pending = append(d.pending, pendingLinks{args.links, args.text, "desktop"})
	}
	d.notify = &notifications{d: d}
	d.tray = &trayUI{d: d}
	return d
}

// config returns a copy of the configuration.
func (d *desktopApp) config() config.Config {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	return *d.cfg
}

// updateConfig applies fn to the in-memory config and to config.toml (read
// fresh, so fields changed by the API's settings endpoint are kept).
func (d *desktopApp) updateConfig(fn func(*config.Config)) error {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	onDisk, err := config.Load(d.cfgPath)
	if err != nil {
		return fmt.Errorf("leer %s: %w", d.cfgPath, err)
	}
	fn(onDisk)
	if err := onDisk.Validate(); err != nil {
		return err
	}
	if err := config.Save(d.cfgPath, onDisk); err != nil {
		return err
	}
	fn(d.cfg)
	return nil
}

func (d *desktopApp) initialBackground() desktop.RGB {
	return d.appearance.Background(d.systemDark)
}

// --- Wails lifecycle ---------------------------------------------------

func (d *desktopApp) startup(ctx context.Context) {
	d.ctx = ctx
	d.bg, d.cancel = context.WithCancel(context.Background())
	// Always initialized: the clipboard prompt uses notifications even when
	// download notifications are off.
	go d.notify.init()
	if trayAvailable() {
		d.tray.start()
	}
	go d.establish()
	if d.startupErr != nil {
		go d.showError("Configuración no válida", d.startupErr.Error())
	}
}

func (d *desktopApp) domReady(context.Context) {}

// beforeClose runs on quit (window close without tray, tray "Salir",
// Cmd+Q): never prevent it.
func (d *desktopApp) beforeClose(context.Context) bool { return false }

func (d *desktopApp) shutdown(context.Context) {
	d.quitting.Store(true)
	if d.cancel != nil {
		d.cancel()
	}
	d.stopServices()
	d.tray.stop()
	d.notify.cleanup()
	d.mu.Lock()
	c := d.core
	d.core = nil
	d.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := c.Stop(ctx); err != nil {
			d.log.Error("core stop", "err", err)
		}
	}
}

// onSecondInstance receives the arguments of a second launch (links,
// adesc:// URLs) and focuses the window.
func (d *desktopApp) onSecondInstance(data options.SecondInstanceData) {
	a := parseArgs(data.Args)
	if len(a.links) > 0 || a.text != "" {
		d.addLinks(a.links, a.text, "desktop")
	}
	if a.hidden && len(a.links) == 0 {
		return // a second autostart: nothing to show
	}
	d.showWindow()
}

// onURLOpen handles adesc:// URLs on macOS (Apple events, not argv).
func (d *desktopApp) onURLOpen(raw string) {
	links, text, ok := desktop.ParseSchemeURL(raw)
	if !ok {
		return
	}
	if len(links) > 0 || text != "" {
		d.addLinks(links, text, "desktop")
	}
	d.showWindow()
}

// --- window helpers ----------------------------------------------------

func (d *desktopApp) showWindow() {
	if d.ctx == nil {
		return
	}
	wruntime.WindowShow(d.ctx)
	wruntime.WindowUnminimise(d.ctx)
}

func (d *desktopApp) quit() {
	d.quitting.Store(true)
	if d.ctx != nil {
		wruntime.Quit(d.ctx)
	}
}

// navigate asks the UI to show a screen (Wails event "adesc:navigate").
func (d *desktopApp) navigate(screen string) {
	if d.ctx != nil {
		wruntime.EventsEmit(d.ctx, "adesc:navigate", screen)
	}
}

// --- connection ----------------------------------------------------------

// waitConnection blocks until the current connection attempt ends.
func (d *desktopApp) waitConnection(ctx context.Context) (desktop.Connection, error) {
	d.mu.Lock()
	ready := d.ready
	d.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return desktop.Connection{}, errors.New("el núcleo todavía está arrancando")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn, d.connErr
}

func (d *desktopApp) currentAPI() *desktop.API {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.api
}

// establish connects according to config.Desktop.Mode and starts the
// services (events, tray, clipboard).
func (d *desktopApp) establish() {
	conn, c, err := d.connect(d.bg)
	var api *desktop.API
	if err == nil {
		api, err = desktop.NewAPI(conn.BaseURL, conn.Token)
	}
	d.mu.Lock()
	d.conn, d.core, d.connErr, d.api = conn, c, err, api
	close(d.ready)
	d.mu.Unlock()
	if err != nil {
		d.log.Error("connect", "mode", d.config().Desktop.Mode, "err", err)
		d.tray.setStatus("Sin conexión")
		if !d.args.hidden && !d.quitting.Load() {
			d.showError("AdminDescargas", "No se pudo conectar con el núcleo:\n"+err.Error())
		}
		return
	}
	d.log.Info("connected", "mode", conn.Mode, "url", conn.BaseURL)
	d.startServices()
}

func (d *desktopApp) connect(ctx context.Context) (desktop.Connection, *app.App, error) {
	cfg := d.config()
	if desktop.Mode(cfg.Desktop.Mode) == desktop.ModeRemote {
		url, err := desktop.NormalizeRemoteURL(cfg.Desktop.RemoteURL)
		if err == nil {
			err = desktop.CheckServer(ctx, url, cfg.Desktop.RemoteToken)
		}
		if err == nil {
			return desktop.Connection{BaseURL: url, Token: cfg.Desktop.RemoteToken, Mode: desktop.ModeRemote}, nil, nil
		}
		msg := fmt.Sprintf("No se pudo conectar con %s:\n%v\n\n¿Usar el modo integrado (núcleo en este equipo)?", cfg.Desktop.RemoteURL, err)
		if !d.args.hidden && d.askYesNo("Modo cliente remoto", msg) {
			if uerr := d.updateConfig(func(c *config.Config) { c.Desktop.Mode = config.DesktopIntegrated }); uerr != nil {
				d.log.Warn("save mode", "err", uerr)
			}
			return d.connect(ctx)
		}
		return desktop.Connection{}, nil, err
	}

	a, conn, err := desktop.StartIntegrated(ctx, &cfg, d.coreLog, desktop.WebviewOrigins(), app.WithConfigPath(d.cfgPath))
	if err == nil {
		return conn, a, nil
	}
	if !errors.Is(err, app.ErrAlreadyRunning) {
		return desktop.Connection{}, nil, fmt.Errorf("no se pudo arrancar el núcleo: %w", err)
	}
	local, derr := desktop.DetectLocalDaemon(ctx, &cfg)
	if derr != nil {
		return desktop.Connection{}, nil, fmt.Errorf("otro proceso usa la carpeta de datos %s y no responde: %w", cfg.DataDir, derr)
	}
	// Started from autostart: connect silently.
	if d.args.hidden {
		return local, nil, nil
	}
	msg := fmt.Sprintf("Un daemon de AdminDescargas ya usa la carpeta de datos\n%s\ny escucha en %s.\n\n¿Conectarse a él como cliente?", cfg.DataDir, local.BaseURL)
	if d.askYesNo("AdminDescargas ya está en marcha", msg) {
		return local, nil, nil
	}
	d.quit()
	return desktop.Connection{}, nil, errors.New("cancelado: ya hay un daemon en marcha")
}

// reconnect stops the current connection (and the integrated core) and
// connects again with the current config, then reloads the UI.
func (d *desktopApp) reconnect() {
	d.stopServices()
	d.mu.Lock()
	c := d.core
	d.core, d.api, d.connErr = nil, nil, nil
	d.conn = desktop.Connection{}
	d.ready = make(chan struct{})
	d.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := c.Stop(ctx); err != nil {
			d.log.Error("core stop", "err", err)
		}
		cancel()
	}
	d.establish()
	if d.ctx != nil {
		wruntime.WindowReloadApp(d.ctx)
	}
}

// --- services ----------------------------------------------------------

func (d *desktopApp) startServices() {
	d.mu.Lock()
	api := d.api
	ctx, cancel := context.WithCancel(d.bg)
	d.svcCancel = cancel
	pending := d.pending
	d.pending = nil
	d.mu.Unlock()

	notifier := &desktop.Notifier{
		Emit: func(n desktop.Notification) {
			if d.config().Desktop.Notifications {
				d.notify.show(n)
			}
		},
		OnStats: d.setStats,
	}
	stream := &desktop.EventStream{
		Client:  api.C,
		OnEvent: notifier.Handle,
		OnState: func(ok bool, err error) {
			if ok {
				d.tray.setStatus("")
				return
			}
			d.tray.setStatus("Sin conexión")
			if err != nil && ctx.Err() == nil {
				d.log.Debug("events stream dropped", "err", err)
			}
		},
	}
	d.svcWG.Add(2)
	go func() {
		defer d.svcWG.Done()
		_ = stream.Run(ctx)
		notifier.Flush()
	}()
	go func() {
		defer d.svcWG.Done()
		d.refreshAppearance(ctx)
		if st, err := api.Stats(ctx); err == nil {
			d.setStats(st)
		}
	}()
	d.startClipboard(ctx, api)
	for _, p := range pending {
		d.addLinks(p.links, p.text, p.source)
	}
}

func (d *desktopApp) stopServices() {
	d.mu.Lock()
	cancel := d.svcCancel
	d.svcCancel = nil
	d.monitor = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.svcWG.Wait()
}

func (d *desktopApp) setStats(s core.Stats) {
	d.mu.Lock()
	d.stats = s
	d.mu.Unlock()
	d.tray.setStats(s)
}

// addLinks sends links to the LinkGrabber (queued until connected) and
// shows the LinkGrabber screen.
func (d *desktopApp) addLinks(links []string, text, source string) {
	if len(links) == 0 && text == "" {
		return
	}
	d.mu.Lock()
	api := d.api
	if api == nil {
		d.pending = append(d.pending, pendingLinks{links, text, source})
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(d.bg, 30*time.Second)
		defer cancel()
		if err := api.AddLinks(ctx, links, text, source, false); err != nil {
			d.log.Warn("add links", "links", len(links), "err", err)
			d.notify.show(desktop.Notification{Kind: desktop.NotifyNotice, Title: "No se pudieron añadir los enlaces", Body: err.Error(), Level: core.NoticeError})
			return
		}
		d.navigate("linkgrabber")
	}()
}

func (d *desktopApp) pauseAll() {
	if api := d.currentAPI(); api != nil {
		go d.apiCall("pausar todo", api.PauseAll)
	}
}

func (d *desktopApp) resumeAll() {
	if api := d.currentAPI(); api != nil {
		go d.apiCall("reanudar todo", api.ResumeAll)
	}
}

func (d *desktopApp) apiCall(what string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(d.bg, 30*time.Second)
	defer cancel()
	if err := fn(ctx); err != nil {
		d.log.Warn(what, "err", err)
		d.notify.show(desktop.Notification{Kind: desktop.NotifyNotice, Title: "No se pudo " + what, Body: err.Error(), Level: core.NoticeError})
	}
}

// --- clipboard ----------------------------------------------------------

// linkExtractor is the URL extractor of the clipboard monitor (the same
// one the core uses for pasted text).
var linkExtractor clipboard.Extractor = linkgrabber.ExtractURLs

func (d *desktopApp) startClipboard(ctx context.Context, api *desktop.API) {
	cfg := d.config()
	src, err := clipboard.NewSource(cfg.Clipboard)
	if err != nil {
		d.setClipboardState(nil, err)
		d.log.Info("clipboard monitor unavailable", "err", err)
		return
	}
	// Plugins only match URLs here, so a local registry works the same in
	// remote mode. Catch-all plugins are ignored by RegistryMatcher.
	reg, err := hosters.NewRegistryFromFactories(cfg.Hosters)
	if err != nil {
		d.setClipboardState(nil, err)
		return
	}
	m, err := clipboard.New(clipboard.Options{
		Source:   src,
		Extract:  linkExtractor,
		Match:    clipboard.RegistryMatcher(reg),
		Sink:     api,
		Known:    api.UnknownLinks,
		Prompter: d.notify,
		Config:   cfg.Clipboard,
		Logger:   d.log,
	})
	if err != nil {
		d.setClipboardState(nil, err)
		return
	}
	d.setClipboardState(m, nil)
	d.svcWG.Add(1)
	go func() {
		defer d.svcWG.Done()
		if err := m.Run(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("clipboard monitor stopped", "err", err)
			d.setClipboardState(nil, err)
		}
	}()
}

func (d *desktopApp) setClipboardState(m *clipboard.Monitor, err error) {
	d.mu.Lock()
	d.monitor, d.clipErr = m, err
	d.mu.Unlock()
	d.tray.setClipboard(m != nil, m != nil && m.Enabled())
}

// clipboardStatus is the body of GET /__adesc/clipboard.
type clipboardStatus struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend,omitempty"`
	Error     string `json:"error,omitempty"`
	config.ClipboardConfig
}

func (d *desktopApp) clipboardStatus() clipboardStatus {
	cfg := d.config()
	d.mu.Lock()
	defer d.mu.Unlock()
	st := clipboardStatus{ClipboardConfig: cfg.Clipboard}
	if d.monitor != nil {
		st.Available, st.Backend = true, d.monitor.SourceName()
		st.Enabled = d.monitor.Enabled()
	}
	if d.clipErr != nil {
		st.Error = d.clipErr.Error()
	}
	return st
}

// setClipboardConfig applies and persists the clipboard settings.
func (d *desktopApp) setClipboardConfig(c config.ClipboardConfig) error {
	if err := d.updateConfig(func(cfg *config.Config) { cfg.Clipboard = c }); err != nil {
		return err
	}
	d.mu.Lock()
	m := d.monitor
	d.mu.Unlock()
	if m != nil {
		m.SetConfig(c)
		d.tray.setClipboard(true, m.Enabled())
	}
	return nil
}

func (d *desktopApp) setClipboardEnabled(on bool) error {
	c := d.config().Clipboard
	c.Enabled = on
	return d.setClipboardConfig(c)
}

// --- appearance ----------------------------------------------------------

// refreshAppearance reads settings.appearance from the API and applies it.
func (d *desktopApp) refreshAppearance(ctx context.Context) {
	api := d.currentAPI()
	if api == nil {
		return
	}
	a, err := api.Appearance(ctx)
	if err != nil {
		d.log.Debug("settings appearance", "err", err)
		return
	}
	d.applyAppearance(a, "")
}

// applyAppearance updates the window background (and remembers it for the
// next start). esquema ("oscuro"/"claro"), when the UI sends it, is the
// scheme the UI resolved for tema "sistema".
func (d *desktopApp) applyAppearance(a desktop.Appearance, esquema string) {
	if a.Estilo == "" {
		a.Estilo = desktop.DefaultEstilo
	}
	if a.Tema == "" {
		a.Tema = desktop.TemaSistema
	}
	dark := d.systemDark
	switch esquema {
	case desktop.TemaOscuro:
		dark = true
	case desktop.TemaClaro:
		dark = false
	}
	d.mu.Lock()
	changed := d.appearance != a
	d.appearance = a
	d.mu.Unlock()
	if d.ctx != nil {
		c := a.Background(dark)
		wruntime.WindowSetBackgroundColour(d.ctx, c.R, c.G, c.B, 255)
		switch a.Tema {
		case desktop.TemaOscuro:
			wruntime.WindowSetDarkTheme(d.ctx)
		case desktop.TemaClaro:
			wruntime.WindowSetLightTheme(d.ctx)
		default:
			wruntime.WindowSetSystemDefaultTheme(d.ctx)
		}
	}
	if changed {
		if err := desktop.SaveState(d.config().DataDir, desktop.State{Appearance: a}); err != nil {
			d.log.Debug("save desktop state", "err", err)
		}
	}
}
