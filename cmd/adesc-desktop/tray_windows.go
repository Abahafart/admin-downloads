//go:build windows

package main

import (
	"runtime"

	"fyne.io/systray"
)

func trayAvailable() bool { return true }

// runTray runs the tray's own message loop on a dedicated OS thread (the
// hidden tray window must be serviced by the thread that created it).
func runTray(onReady, onExit func()) (stop func()) {
	go func() {
		runtime.LockOSThread()
		systray.Run(onReady, onExit)
	}()
	return systray.Quit
}
