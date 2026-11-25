package main

import (
	"testing"
)

func TestCommandTree(t *testing.T) {
	for _, path := range [][]string{
		{"daemon"}, {"add"}, {"ls"}, {"pause"}, {"resume"}, {"restart"}, {"retry"},
		{"accounts", "add"}, {"egress", "import"}, {"egress", "test"}, {"ip"},
		{"selftest"}, {"service", "install"}, {"version"},
		{"rm"}, {"grabber", "confirm"}, {"accounts", "ls"}, {"accounts", "rm"}, {"accounts", "check"},
		{"accounts", "import-cookies"}, {"accounts", "2fa"}, {"egress", "ls"}, {"egress", "rotate"},
		{"service", "uninstall"}, {"service", "start"}, {"service", "stop"}, {"service", "status"}, {"password"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Errorf("command %v not registered (%v)", path, err)
		}
	}
}
