package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"admindescargas/internal/api"
	"admindescargas/internal/apiclient"
	"admindescargas/internal/rotation"
)

// Network / rotation commands (clients of /api/v1/egress, /rotation and /ip).
func init() {
	egress := &cobra.Command{
		Use:   "egress",
		Short: "Gestiona las salidas de red (proxies, Tor, WireGuard, router, VPN)",
	}
	egress.AddCommand(newEgressLs(), newEgressImport(), newEgressTest(), newEgressRotate())
	rootCmd.AddCommand(egress, newIPCmd())
}

func printEgresses(cmd *cobra.Command, list []rotation.EgressStatus) error {
	if globalFlags.jsonOut {
		return printJSON(cmd, list)
	}
	tw := newTable(cmd)
	fmt.Fprintln(tw, "ID\tTIPO\tÁMBITO\tSANA\tIP PÚBLICA\tEN USO\tAGOTADA PARA\tETIQUETA / ERROR")
	for _, e := range list {
		var exhausted []string
		for h, until := range e.Exhausted {
			if left := time.Until(until); left > 0 {
				exhausted = append(exhausted, fmt.Sprintf("%s (%s)", h, humanETA(int64(left.Seconds()))))
			}
		}
		info := e.Label
		if e.LastError != "" {
			info = strings.TrimSpace(info + " " + truncate(e.LastError, 60))
		}
		ip := e.PublicIP
		if ip == "" {
			ip = "-"
		}
		ex := strings.Join(exhausted, ", ")
		if ex == "" {
			ex = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", e.ID, e.Kind, e.Scope, yesNo(e.Healthy), ip, e.InUse, ex, info)
	}
	return tw.Flush()
}

func newEgressLs() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Lista las salidas, su estado e IP pública",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				list, err := c.Egresses(ctx)
				if err != nil {
					return err
				}
				return printEgresses(cmd, list)
			})
		},
	}
}

func newEgressImport() *cobra.Command {
	var name string
	var wg bool
	cmd := &cobra.Command{
		Use:   "import <archivo|->",
		Short: "Importa proxies (scheme://user:pass@host:port) o un .conf de WireGuard",
		Long: "Importa una lista de proxies (una por línea: http://, https://, socks5://, socks5h://)\n" +
			"o un archivo .conf de wg-quick (Mullvad, Proton...). Los archivos .conf se detectan\n" +
			"por la extensión; con stdin usa --wireguard.",
		Example: "  adesc egress import proxies.txt\n  adesc egress import mullvad-se1.conf\n  cat wg.conf | adesc egress import - --wireguard --name proton-nl",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			text, err := readAllInput(cmd, path)
			if err != nil {
				return err
			}
			isWG := wg || strings.EqualFold(filepath.Ext(path), ".conf")
			if isWG && name == "" {
				if path == "-" {
					return errors.New("indica --name para un .conf leído de stdin")
				}
				name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var res api.ImportResult
				if isWG {
					res, err = c.ImportWireGuard(ctx, name, text)
				} else {
					res, err = c.ImportProxies(ctx, text)
				}
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, res)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Añadidas: %d, omitidas: %d.\n", res.Added, res.Skipped)
				for _, e := range res.Errors {
					if e.Line > 0 {
						fmt.Fprintf(cmd.ErrOrStderr(), "  línea %d: %s\n", e.Line, e.Message)
					} else {
						fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", e.Message)
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "nombre del túnel WireGuard (por defecto, el del archivo)")
	cmd.Flags().BoolVar(&wg, "wireguard", false, "tratar la entrada como un .conf de WireGuard")
	return cmd
}

func newEgressTest() *cobra.Command {
	return &cobra.Command{
		Use:   "test [id]...",
		Short: "Comprueba las salidas y su IP pública (sin ids: todas)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				ids := args
				if len(ids) == 0 {
					list, err := c.Egresses(ctx)
					if err != nil {
						return err
					}
					for _, e := range list {
						ids = append(ids, e.ID)
					}
				}
				var out []rotation.EgressStatus
				var errs []error
				for _, id := range ids {
					st, err := c.TestEgress(ctx, id)
					if err != nil {
						errs = append(errs, fmt.Errorf("%s: %w", id, friendly(err)))
						continue
					}
					out = append(out, st)
				}
				if err := printEgresses(cmd, out); err != nil {
					return err
				}
				return errors.Join(errs...)
			})
		},
	}
}

func newEgressRotate() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate [id]",
		Short: "Fuerza una rotación (sin id: rotación global con el router o la VPN)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				return report(cmd, c.Rotate(ctx, id), "Rotación hecha.")
			})
		},
	}
}

func newIPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ip",
		Short: "Muestra la IP pública de cada salida",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				list, err := c.PublicIPs(ctx)
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, list)
				}
				tw := newTable(cmd)
				fmt.Fprintln(tw, "SALIDA\tTIPO\tIP PÚBLICA")
				for _, e := range list {
					ip := e.PublicIP
					if e.Error != "" {
						ip = "error: " + truncate(e.Error, 70)
					} else if ip == "" {
						ip = "-"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\n", e.ID, e.Kind, ip)
				}
				return tw.Flush()
			})
		},
	}
}
