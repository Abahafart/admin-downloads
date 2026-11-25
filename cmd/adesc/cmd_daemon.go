package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"

	"admindescargas/internal/app"
	"admindescargas/internal/config"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "daemon",
		Short: "Arranca el núcleo: cola de descargas, API REST/SSE y Web UI",
		Args:  cobra.NoArgs,
		RunE:  runDaemon,
	})
}

func runDaemon(cmd *cobra.Command, _ []string) error {
	// Started by systemd/launchd/the Windows SCM (adesc service install):
	// let kardianos/service drive start and stop.
	if !service.Interactive() {
		return runAsService(serveDaemon)
	}
	return serveDaemon(cmd.Context())
}

// serveDaemon runs the core until ctx is canceled.
func serveDaemon(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	path := globalFlags.configPath
	if path == "" {
		if path, err = config.DefaultPath(); err != nil {
			return err
		}
	}
	a, err := app.New(cfg, app.WithConfigPath(path))
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		if errors.Is(err, app.ErrAlreadyRunning) {
			return fmt.Errorf("%w (%s)", err, cfg.DataDir)
		}
		return err
	}
	a.Logger().Info("listening", "url", a.BaseURL())
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.Stop(stopCtx)
}
