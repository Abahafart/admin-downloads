package desktop

// AutostartName identifies the login item (desktop file, LaunchAgent label
// suffix, registry value).
const AutostartName = "admindescargas"

// HiddenFlag makes adesc-desktop start in the tray (used by autostart).
const HiddenFlag = "--hidden"

// Autostart registers adesc-desktop to start at login (PLAN.md section 8).
// On Linux it writes ~/.config/autostart/admindescargas.desktop and on
// macOS a LaunchAgent (emersion/go-autostart); on Windows it uses the
// HKCU\...\CurrentVersion\Run registry value (no cgo).
type Autostart struct {
	// Exec is the command line, e.g. [os.Executable(), "--hidden"].
	Exec []string
}
