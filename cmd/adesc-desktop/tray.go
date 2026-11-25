package main

import (
	"fmt"
	goruntime "runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"admindescargas/internal/core"
	"admindescargas/internal/desktop"
)

// trayUI is the system tray icon (Wails v3 SystemTray): total speed, pause /
// resume all, open the window, clipboard monitor toggle and quit.
type trayUI struct {
	d *desktopApp

	mu      sync.Mutex
	ready   bool
	tray    *application.SystemTray
	menu    *application.Menu
	status  string
	stats   core.Stats
	clipOK  bool
	clipOn  bool
	mSpeed  *application.MenuItem
	mOpen   *application.MenuItem
	mPause  *application.MenuItem
	mResume *application.MenuItem
	mClip   *application.MenuItem
	mQuit   *application.MenuItem
}

func (t *trayUI) start() {
	if t.d.wailsApp == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready {
		return
	}

	t.tray = t.d.wailsApp.SystemTray.New()
	t.tray.SetIcon(trayIcon())
	t.tray.SetTooltip("AdminDescargas")
	if goruntime.GOOS != "darwin" {
		t.tray.OnClick(func() { t.d.showWindow() })
	}

	t.menu = t.d.wailsApp.NewMenu()
	t.mSpeed = t.menu.Add("↓ 0 B/s")
	t.mSpeed.SetEnabled(false)
	t.menu.AddSeparator()
	t.mOpen = t.menu.Add("Abrir AdminDescargas").OnClick(func(ctx *application.Context) {
		t.d.showWindow()
	})
	t.mPause = t.menu.Add("Pausar todo").OnClick(func(ctx *application.Context) {
		t.d.pauseAll()
	})
	t.mResume = t.menu.Add("Reanudar todo").OnClick(func(ctx *application.Context) {
		t.d.resumeAll()
	})
	t.mClip = t.menu.AddCheckbox("Monitor del portapapeles", t.clipOn).OnClick(func(ctx *application.Context) {
		on := ctx.ClickedMenuItem().Checked()
		if err := t.d.setClipboardEnabled(on); err != nil {
			t.d.log.Warn("clipboard toggle", "err", err)
		}
	})
	t.menu.AddSeparator()
	t.mQuit = t.menu.Add("Salir").OnClick(func(ctx *application.Context) {
		t.d.quit()
	})

	t.tray.SetMenu(t.menu)
	t.ready = true
	t.refreshLocked()
}

func (t *trayUI) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tray != nil {
		t.tray.Destroy()
		t.tray = nil
	}
	t.ready = false
}

func (t *trayUI) setStats(s core.Stats) {
	t.mu.Lock()
	t.stats = s
	t.refreshLocked()
	t.mu.Unlock()
}

// setStatus shows a connection problem ("Sin conexión") instead of the
// speed; "" clears it.
func (t *trayUI) setStatus(s string) {
	t.mu.Lock()
	t.status = s
	t.refreshLocked()
	t.mu.Unlock()
}

func (t *trayUI) setClipboard(available, on bool) {
	t.mu.Lock()
	t.clipOK, t.clipOn = available, on
	t.refreshLocked()
	t.mu.Unlock()
}

func (t *trayUI) refreshLocked() {
	if !t.ready || t.tray == nil {
		return
	}
	speed := desktop.FormatSpeed(t.stats.Speed)
	line := "↓ " + speed
	if t.stats.Active > 0 {
		line += fmt.Sprintf(" · %d activas", t.stats.Active)
	}
	tip := "AdminDescargas · ↓ " + speed
	if t.status != "" {
		line, tip = t.status, "AdminDescargas · "+t.status
	}
	t.mSpeed.SetLabel(line)
	t.tray.SetTooltip(tip)
	if goruntime.GOOS == "darwin" {
		if t.stats.Speed > 0 && t.status == "" {
			t.tray.SetLabel(speed)
		} else {
			t.tray.SetLabel("")
		}
	}
	if t.stats.Paused {
		t.mPause.SetEnabled(false)
		t.mResume.SetEnabled(true)
	} else {
		t.mPause.SetEnabled(true)
		t.mResume.SetEnabled(true)
	}
	t.mClip.SetEnabled(t.clipOK)
	t.mClip.SetChecked(t.clipOn)
}
