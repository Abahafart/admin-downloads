// Package desktop holds the platform-neutral logic of adesc-desktop (the
// Wails shell lives in cmd/adesc-desktop, its own Go module): connecting to
// a core (integrated, local daemon, remote), the REST calls of the tray and
// the clipboard monitor, the SSE event reader, desktop notifications from
// events, adesc:// links, the window background per theme and autostart.
//
// Everything here is pure Go and builds with CGO_ENABLED=0, so the daemon
// build (`go build ./...`) and its tests keep covering it. The clipboard
// monitor is in the clipboard subpackage.
package desktop
