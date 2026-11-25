// Command adesc is the AdminDescargas daemon and CLI. `adesc daemon` runs
// the core (API + Web UI); every other subcommand is a client of the REST
// API. It is built with CGO_ENABLED=0.
//
// Each subcommand group lives in its own cmd_*.go file and registers itself
// from init(), so features can be added without touching shared files.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := rootCmd.ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
