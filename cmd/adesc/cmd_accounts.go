package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"admindescargas/internal/accounts"
	"admindescargas/internal/api"
	"admindescargas/internal/apiclient"
)

// defaultKinds is the usual credential type per hoster (PLAN.md section 5).
var defaultKinds = map[string]accounts.Kind{
	"mega":       accounts.KindPassword,
	"gdrive":     accounts.KindOAuth,
	"google":     accounts.KindOAuth,
	"terabox":    accounts.KindCookie,
	"mediafire":  accounts.KindCookie,
	"pixeldrain": accounts.KindAPIKey,
	"onefichier": accounts.KindAPIKey,
}

var statusLabels = map[accounts.Status]string{
	accounts.StatusUnknown:  "sin comprobar",
	accounts.StatusOK:       "ok",
	accounts.StatusInvalid:  "credenciales no válidas",
	accounts.StatusExpired:  "caducada",
	accounts.StatusQuota:    "sin cuota",
	accounts.StatusNeeds2FA: "requiere código 2FA",
	accounts.StatusError:    "error",
	accounts.StatusDisabled: "deshabilitada",
	accounts.StatusChecking: "comprobando",
}

// Account commands (clients of /api/v1/accounts).
func init() {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "Gestiona cuentas de hosters (MEGA, Google, TeraBox...)",
	}
	cmd.AddCommand(newAccountsLs(), newAccountsAdd(), newAccountsRm(), newAccountsCheck(), newAccountsImportCookies(), newAccounts2FA(), newAccountsOAuthComplete())
	rootCmd.AddCommand(cmd)
}

func printAccounts(cmd *cobra.Command, list []accounts.Account) error {
	if globalFlags.jsonOut {
		return printJSON(cmd, list)
	}
	if len(list) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No hay cuentas.")
		return nil
	}
	tw := newTable(cmd)
	fmt.Fprintln(tw, "ID\tHOSTER\tTIPO\tETIQUETA\tUSUARIO\tESTADO\tPREMIUM\tCUOTA LIBRE\tACTIVA")
	for _, a := range list {
		st := statusLabels[a.Status]
		if st == "" {
			st = string(a.Status)
		}
		if a.StatusMsg != "" {
			st += ": " + truncate(a.StatusMsg, 40)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.Hoster, a.Kind, a.Label, a.Username, st,
			yesNo(a.Premium), humanBytes(a.QuotaLeft()), yesNo(a.Enabled))
	}
	return tw.Flush()
}

func newAccountsLs() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Lista las cuentas y su estado",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				list, err := c.Accounts(ctx)
				if err != nil {
					return err
				}
				return printAccounts(cmd, list)
			})
		},
	}
}

