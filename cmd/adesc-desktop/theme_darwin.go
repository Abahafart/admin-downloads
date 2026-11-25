//go:build darwin

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// systemPrefersDark reads AppleInterfaceStyle ("Dark" in dark mode; the key
// does not exist in light mode).
func systemPrefersDark() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleInterfaceStyle").Output()
	return err == nil && strings.EqualFold(strings.TrimSpace(string(out)), "dark")
}
