package main

import (
	"fmt"
	goruntime "runtime"
	"sync"

	"fyne.io/systray"

	"admindescargas/internal/core"
	"admindescargas/internal/desktop"
)

// trayUI is the system tray icon (fyne.io/systray): total speed, pause /
// resume all, open the window, clipboard monitor toggle and quit. The
// event loop is started per platform (tray_*.go).
type trayUI struct {
	d *desktopApp

	mu       sync.Mutex
	ready    bool
	stopFn   func()
	status   string
	stats    core.Stats
	clipOK   bool
	clipOn   bool
	mSpeed   *systray.MenuItem
	mOpen    *systray.MenuItem
	mPause   *systray.MenuItem
	mResume  *systray.MenuItem
	mClip    *systray.MenuItem
	mQuit    *systray.MenuItem
	quitDone chan struct{}
	quitOnce sync.Once
}

func (t *trayUI) start() {
	t.quitDone = make(chan struct{})
	t.stopFn = runTray(t.onReady, func() { t.quitOnce.Do(func() { close(t.quitDone) }) })
}

func (t *trayUI) stop() {
	t.mu.Lock()
	stop := t.stopFn
	t.stopFn = nil
	t.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (t *trayUI) onReady() {
	systray.SetIcon(trayIcon())
	systray.SetTooltip("AdminDescargas")
	t.mu.Lock()
	t.mSpeed = systray.AddMenuItem("↓ 0 B/s", "Velocidad total")
	t.mSpeed.Disable()
	systray.AddSeparator()
	t.mOpen = systray.AddMenuItem("Abrir AdminDescargas", "Mostrar la ventana")
	t.mPause = systray.AddMenuItem("Pausar todo", "Pausar todas las descargas")
	t.mResume = systray.AddMenuItem("Reanudar todo", "Reanudar las descargas pausadas")
	t.mClip = systray.AddMenuItemCheckbox("Monitor del portapapeles", "Detectar enlaces copiados", t.clipOn)
	systray.AddSeparator()
	t.mQuit = systray.AddMenuItem("Salir", "Cerrar AdminDescargas y detener las descargas")
	t.ready = true
	t.mu.Unlock()
	if goruntime.GOOS != "darwin" {
		// macOS convention: a click opens the menu.
		systray.SetOnTapped(t.d.showWindow)
	}
	t.refresh()
	go t.loop()
}

func (t *trayUI) loop() {
	for {
		select {
		case <-t.quitDone:
			return
		case <-t.mOpen.ClickedCh:
			t.d.showWindow()
		case <-t.mPause.ClickedCh:
			t.d.pauseAll()
		case <-t.mResume.ClickedCh:
			t.d.resumeAll()
		case <-t.mClip.ClickedCh:
			on := !t.mClip.Checked()
			if err := t.d.setClipboardEnabled(on); err != nil {
				t.d.log.Warn("clipboard toggle", "err", err)
			}
		case <-t.mQuit.ClickedCh:
			t.d.quit()
			return
		}
	}
}

func (t *trayUI) setStats(s core.Stats) {
	t.mu.Lock()
	t.stats = s
	t.mu.Unlock()
	t.refresh()
}

// setStatus shows a connection problem ("Sin conexión") instead of the
// speed; "" clears it.
func (t *trayUI) setStatus(s string) {
	t.mu.Lock()
	t.status = s
	t.mu.Unlock()
	t.refresh()
}

func (t *trayUI) setClipboard(available, on bool) {
	t.mu.Lock()
	t.clipOK, t.clipOn = available, on
	t.mu.Unlock()
	t.refresh()
}

func (t *trayUI) refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
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
	t.mSpeed.SetTitle(line)
	systray.SetTooltip(tip)
	if goruntime.GOOS == "darwin" {
		// The menu bar shows the speed next to the icon while downloading.
		if t.stats.Speed > 0 && t.status == "" {
			systray.SetTitle(speed)
		} else {
			systray.SetTitle("")
		}
	}
	if t.stats.Paused {
		t.mPause.Disable()
		t.mResume.Enable()
	} else {
		t.mPause.Enable()
		t.mResume.Enable()
	}
	if t.clipOK {
		t.mClip.Enable()
	} else {
		t.mClip.Disable()
	}
	if t.clipOn {
		t.mClip.Check()
	} else {
		t.mClip.Uncheck()
	}
}