func newAccountsAdd() *cobra.Command {
	var kind, label, user, cookiesFile string
	var allowRotation, disabled bool
	cmd := &cobra.Command{
		Use:   "add <hoster>",
		Short: "Añade una cuenta (contraseña, cookies, OAuth o API key)",
		Long: "Añade una cuenta. El tipo se deduce del hoster (mega: password, gdrive: oauth,\n" +
			"terabox/mediafire: cookie, pixeldrain/onefichier: apikey) o se indica con --kind.\n\n" +
			"La contraseña o la API key se leen de la entrada estándar (nunca como argumento,\n" +
			"para que no queden en el historial). Para OAuth se muestra la URL que hay que abrir\n" +
			"en el navegador.",
		Example: "  adesc accounts add mega --user yo@example.com\n" +
			"  printf '%s\\n' \"$MEGA_PASS\" | adesc accounts add mega --user yo@example.com\n" +
			"  adesc accounts add terabox --cookies-file cookies.txt\n" +
			"  adesc accounts add gdrive --label personal",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hoster := strings.ToLower(args[0])
			k := accounts.Kind(kind)
			if kind == "" {
				var ok bool
				if k, ok = defaultKinds[hoster]; !ok {
					return fmt.Errorf("indica --kind (password, cookie, oauth o apikey) para %q", hoster)
				}
			}
			if !k.Valid() {
				return fmt.Errorf("--kind %q no válido", kind)
			}
			enabled := !disabled
			req := api.AccountCreateRequest{Hoster: hoster, Label: label, Kind: string(k), Username: user,
				Enabled: &enabled, AllowRotation: allowRotation}
			switch k {
			case accounts.KindPassword:
				if user == "" {
					return errors.New("indica el usuario con --user")
				}
				pw, err := promptLine(cmd, "Contraseña: ")
				if err != nil {
					return err
				}
				req.Password = pw
			case accounts.KindAPIKey:
				key, err := promptLine(cmd, "API key: ")
				if err != nil {
					return err
				}
				req.APIKey = strings.TrimSpace(key)
			case accounts.KindCookie:
				if cookiesFile == "" {
					return errors.New("indica el archivo de cookies con --cookies-file (o \"-\" para stdin)")
				}
				text, err := readAllInput(cmd, cookiesFile)
				if err != nil {
					return err
				}
				req.Cookies = text
			}
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				a, err := c.CreateAccount(ctx, req)
				if err != nil {
					return err
				}
				if k == accounts.KindOAuth {
					u, err := c.GoogleOAuthStart(ctx, a.ID)
					if err != nil {
						return fmt.Errorf("cuenta %s creada, pero no se pudo iniciar OAuth: %w", a.ID, err)
					}
					if globalFlags.jsonOut {
						return printJSON(cmd, map[string]any{"account": a, "oauth_url": u})
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Cuenta %s creada. Abre esta URL en el navegador para autorizarla:\n%s\n", a.ID, u)
					return nil
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, a)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cuenta %s creada (%s). Compruébala con `adesc accounts check %s`.\n", a.ID, a.Hoster, a.ID)
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&kind, "kind", "", "tipo de credencial: password, cookie, oauth o apikey")
	f.StringVar(&label, "label", "", "nombre visible de la cuenta")
	f.StringVarP(&user, "user", "u", "", "usuario o e-mail (cuentas con contraseña)")
	f.StringVar(&cookiesFile, "cookies-file", "", "cookies.txt (Netscape) o JSON; \"-\" para stdin")
	f.BoolVar(&allowRotation, "allow-rotation", false, "permitir rotar la IP con esta cuenta (desaconsejado)")
	f.BoolVar(&disabled, "disabled", false, "crearla deshabilitada")
	return cmd
}

func newAccountsRm() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <id>...",
		Aliases: []string{"delete"},
		Short:   "Borra una cuenta y su secreto",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				for _, id := range args {
					if err := c.DeleteAccount(ctx, id); err != nil {
						return fmt.Errorf("%s: %w", id, err)
					}
				}
				return report(cmd, nil, "Hecho.")
			})
		},
	}
}

func newAccountsCheck() *cobra.Command {
	return &cobra.Command{
		Use:   "check [id]...",
		Short: "Comprueba las cuentas y su cuota (sin ids: todas)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				ids := args
				if len(ids) == 0 {
					list, err := c.Accounts(ctx)
					if err != nil {
						return err
					}
					for _, a := range list {
						if a.Enabled {
							ids = append(ids, a.ID)
						}
					}
				}
				var out []accounts.Account
				var errs []error
				for _, id := range ids {
					a, err := c.CheckAccount(ctx, id)
					if err != nil {
						errs = append(errs, fmt.Errorf("%s: %w", id, friendly(err)))
						continue
					}
					out = append(out, a)
				}
				if err := printAccounts(cmd, out); err != nil {
					return err
				}
				return errors.Join(errs...)
			})
		},
	}
}

func newAccountsImportCookies() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:     "import-cookies <hoster> <archivo|->",
		Short:   "Importa un cookies.txt (Netscape) o JSON como cuenta de cookies",
		Example: "  adesc accounts import-cookies terabox ~/Descargas/cookies.txt --label principal",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := readAllInput(cmd, args[1])
			if err != nil {
				return err
			}
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				a, err := c.ImportCookies(ctx, strings.ToLower(args[0]), label, text)
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, a)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cookies importadas en la cuenta %s (%s).\n", a.ID, a.Hoster)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "nombre visible de la cuenta")
	return cmd
}

func newAccounts2FA() *cobra.Command {
	return &cobra.Command{
		Use:   "2fa <id> <código>",
		Short: "Envía el código 2FA de una cuenta que lo pide (MEGA)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				a, err := c.SubmitAccount2FA(ctx, args[0], args[1])
				if err != nil {
					return err
				}
				return printAccounts(cmd, []accounts.Account{a})
			})
		},
	}
}

func newAccountsOAuthComplete() *cobra.Command {
	return &cobra.Command{
		Use:   "oauth-complete <url>",
		Short: "Termina la conexión con Google pegando la dirección que mostró el navegador",
		Long: "Si el navegador no pudo volver al daemon tras autorizar en Google (daemon remoto),\n" +
			"copia la dirección completa de la barra del navegador y pásala aquí.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if err := c.GoogleOAuthComplete(ctx, args[0]); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Cuenta de Google conectada.")
				return nil
			})
		},
	}
}
