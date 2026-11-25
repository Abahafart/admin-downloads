// Command adesc-desktop is the AdminDescargas desktop app (PLAN.md section
// 8): a Wails v3 window with the same Svelte UI as the daemon, a tray icon,
// system notifications, native dialogs, the clipboard monitor, autostart,
// single instance and the adesc:// URL scheme.
//
// Modes (config [desktop].mode):
//   - integrated (default): the core runs in this process (internal/app) and
//     the API listens on 127.0.0.1 so the CLI and the browser extension work
//     while the app is open. If a daemon already owns the data directory the
//     app offers to connect to it as a client.
//   - remote: client of an `adesc daemon` elsewhere (URL + token).
//
// The UI always talks HTTP + SSE to the API; the Wails asset server only
// adds a few same-origin endpoints under /__adesc/ (connection, native
// dialogs, clipboard, desktop settings), see assets.go.
//
// This is a separate Go module so Wails never enters the root go.mod.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	wnotify "github.com/wailsapp/wails/v3/pkg/services/notifications"

	"admindescargas/internal/app"
	"admindescargas/internal/buildinfo"
	"admindescargas/internal/config"
	"admindescargas/internal/desktop"
)

func main() { os.Exit(run(os.Args[1:])) }

// cliArgs are the command line options. Unknown arguments are ignored
// (macOS adds -psn_..., file managers and URL handlers pass links).
type cliArgs struct {
	configPath string
	hidden     bool
	version    bool
	help       bool
	links      []string
	text       string
}

func parseArgs(args []string) cliArgs {
	var c cliArgs
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" || a == "-config":
			if i+1 < len(args) {
				c.configPath = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--config="):
			c.configPath = strings.TrimPrefix(a, "--config=")
		case a == desktop.HiddenFlag || a == "-hidden":
			c.hidden = true
		case a == "--version" || a == "-version":
			c.version = true
		case a == "--help" || a == "-h" || a == "-help":
			c.help = true
		default:
			rest = append(rest, a)
		}
	}
	c.links, c.text = desktop.LinksFromArgs(rest)
	return c
}

const usage = `Uso: adesc-desktop [--config <config.toml>] [--hidden] [enlaces o adesc://add?url=...]

  --config   ruta de config.toml (por defecto: <config del usuario>/admindescargas/config.toml)
  --hidden   arrancar en la bandeja, sin mostrar la ventana
  --version  mostrar la versión
`

func run(argv []string) int {
	args := parseArgs(argv)
	if args.help {
		fmt.Print(usage)
		return 0
	}
	if args.version {
		fmt.Println("adesc-desktop", buildinfo.Version)
		return 0
	}

	cfgPath := args.configPath
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cfgPath = p
	}
	cfg, cfgErr := config.Load(cfgPath)
	if cfgErr != nil {
		// Keep going with defaults so the user sees the error in a dialog
		// (a GUI app has no console on Windows/macOS).
		cfg = config.Default()
		if tok, err := config.GenerateToken(); err == nil {
			cfg.Server.Token = tok
		}
	}

	baseLog := newLogger(cfg)
	log := baseLog.With("component", "desktop")
	d := newDesktopApp(cfgPath, cfg, log, args)
	d.coreLog = baseLog.With("component", "core")
	if cfgErr != nil {
		d.startupErr = fmt.Errorf("no se pudo leer %s: %w (se usan los valores por defecto)", cfgPath, cfgErr)
		log.Error("config", "err", cfgErr)
	}

	ns := wnotify.New()
	d.notify.svc = ns

	wApp := application.New(application.Options{
		Name:        "AdminDescargas",
		Description: "Gestor de descargas multi-hoster.",
		Icon:        appIcon,
		Logger:      baseLog,
		Assets: application.AssetOptions{
			Handler: d.assetHandler(),
		},
		Services: []application.Service{
			application.NewService(ns),
			application.NewService(&Bindings{d: d}),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               singleInstanceID(cfg.DataDir),
			OnSecondInstanceLaunch: d.onSecondInstance,
		},
		OnShutdown: d.shutdown,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})
	d.wailsApp = wApp

	bg := d.initialBackground()
	window := wApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:                      "AdminDescargas",
		Width:                      1200,
		Height:                     780,
		MinWidth:                   720,
		MinHeight:                  480,
		Hidden:                     args.hidden || cfg.Desktop.StartHidden,
		BackgroundColour:           application.RGBA{Red: bg.R, Green: bg.G, Blue: bg.B, Alpha: 255},
		URL:                        "/",
		DefaultContextMenuDisabled: false,
		Windows: application.WindowsWindow{
			Theme: windowsTheme(d.appearance),
		},
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
		},
		Linux: application.LinuxWindow{
			Icon:             appIcon,
			WebviewGpuPolicy: application.WebviewGpuPolicyOnDemand,
		},
	})
	d.window = window

	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if d.closeToTray && !d.quitting.Load() {
			window.Hide()
			e.Cancel()
		}
	})

	wApp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		d.startup()
	})
	wApp.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(e *application.ApplicationEvent) {
		d.onURLOpen(e.Context().URL())
	})
	wApp.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		d.showWindow()
	})

	if err := wApp.Run(); err != nil {
		log.Error("wails", "err", err)
		fmt.Fprintln(os.Stderr, "adesc-desktop:", err)
		return 1
	}
	return d.exitCode
}

// singleInstanceID is stable per data directory, so two profiles (two
// --config with different data dirs) can run side by side.
func singleInstanceID(dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return "admindescargas-desktop-" + hex.EncodeToString(sum[:6])
}

// newLogger logs to cfg.Log.File, or to <data dir>/logs/adesc-desktop.log
// (a GUI process has no visible stderr). ADESC_LOG_STDERR=1 logs to stderr
// instead (development, CI).
func newLogger(cfg *config.Config) *slog.Logger {
	lc := cfg.Log
	if os.Getenv("ADESC_LOG_STDERR") == "1" {
		lc.File = ""
	} else if lc.File == "" {
		lc.File = filepath.Join(cfg.DataDir, "logs", "adesc-desktop.log")
	}
	l, err := app.NewLogger(lc)
	if err != nil {
		lc.File = ""
		if l, err = app.NewLogger(lc); err != nil {
			return slog.New(slog.NewTextHandler(io.Discard, nil))
		}
	}
	return l
}

func windowsTheme(a desktop.Appearance) application.Theme {
	switch a.Tema {
	case desktop.TemaOscuro:
		return application.Dark
	case desktop.TemaClaro:
		return application.Light
	default:
		return application.SystemDefault
	}
}
