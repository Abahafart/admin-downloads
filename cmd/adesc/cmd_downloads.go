package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/core"
)

// Download queue commands (clients of /api/v1/links, /linkgrabber,
// /downloads and /packages).
func init() {
	rootCmd.AddCommand(newAddCmd(), newLsCmd(),
		newActionCmd(apiclient.ActionPause, "pause <id>...", "Pausa descargas (se pueden reanudar)",
			"--all pausa todas las descargas (como el botón de la bandeja)."),
		newActionCmd(apiclient.ActionResume, "resume <id>...", "Reanuda descargas desde el último byte guardado",
			"--all reanuda todo lo pausado."),
		newActionCmd(apiclient.ActionRestart, "restart <id>...", "Borra el progreso y descarga desde cero", ""),
		newActionCmd(apiclient.ActionRetry, "retry <id>...", "Reintenta descargas fallidas",
			"--failed (o --all) reintenta todas las fallidas."),
		newActionCmd(apiclient.ActionDelete, "rm <id>...", "Quita descargas de la cola",
			"--files borra también los archivos (.part o completados) del disco."),
		newGrabberCmd(),
	)
}

func newAddCmd() *cobra.Command {
	var start bool
	var dest, pkg string
	cmd := &cobra.Command{
		Use:   "add [url]...",
		Short: "Añade enlaces al LinkGrabber (también los lee de stdin)",
		Long: "Añade enlaces al LinkGrabber. Sin argumentos (o con \"-\") lee texto de la entrada\n" +
			"estándar y extrae todos los enlaces que contenga.\n\n" +
			"Con --start las descargas empiezan en cuanto se resuelven, sin pasar por el LinkGrabber.",
		Example: "  adesc add https://mega.nz/file/abc#key\n  cat enlaces.txt | adesc add --start",
		RunE: func(cmd *cobra.Command, args []string) error {
			req := core.AddLinksRequest{Source: "cli", AutoConfirm: start, DestDir: dest, PackageName: pkg}
			if len(args) == 0 || len(args) == 1 && args[0] == "-" {
				if len(args) == 0 && !stdinIsPipe(cmd) {
					return errors.New("indica enlaces como argumentos o pásalos por la entrada estándar")
				}
				text, err := readAllInput(cmd, "-")
				if err != nil {
					return err
				}
				req.Text = text
			} else {
				req.URLs = args
			}
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				entries, err := c.AddLinks(ctx, req)
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, entries)
				}
				if len(entries) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No se encontraron enlaces nuevos.")
					return nil
				}
				printGrabber(cmd, entries)
				if !start {
					fmt.Fprintln(cmd.ErrOrStderr(), "\nConfírmalos con `adesc grabber confirm --all` o desde la Web UI.")
				}
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&start, "start", "s", false, "empezar a descargar sin pasar por el LinkGrabber")
	f.StringVar(&dest, "dest", "", "carpeta de destino (con --start; por defecto download_dir)")
	f.StringVar(&pkg, "package", "", "nombre del paquete (con --start)")
	return cmd
}

var grabberLabels = map[core.GrabberState]string{
	core.GrabberPending:     "pendiente",
	core.GrabberResolving:   "resolviendo",
	core.GrabberReady:       "listo",
	core.GrabberError:       "error",
	core.GrabberUnsupported: "no soportado",
}

