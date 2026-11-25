//go:build linux

package main

import (
	"github.com/godbus/dbus/v5"
)

// systemPrefersDark asks the XDG desktop portal for
// org.freedesktop.appearance color-scheme (1 = prefer dark).
func systemPrefersDark() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var v dbus.Variant
	if err := obj.Call("org.freedesktop.portal.Settings.ReadOne", 0, "org.freedesktop.appearance", "color-scheme").Store(&v); err != nil {
		// Older portals only have Read, which wraps the value twice.
		if err := obj.Call("org.freedesktop.portal.Settings.Read", 0, "org.freedesktop.appearance", "color-scheme").Store(&v); err != nil {
			return false
		}
	}
	for i := 0; i < 2; i++ {
		inner, ok := v.Value().(dbus.Variant)
		if !ok {
			break
		}
		v = inner
	}
	n, ok := v.Value().(uint32)
	return ok && n == 1
}
