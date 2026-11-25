package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"admindescargas/internal/api"
	"admindescargas/internal/apiclient"
)

// selftest runs the plugins' self-tests through the daemon
// (POST /api/v1/selftest/{hoster}), so they use its egresses.
func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:     "selftest <hoster>...",
		Short:   "Prueba los plugins contra enlaces públicos conocidos",
		Long:    "Ejecuta la prueba de cada plugin contra enlaces públicos conocidos para detectar\ncambios en el hoster. Termina con error si alguna falla.",
		Example: "  adesc selftest mega mediafire",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var results []api.SelfTestResult
				var failed []string
				for _, h := range args {
					res, err := c.SelfTest(ctx, strings.ToLower(h))
					if err != nil {
						res = api.SelfTestResult{Hoster: h, Error: friendly(err).Error()}
					}
					results = append(results, res)
					if !res.OK {
						failed = append(failed, h)
					}
					if !globalFlags.jsonOut {
						d := (time.Duration(res.DurationMS) * time.Millisecond).Round(10 * time.Millisecond)
						if res.OK {
							fmt.Fprintf(cmd.OutOrStdout(), "%s: OK (%s)\n", res.Hoster, d)
						} else {
							fmt.Fprintf(cmd.OutOrStdout(), "%s: FALLO: %s\n", res.Hoster, res.Error)
						}
					}
				}
				if globalFlags.jsonOut {
					if err := printJSON(cmd, results); err != nil {
						return err
					}
				}
				if len(failed) > 0 {
					return fmt.Errorf("fallaron: %s", strings.Join(failed, ", "))
				}
				return nil
			})
		},
	})
}
