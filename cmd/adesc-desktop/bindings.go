package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"admindescargas/internal/config"
	"admindescargas/internal/desktop"
)

// Bindings is the Wails binding (window.go.main.Bindings.*). The UI's
// transport is HTTP (GET /__adesc/connection and the API); these methods
// mirror the /__adesc/ endpoints for code that prefers the Wails runtime.
// Every method only calls the shared desktopApp logic.
type Bindings struct{ d *desktopApp }

// Connection returns the API base URL and token (integrated core, local
// daemon or remote daemon), waiting up to 30 s for the core to start.
func (b *Bindings) Connection() (desktop.Connection, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return b.d.waitConnection(ctx)
}

// Desktop returns the desktop settings and state.
func (b *Bindings) Desktop() desktopInfo { return b.d.desktopInfo() }

// SetDesktop updates the desktop settings (see desktopSettings).
func (b *Bindings) SetDesktop(s desktopSettings) (desktopInfo, error) {
	if err := b.d.applyDesktopSettings(context.Background(), s); err != nil {
		return desktopInfo{}, err
	}
	return b.d.desktopInfo(), nil
}

// Clipboard returns the clipboard monitor status and settings.
func (b *Bindings) Clipboard() clipboardStatus { return b.d.clipboardStatus() }

// SetClipboard replaces the clipboard monitor settings.
func (b *Bindings) SetClipboard(c config.ClipboardConfig) (clipboardStatus, error) {
	if err := b.d.setClipboardConfig(c); err != nil {
		return clipboardStatus{}, err
	}
	return b.d.clipboardStatus(), nil
}

// SetAppearance updates the window background for the given style/theme;
// esquema is the scheme the UI resolved ("oscuro"/"claro").
func (b *Bindings) SetAppearance(estilo, tema, esquema string) {
	b.d.applyAppearance(desktop.Appearance{Estilo: estilo, Tema: tema}, esquema)
}

// PickDownloadFolder opens the native folder chooser; "" when canceled.
func (b *Bindings) PickDownloadFolder() (string, error) {
	dir, err := b.d.pickDownloadFolder()
	if errors.Is(err, errCancelled) {
		return "", nil
	}
	return dir, err
}

// ImportProxies asks for a proxy list file and imports it; nil when
// canceled.
func (b *Bindings) ImportProxies() (json.RawMessage, error) {
	return b.importFile(importProxies, "", "")
}

// ImportWireGuard asks for a wg-quick .conf and imports it.
func (b *Bindings) ImportWireGuard() (json.RawMessage, error) {
	return b.importFile(importWireGuard, "", "")
}

// ImportCookies asks for a cookies.txt and creates/updates the account of
// hoster.
func (b *Bindings) ImportCookies(hoster, label string) (json.RawMessage, error) {
	return b.importFile(importCookies, hoster, label)
}

func (b *Bindings) importFile(kind importKind, hoster, label string) (json.RawMessage, error) {
	res, err := b.d.importFile(context.Background(), kind, hoster, label)
	if errors.Is(err, errCancelled) {
		return nil, nil
	}
	return res, err
}

// HideWindow hides the window (the app stays in the tray).
func (b *Bindings) HideWindow() { b.d.hideWindow() }

// Quit exits the app (stopping the integrated core).
func (b *Bindings) Quit() { go b.d.quit() }

func (d *desktopApp) hideWindow() {
	if d.ctx != nil {
		wruntime.WindowHide(d.ctx)
	}
}
