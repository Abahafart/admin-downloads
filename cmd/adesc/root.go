package main

import (
	"github.com/spf13/cobra"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/buildinfo"
	"admindescargas/internal/config"
)

// Global flags shared by every subcommand.
var globalFlags struct {
	configPath string // --config
	url        string // --url: daemon base URL (overrides server.listen)
	token      string // --token: API token (overrides server.token)
	jsonOut    bool   // --json: machine-readable output
}

var rootCmd = &cobra.Command{
	Use:           "adesc",
	Short:         "AdminDescargas: gestor de descargas multi-hoster",
	Long:          "AdminDescargas descarga de MEGA, MediaFire, Google Drive, TeraBox y otros hosters,\ncon reanudación, carpetas completas y rotación de IP.\n\n`adesc daemon` arranca el núcleo; el resto de comandos hablan con su API.",
	Version:       buildinfo.String(),
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&globalFlags.configPath, "config", "", "ruta de config.toml (por defecto: <config del usuario>/admindescargas/config.toml)")
	pf.StringVar(&globalFlags.url, "url", "", "URL del daemon (por defecto: server.listen de la config)")
	pf.StringVar(&globalFlags.token, "token", "", "token de la API (por defecto: server.token de la config)")
	pf.BoolVar(&globalFlags.jsonOut, "json", false, "salida en JSON")
}

// loadConfig loads config.toml honoring --config (creating it on first run).
func loadConfig() (*config.Config, error) {
	return config.Load(globalFlags.configPath)
}

// newClient returns an API client honoring --url / --token, falling back
// to the local config.
func newClient() (*apiclient.Client, error) {
	if globalFlags.url != "" && globalFlags.token != "" {
		return apiclient.New(globalFlags.url, globalFlags.token)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	c, err := apiclient.FromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if globalFlags.url != "" {
		if c, err = apiclient.New(globalFlags.url, c.Token); err != nil {
			return nil, err
		}
	}
	if globalFlags.token != "" {
		c.Token = globalFlags.token
	}
	return c, nil
}
