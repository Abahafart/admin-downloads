package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"admindescargas/internal/buildinfo"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Muestra la versión",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "adesc", buildinfo.String())
		},
	})
}
