package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"admindescargas/internal/buildinfo"
	"admindescargas/internal/config"
	"admindescargas/internal/desktop"
)

// errCancelled is returned when the user closes a native dialog.
var errCancelled = errors.New("cancelado")

// maxImportFile bounds files read for imports (proxy lists, .conf,
// cookies.txt) and drag & drop.
const maxImportFile = 4 << 20

// askYesNo shows a native question dialog.
func (d *desktopApp) askYesNo(title, msg string) bool {
	if d.wailsApp == nil {
		return false
	}
	ch := make(chan bool, 1)
	q := d.wailsApp.Dialog.Question().SetTitle(title).SetMessage(msg)
	if d.window != nil {
		q.AttachToWindow(d.window)
	}
	q.AddButton("Sí").SetAsDefault().OnClick(func() {
		select {
		case ch <- true:
		default:
		}
	})
	q.AddButton("No").SetAsCancel().OnClick(func() {
		select {
		case ch <- false:
		default:
		}
	})
	q.Show()
	select {
	case yes := <-ch:
		return yes
	case <-time.After(5 * time.Minute):
		return false
	}
}

func (d *desktopApp) showError(title, msg string) {
	if d.wailsApp == nil {
		return
	}
	errDlg := d.wailsApp.Dialog.Error().SetTitle(title).SetMessage(msg)
	if d.window != nil {
		errDlg.AttachToWindow(d.window)
	}
	errDlg.Show()
}

// pickDownloadFolder opens the native folder chooser at the current
// download folder. The UI saves the result through PUT /api/v1/settings.
func (d *desktopApp) pickDownloadFolder() (string, error) {
	if d.wailsApp == nil {
		return "", errors.New("la aplicación no está lista")
	}
	dlg := d.wailsApp.Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("Carpeta de descargas").
		SetDirectory(d.config().DownloadDir)
	if d.window != nil {
		dlg.AttachToWindow(d.window)
	}
	dir, err := dlg.PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", errCancelled
	}
	return dir, nil
}

// importKind is one of the native "import file" actions.
type importKind string

const (
	importProxies   importKind = "proxies"
	importWireGuard importKind = "wireguard"
	importCookies   importKind = "cookies"
)

// importFile asks for a file and posts it to the matching API endpoint.
// hoster and label apply to cookies. It returns the API's JSON answer.
func (d *desktopApp) importFile(ctx context.Context, kind importKind, hoster, label string) (json.RawMessage, error) {
	if d.wailsApp == nil {
		return nil, errors.New("la aplicación no está lista")
	}
	dlg := d.wailsApp.Dialog.OpenFile().
		CanChooseFiles(true).
		CanChooseDirectories(false)
	if d.window != nil {
		dlg.AttachToWindow(d.window)
	}
	switch kind {
	case importProxies:
		dlg.SetTitle("Importar lista de proxies").
			AddFilter("Listas de proxies (*.txt, *.list)", "*.txt;*.list").
			AddFilter("Todos los archivos", "*")
	case importWireGuard:
		dlg.SetTitle("Importar configuración de WireGuard").
			AddFilter("WireGuard (*.conf)", "*.conf").
			AddFilter("Todos los archivos", "*")
	case importCookies:
		if strings.TrimSpace(hoster) == "" {
			return nil, errors.New("falta el hoster de las cookies")
		}
		dlg.SetTitle("Importar cookies.txt").
			AddFilter("Cookies (*.txt, *.json)", "*.txt;*.json").
			AddFilter("Todos los archivos", "*")
	default:
		return nil, fmt.Errorf("importación desconocida %q", kind)
	}
	path, err := dlg.PromptForSingleSelection()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errCancelled
	}
	text, err := readSmallFile(path)
	if err != nil {
		return nil, err
	}
	return d.postImport(ctx, kind, filepath.Base(path), text, hoster, label)
}

func (d *desktopApp) postImport(ctx context.Context, kind importKind, name, text, hoster, label string) (json.RawMessage, error) {
	if _, err := d.waitConnection(ctx); err != nil {
		return nil, err
	}
	api := d.currentAPI()
	if api == nil {
		return nil, errors.New("sin conexión con el núcleo")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	switch kind {
	case importProxies:
		return api.ImportProxies(ctx, text)
	case importWireGuard:
		return api.ImportWireGuard(ctx, strings.TrimSuffix(name, filepath.Ext(name)), text)
	case importCookies:
		return api.ImportCookies(ctx, hoster, label, text)
	}
	return nil, fmt.Errorf("importación desconocida %q", kind)
}

func readSmallFile(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // G703: path is selected via native file dialog
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxImportFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxImportFile {
		return "", fmt.Errorf("%s es demasiado grande (máximo %d MiB)", filepath.Base(path), maxImportFile>>20)
	}
	return string(b), nil
}

// droppedFile is a file dropped on the window (read by the injected
// script; only its name and text reach Go).
type droppedFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// handleDrop processes a drag & drop on the window: free text or links go
// to the LinkGrabber, a WireGuard .conf is imported, any other text file
// is scanned for links.
func (d *desktopApp) handleDrop(ctx context.Context, text string, files []droppedFile) error {
	var errs []error
	var texts []string
	if strings.TrimSpace(text) != "" {
		texts = append(texts, text)
	}
	for _, f := range files {
		if len(f.Text) > maxImportFile {
			errs = append(errs, fmt.Errorf("%s es demasiado grande", f.Name))
			continue
		}
		if strings.EqualFold(filepath.Ext(f.Name), ".conf") && strings.Contains(f.Text, "[Interface]") {
			if _, err := d.postImport(ctx, importWireGuard, f.Name, f.Text, "", ""); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", f.Name, err))
			} else {
				d.navigate("red")
			}
			continue
		}
		texts = append(texts, f.Text)
	}
	if len(texts) > 0 {
		d.addLinks(nil, strings.Join(texts, "\n"), "desktop")
	}
	return errors.Join(errs...)
}

