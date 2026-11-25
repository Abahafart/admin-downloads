//go:build !windows

package desktop

import "github.com/emersion/go-autostart"

func (a Autostart) app() *autostart.App {
	return &autostart.App{Name: AutostartName, DisplayName: "AdminDescargas", Exec: a.Exec}
}

// Enabled reports whether the login item exists.
func (a Autostart) Enabled() bool { return a.app().IsEnabled() }

// Set enables or disables the login item.
func (a Autostart) Set(on bool) error {
	app := a.app()
	if !on {
		if !app.IsEnabled() {
			return nil
		}
		return app.Disable()
	}
	return app.Enable()
}
