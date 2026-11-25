//go:build windows

package desktop

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Enabled reports whether the Run value exists.
func (a Autostart) Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(AutostartName)
	return err == nil
}

// Set enables or disables the Run value.
func (a Autostart) Set(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(AutostartName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(AutostartName, quoteArgs(a.Exec))
}

// quoteArgs quotes each argument for the Run value (CommandLineToArgvW
// rules for the simple cases: paths with spaces, no embedded quotes).
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, s := range args {
		if s == "" || strings.ContainsAny(s, " \t\"") {
			s = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
		}
		q[i] = s
	}
	return strings.Join(q, " ")
}