// desktopInfo is the body of GET /__adesc/desktop.
type desktopInfo struct {
	Version        string `json:"version"`
	Platform       string `json:"platform"`
	Mode           string `json:"mode"`       // configured: integrated | remote
	Connection     string `json:"connection"` // current: integrated | local-daemon | remote | ""
	ConnectionErr  string `json:"connection_error,omitempty"`
	RemoteURL      string `json:"remote_url"`
	HasRemoteToken bool   `json:"has_remote_token"`
	CloseToTray    bool   `json:"close_to_tray"`
	TrayAvailable  bool   `json:"tray_available"`
	Autostart      bool   `json:"autostart"`
	Notifications  bool   `json:"notifications"`
	StartHidden    bool   `json:"start_hidden"`
}

func (d *desktopApp) desktopInfo() desktopInfo {
	cfg := d.config()
	d.mu.Lock()
	conn, connErr := d.conn, d.connErr
	d.mu.Unlock()
	info := desktopInfo{
		Version:        buildinfo.Version,
		Platform:       goruntime.GOOS,
		Mode:           cfg.Desktop.Mode,
		Connection:     string(conn.Mode),
		RemoteURL:      cfg.Desktop.RemoteURL,
		HasRemoteToken: cfg.Desktop.RemoteToken != "",
		CloseToTray:    cfg.Desktop.CloseToTray,
		TrayAvailable:  trayAvailable(),
		Autostart:      d.autostart().Enabled(),
		Notifications:  cfg.Desktop.Notifications,
		StartHidden:    cfg.Desktop.StartHidden,
	}
	if connErr != nil {
		info.ConnectionErr = connErr.Error()
	}
	return info
}

// desktopSettings is the body of PUT /__adesc/desktop (every field
// optional). Changing mode, remote_url or remote_token reconnects and
// reloads the UI; close_to_tray applies on the next start.
type desktopSettings struct {
	Mode          *string `json:"mode,omitempty"`
	RemoteURL     *string `json:"remote_url,omitempty"`
	RemoteToken   *string `json:"remote_token,omitempty"`
	CloseToTray   *bool   `json:"close_to_tray,omitempty"`
	Autostart     *bool   `json:"autostart,omitempty"`
	Notifications *bool   `json:"notifications,omitempty"`
	StartHidden   *bool   `json:"start_hidden,omitempty"`
}

func (d *desktopApp) applyDesktopSettings(ctx context.Context, s desktopSettings) error {
	cur := d.config().Desktop
	next := cur
	if s.Mode != nil {
		next.Mode = *s.Mode
	}
	if s.RemoteURL != nil {
		next.RemoteURL = strings.TrimSpace(*s.RemoteURL)
		if next.RemoteURL != "" {
			u, err := desktop.NormalizeRemoteURL(next.RemoteURL)
			if err != nil {
				return fmt.Errorf("remote_url: %w", err)
			}
			next.RemoteURL = u
		}
	}
	if s.RemoteToken != nil {
		next.RemoteToken = strings.TrimSpace(*s.RemoteToken)
	}
	if s.CloseToTray != nil {
		next.CloseToTray = *s.CloseToTray
	}
	if s.Notifications != nil {
		next.Notifications = *s.Notifications
	}
	if s.StartHidden != nil {
		next.StartHidden = *s.StartHidden
	}
	switch next.Mode {
	case config.DesktopIntegrated:
	case config.DesktopRemote:
		if next.RemoteURL == "" {
			return errors.New("el modo remoto necesita remote_url")
		}
		// Refuse to switch to a server that does not answer or rejects the
		// token: the user would be left without a working UI.
		if next.RemoteURL != cur.RemoteURL || next.RemoteToken != cur.RemoteToken || cur.Mode != next.Mode {
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := desktop.CheckServer(cctx, next.RemoteURL, next.RemoteToken)
			cancel()
			if err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("mode %q: debe ser integrated o remote", next.Mode)
	}
	if s.Autostart != nil {
		if err := d.autostart().Set(*s.Autostart); err != nil {
			return fmt.Errorf("autoarranque: %w", err)
		}
		next.Autostart = *s.Autostart
	}
	if err := d.updateConfig(func(c *config.Config) { c.Desktop = next }); err != nil {
		return err
	}
	if next.Mode != cur.Mode || next.Mode == config.DesktopRemote && (next.RemoteURL != cur.RemoteURL || next.RemoteToken != cur.RemoteToken) {
		go d.reconnect() //nolint:gosec // G118: the reconnect must outlive the settings request
	}
	return nil
}

func (d *desktopApp) autostart() desktop.Autostart {
	exe, err := os.Executable()
	if err != nil {
		exe = "adesc-desktop"
	}
	if goruntime.GOOS == "darwin" {
		// Inside a .app bundle, start the bundle (keeps the Dock icon and
		// the notification identity).
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
			return desktop.Autostart{Exec: []string{"/usr/bin/open", "-a", exe[:i+len(".app")], "--args", desktop.HiddenFlag}}
		}
	}
	args := []string{exe, desktop.HiddenFlag}
	if d.args.configPath != "" {
		args = append(args, "--config", d.args.configPath)
	}
	return desktop.Autostart{Exec: args}
}
