//go:build darwin

package main

import "fyne.io/systray"

func trayAvailable() bool { return true }

// runTray attaches the status item to the NSApplication run by Wails
// (systray's external-loop mode does not replace the app delegate). The
// start function must run on the main thread.
func runTray(onReady, onExit func()) (stop func()) {
	start, _ := systray.RunWithExternalLoop(onReady, onExit)
	runOnMainThread(start)
	// systray's end/Quit would [NSApp stop:] Wails' run loop; the status
	// item goes away with the process instead.
	return func() {}
}
