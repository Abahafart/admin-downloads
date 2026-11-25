package main

import (
	_ "embed"
	goruntime "runtime"
)

// Icons drawn by tools/genicons (go run tools/genicons/main.go).
var (
	//go:embed build/appicon.png
	appIcon []byte
	//go:embed icons/tray.png
	trayPNG []byte
	//go:embed icons/tray.ico
	trayICO []byte
)

// trayIcon returns the tray icon in the format systray expects: ICO on
// Windows, PNG elsewhere.
func trayIcon() []byte {
	if goruntime.GOOS == "windows" {
		return trayICO
	}
	return trayPNG
}
