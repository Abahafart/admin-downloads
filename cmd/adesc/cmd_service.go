package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

// serviceName is the systemd unit / launchd label / Windows service name.
const serviceName = "admindescargas"

// daemonProgram adapts `adesc daemon` to kardianos/service: Start must
// return quickly, Stop cancels the core and waits for it to flush.
type daemonProgram struct {
	run    func(ctx context.Context) error
	cancel context.CancelFunc
	done   chan error
}

func (p *daemonProgram) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	go func() {
		err := p.run(ctx)
		p.done <- err
		if err != nil && ctx.Err() == nil {
			// The core failed on its own (port in use, locked data dir...):
			// exit so the service manager reports and restarts it.
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *daemonProgram) Stop(service.Service) error {
	if p.cancel == nil {
		return nil
	}
	p.cancel()
	select {
	case err := <-p.done:
		return err
	case <-time.After(45 * time.Second):
		return errors.New("el daemon no se detuvo a tiempo")
	}
}

// newService builds the service definition. The installed service runs
// `adesc daemon` with the same --config as this invocation (made absolute).
func newService(user bool, prg service.Interface) (service.Service, error) {
	args := []string{"daemon"}
	if globalFlags.configPath != "" {
		p, err := filepath.Abs(globalFlags.configPath)
		if err != nil {
			return nil, err
		}
		args = append(args, "--config", p)
	}
	if prg == nil {
		prg = &daemonProgram{}
	}
	return service.New(prg, &service.Config{
		Name:        serviceName,
		DisplayName: "AdminDescargas",
		Description: "Gestor de descargas multi-hoster (daemon y Web UI).",
		Arguments:   args,
		Option: service.KeyValue{
			"UserService": user,
			"Restart":     "on-failure",
			"KeepAlive":   true,
			"RunAtLoad":   true,
		},
	})
}

// runAsService runs fn under the OS service manager (used by `adesc
// daemon` when it is not started from a terminal).
func runAsService(fn func(ctx context.Context) error) error {
	s, err := newService(false, &daemonProgram{run: fn})
	if err != nil {
		return err
	}
	return s.Run()
}

func init() {
	var user bool
	svc := &cobra.Command{
		Use:   "service",
		Short: "Instala el daemon como servicio del sistema",
		Long: "Instala `adesc daemon` como servicio de systemd (Linux), launchd (macOS) o\n" +
			"servicio de Windows. Sin --user se instala para todo el sistema (requiere\n" +
			"permisos de administrador) y usa la configuración del usuario que lo ejecuta\n" +
			"solo si se indica --config.",
	}
	svc.PersistentFlags().BoolVar(&user, "user", false, "servicio del usuario actual (systemd --user, LaunchAgent)")
	action := func(use, short, done string, fn func(service.Service) error) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				s, err := newService(user, nil)
				if err != nil {
					return err
				}
				if err := fn(s); err != nil {
					return fmt.Errorf("%s: %w", cmd.CommandPath(), err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), done)
				return nil
			},
		}
	}
	svc.AddCommand(
		action("install", "Instala el servicio", "Servicio instalado. Arráncalo con `adesc service start`.",
			func(s service.Service) error { return s.Install() }),
		action("uninstall", "Desinstala el servicio", "Servicio desinstalado.",
			func(s service.Service) error {
				_ = s.Stop() // ignore "not running"
				return s.Uninstall()
			}),
		action("start", "Arranca el servicio", "Servicio arrancado.", func(s service.Service) error { return s.Start() }),
		action("stop", "Para el servicio", "Servicio parado.", func(s service.Service) error { return s.Stop() }),
		&cobra.Command{
			Use:   "status",
			Short: "Muestra el estado del servicio",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				s, err := newService(user, nil)
				if err != nil {
					return err
				}
				st, err := s.Status()
				switch {
				case errors.Is(err, service.ErrNotInstalled):
					fmt.Fprintln(cmd.OutOrStdout(), "no instalado")
					return nil
				case err != nil:
					return err
				}
				switch st {
				case service.StatusRunning:
					fmt.Fprintln(cmd.OutOrStdout(), "en marcha")
				case service.StatusStopped:
					fmt.Fprintln(cmd.OutOrStdout(), "parado")
				default:
					fmt.Fprintln(cmd.OutOrStdout(), "desconocido")
				}
				return nil
			},
		},
	)
	rootCmd.AddCommand(svc)
}
