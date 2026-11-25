package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Instance describes the running core of a data directory. It is written to
// DataDir/instance.json while the API is served, so the desktop app can find
// a running daemon (and offer to connect) and the CLI can find a desktop
// app listening on a random port. It contains no secrets: clients take the
// token from config.toml.
type Instance struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`
	TLS     bool   `json:"tls"`
	Version string `json:"version"`
}

// ReadInstance returns the instance file of dataDir. A stale file may remain
// after a crash; confirm with apiclient.Client.Health.
func ReadInstance(dataDir string) (*Instance, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, InstanceFileName))
	if err != nil {
		return nil, err
	}
	var in Instance
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("app: parse %s: %w", InstanceFileName, err)
	}
	return &in, nil
}

func writeInstance(dataDir string, in Instance) error {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, InstanceFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("app: write instance file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("app: write instance file: %w", err)
	}
	return nil
}
