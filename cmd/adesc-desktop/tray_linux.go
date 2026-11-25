//go:build linux

package main

import (
	"sync"

	"github.com/godbus/dbus/v5"
)

// trayAvailable reports whether a StatusNotifierItem host is running (KDE,
// XFCE, Cinnamon, GNOME with the AppIndicator extension...). Without one
// the window is not hidden on close, so the app cannot get lost.
var trayAvailable = sync.OnceValue(func() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
})
