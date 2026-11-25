package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"admindescargas/internal/accounts"
	"admindescargas/internal/apiclient"
)

// `adesc secrets ...` manages the secret store (vault) of a running daemon.
func init() {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Gestiona el almacén de credenciales (vault cifrado o keyring)",
		Long: "En equipos sin keyring (servidores) las credenciales de las cuentas se guardan en un\n" +
			"vault cifrado que arranca bloqueado. Desbloquéalo con su contraseña maestra (la primera\n" +
			"vez la eliges) o configura [secrets] vault_password_file en config.toml.",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Muestra el tipo de almacén y si está bloqueado",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
					st, err := c.SecretsStatus(ctx)
					if err != nil {
						return err
					}
					return printStoreStatus(cmd, st)
				})
			},
		},
		&cobra.Command{
			Use:   "unlock",
			Short: "Desbloquea el vault (la contraseña se lee de la entrada estándar)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				pw, err := promptLine(cmd, "Contraseña maestra: ")
				if err != nil {
					return err
				}
				if pw == "" {
					return errors.New("contraseña vacía")
				}
				return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
					st, err := c.UnlockSecrets(ctx, pw)
					if err != nil {
						return err
					}
					return printStoreStatus(cmd, st)
				})
			},
		},
		&cobra.Command{
			Use:   "lock",
			Short: "Bloquea el vault",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
					st, err := c.LockSecrets(ctx)
					if err != nil {
						return err
					}
					return printStoreStatus(cmd, st)
				})
			},
		},
		&cobra.Command{
			Use:   "passwd",
			Short: "Cambia la contraseña maestra del vault (lee la actual y la nueva de la entrada estándar)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				old, err := promptLine(cmd, "Contraseña actual: ")
				if err != nil {
					return err
				}
				nw, err := promptLine(cmd, "Contraseña nueva: ")
				if err != nil {
					return err
				}
				return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
					if err := c.ChangeMasterPassword(ctx, old, nw); err != nil {
						return err
					}
					fmt.Fprintln(cmd.OutOrStdout(), "Contraseña maestra cambiada.")
					return nil
				})
			},
		},
	)
	rootCmd.AddCommand(cmd)
}

func printStoreStatus(cmd *cobra.Command, st accounts.StoreStatus) error {
	if globalFlags.jsonOut {
		return printJSON(cmd, st)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Almacén: %s\n", st.Backend)
	if st.Lockable {
		switch {
		case !st.Initialized:
			fmt.Fprintln(out, "Estado: sin crear (el primer `adesc secrets unlock` elige la contraseña maestra)")
		case st.Locked:
			fmt.Fprintln(out, "Estado: bloqueado")
		default:
			fmt.Fprintln(out, "Estado: desbloqueado")
		}
	}
	if st.Warning != "" {
		fmt.Fprintln(out, "Aviso:", st.Warning)
	}
	return nil
}
