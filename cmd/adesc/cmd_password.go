package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/spf13/cobra"

	"admindescargas/internal/api"
	"admindescargas/internal/config"
)

// `adesc password` sets the Web UI password (server.password_hash) in
// config.toml. It edits the local file directly, never through the API.
func init() {
	var clear bool
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Establece la contraseña de la Web UI",
		Long: "Guarda el hash Argon2id de la contraseña de la Web UI en config.toml\n" +
			"(server.password_hash). La contraseña se lee de la entrada estándar; para que no\n" +
			"se vea en pantalla, pásala por una tubería. Reinicia el daemon para aplicarla.\n\n" +
			"Sin contraseña, la Web UI solo se puede abrir desde este equipo.",
		Example: "  adesc password\n  printf '%s\\n' \"$PASS\" | adesc password\n  adesc password --clear",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := globalFlags.configPath
			if path == "" {
				p, err := config.DefaultPath()
				if err != nil {
					return err
				}
				path = p
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			if clear {
				host, _, _ := net.SplitHostPort(cfg.Server.Listen)
				if !config.IsLoopbackHost(host) {
					return fmt.Errorf("server.listen es %s: la contraseña es obligatoria fuera de 127.0.0.1", cfg.Server.Listen)
				}
				cfg.Server.PasswordHash = ""
			} else {
				pw, err := promptLine(cmd, "Nueva contraseña de la Web UI: ")
				if err != nil {
					return err
				}
				if len([]rune(pw)) < 8 {
					return errors.New("la contraseña debe tener al menos 8 caracteres")
				}
				if cfg.Server.PasswordHash, err = api.HashPassword(pw); err != nil {
					return err
				}
			}
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			if clear {
				fmt.Fprintln(cmd.OutOrStdout(), "Contraseña eliminada. Reinicia el daemon para aplicarlo.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Contraseña guardada. Reinicia el daemon para aplicarla.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "quitar la contraseña (solo si el daemon escucha en loopback)")
	rootCmd.AddCommand(cmd)
}