func printGrabber(cmd *cobra.Command, entries []core.GrabberEntry) {
	tw := newTable(cmd)
	fmt.Fprintln(tw, "ID\tESTADO\tHOSTER\tARCHIVOS\tTAMAÑO\tNOMBRE / ENLACE")
	for _, e := range entries {
		name := e.PackageName
		if name == "" {
			name = e.URL
		}
		state := grabberLabels[e.State]
		if e.Error != "" {
			state += ": " + truncate(e.Error, 40)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\n", e.ID, state, e.Hoster, len(e.Files), humanBytes(e.TotalSize), truncate(name, 70))
	}
	tw.Flush()
}

func newLsCmd() *cobra.Command {
	var states, query, hoster string
	var pkgID int64
	var packages bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Lista paquetes y descargas con su progreso",
		Example: "  adesc ls\n  adesc ls --state failed,waiting\n  adesc ls --packages",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if packages {
					pkgs, err := c.Packages(ctx)
					if err != nil {
						return err
					}
					if globalFlags.jsonOut {
						return printJSON(cmd, pkgs)
					}
					tw := newTable(cmd)
					fmt.Fprintln(tw, "ID\tESTADO\tARCHIVOS\tPROGRESO\tVELOCIDAD\tETA\tTAMAÑO\tNOMBRE")
					for _, p := range pkgs {
						files := fmt.Sprintf("%d/%d", p.FilesDone, p.FilesTotal)
						if p.FilesFail > 0 {
							files += fmt.Sprintf(" (%d fallidos)", p.FilesFail)
						}
						fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, stateLabel(p.State, time.Time{}), files,
							percent(p.Done, p.Size), humanSpeed(p.Speed), humanETA(p.ETA), humanBytes(p.Size), truncate(p.Name, 60))
					}
					return tw.Flush()
				}
				q := apiclient.DownloadQuery{PackageID: pkgID, Q: query, Hoster: hoster}
				for _, s := range strings.Split(states, ",") {
					if s = strings.TrimSpace(s); s != "" {
						q.States = append(q.States, core.State(s))
					}
				}
				ds, err := c.Downloads(ctx, q)
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, ds)
				}
				if len(ds) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No hay descargas.")
					return nil
				}
				tw := newTable(cmd)
				fmt.Fprintln(tw, "ID\tPAQ\tESTADO\tPROGRESO\tVELOCIDAD\tETA\tTAMAÑO\tSALIDA\tNOMBRE")
				for _, d := range ds {
					name := d.Name
					if d.RelPath != "" {
						name = d.RelPath
					}
					state := stateLabel(d.State, d.WaitingUntil)
					if d.Resumable != nil && !*d.Resumable {
						state += " (no reanudable)"
					}
					egress := d.EgressID
					if egress == "" {
						egress = "-"
					}
					fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.PackageID, state, percent(d.Done, d.Size),
						humanSpeed(d.Speed), humanETA(d.ETA), humanBytes(d.Size), egress, truncate(name, 60))
					if d.Error != "" && (d.State == core.StateFailed || d.State == core.StateWaiting) {
						fmt.Fprintf(tw, "\t\t  ↳ %s\t\t\t\t\t\t\n", truncate(d.Error, 90))
					}
				}
				return tw.Flush()
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&states, "state", "", "filtra por estado (queued, downloading, waiting, paused, failed, completed...; separados por comas)")
	f.Int64Var(&pkgID, "package", 0, "solo las descargas de este paquete")
	f.StringVarP(&query, "search", "q", "", "busca en el nombre, la ruta o el enlace")
	f.StringVar(&hoster, "hoster", "", "solo este hoster")
	f.BoolVarP(&packages, "packages", "p", false, "lista paquetes en lugar de descargas")
	return cmd
}

// newActionCmd builds pause/resume/restart/retry/rm.
func newActionCmd(action, use, short, extra string) *cobra.Command {
	var all, failed, pkgs, files bool
	long := short + "."
	if extra != "" {
		long += "\n\n" + extra
	}
	long += "\nCon --packages los ids son de paquetes (la acción se aplica a todos sus archivos)."
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				switch {
				case all && action == apiclient.ActionPause:
					return report(cmd, c.PauseAll(ctx), "Todo pausado.")
				case all && action == apiclient.ActionResume:
					return report(cmd, c.ResumeAll(ctx), "Todo reanudado.")
				case (all || failed) && action == apiclient.ActionRetry:
					return report(cmd, c.RetryFailed(ctx), "Reintentando las descargas fallidas.")
				}
				ids, err := parseIDs(args)
				if err != nil {
					return err
				}
				if pkgs {
					err = c.PackageAction(ctx, action, ids, files)
				} else {
					err = c.DownloadAction(ctx, action, ids, files)
				}
				return report(cmd, err, "Hecho.")
			})
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&pkgs, "packages", "p", false, "los ids son de paquetes")
	switch action {
	case apiclient.ActionPause, apiclient.ActionResume:
		f.BoolVarP(&all, "all", "a", false, "aplicar a todas las descargas")
	case apiclient.ActionRetry:
		f.BoolVar(&failed, "failed", false, "reintentar todas las fallidas")
		f.BoolVarP(&all, "all", "a", false, "igual que --failed")
	case apiclient.ActionDelete:
		cmd.Aliases = []string{"delete"}
		f.BoolVar(&files, "files", false, "borrar también los archivos del disco")
	}
	return cmd
}

func report(cmd *cobra.Command, err error, msg string) error {
	if err != nil {
		return err
	}
	if !globalFlags.jsonOut {
		fmt.Fprintln(cmd.OutOrStdout(), msg)
	}
	return nil
}

func newGrabberCmd() *cobra.Command {
	g := &cobra.Command{
		Use:     "grabber",
		Aliases: []string{"linkgrabber"},
		Short:   "Revisa y confirma los enlaces del LinkGrabber",
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "Lista los enlaces pendientes de confirmar",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				entries, err := c.LinkGrabber(ctx)
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, entries)
				}
				if len(entries) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "El LinkGrabber está vacío.")
					return nil
				}
				printGrabber(cmd, entries)
				return nil
			})
		},
	}
	var all, paused bool
	var dest, exists string
	confirm := &cobra.Command{
		Use:   "confirm [id]...",
		Short: "Pasa entradas del LinkGrabber a la cola de descargas",
		Long:  "Pasa entradas del LinkGrabber a la cola (todos sus archivos). Con --all confirma\ntodas las entradas listas.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var ids []int64
				if all {
					entries, err := c.LinkGrabber(ctx)
					if err != nil {
						return err
					}
					for _, e := range entries {
						if e.State == core.GrabberReady {
							ids = append(ids, e.ID)
						}
					}
					if len(ids) == 0 {
						return errors.New("no hay entradas listas para confirmar")
					}
				} else {
					var err error
					if ids, err = parseIDs(args); err != nil {
						return err
					}
				}
				req := core.ConfirmRequest{DestDir: dest, ExistsPolicy: exists, Paused: paused}
				for _, id := range ids {
					req.Items = append(req.Items, core.ConfirmItem{EntryID: id})
				}
				pkgs, err := c.ConfirmLinkGrabber(ctx, req)
				if err != nil {
					return err
				}
				if globalFlags.jsonOut {
					return printJSON(cmd, pkgs)
				}
				for _, p := range pkgs {
					fmt.Fprintf(cmd.OutOrStdout(), "Paquete %d: %s (%d archivos) -> %s\n", p.ID, p.Name, p.FilesTotal, p.DestDir)
				}
				return nil
			})
		},
	}
	cf := confirm.Flags()
	cf.BoolVarP(&all, "all", "a", false, "confirmar todas las entradas listas")
	cf.BoolVar(&paused, "paused", false, "añadirlas en pausa")
	cf.StringVar(&dest, "dest", "", "carpeta de destino (por defecto download_dir)")
	cf.StringVar(&exists, "exists", "", "si el archivo ya existe: skip, overwrite o rename")
	rm := &cobra.Command{
		Use:   "rm <id>...",
		Short: "Descarta entradas del LinkGrabber",
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			return apiCall(cmd, func(ctx context.Context, c *apiclient.Client) error {
				return report(cmd, c.RemoveLinkGrabber(ctx, ids), "Hecho.")
			})
		},
	}
	g.AddCommand(ls, confirm, rm)
	return g
}
