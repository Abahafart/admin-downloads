package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/core"
)

// apiCall runs fn with a client for the daemon and a context bounded by
// the command's context, turning connection errors into a friendly hint.
func apiCall(cmd *cobra.Command, fn func(ctx context.Context, c *apiclient.Client) error) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	return friendly(fn(cmd.Context(), c))
}

func friendly(err error) error {
	if err == nil {
		return nil
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return fmt.Errorf("no se pudo conectar con el daemon (¿está en marcha? ejecuta `adesc daemon`): %w", err)
	}
	var ae *apiclient.Error
	if errors.As(err, &ae) {
		switch ae.Status {
		case 401:
			return fmt.Errorf("el daemon rechazó el token (revisa --token o server.token): %s", ae.Message)
		case 501:
			return fmt.Errorf("el daemon no tiene esta función todavía: %s", ae.Message)
		}
		return errors.New(ae.Message)
	}
	return err
}

// printJSON writes v as indented JSON (for --json).
func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newTable(cmd *cobra.Command) *tabwriter.Writer {
	return tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
}

// parseIDs converts arguments to positive int64 ids.
func parseIDs(args []string) ([]int64, error) {
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		for _, part := range strings.Split(a, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("id no válido: %q", part)
			}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("indica al menos un id")
	}
	return ids, nil
}

// stdinIsPipe reports whether the command's stdin is not a terminal.
func stdinIsPipe(cmd *cobra.Command) bool {
	in := cmd.InOrStdin()
	f, ok := in.(*os.File)
	if !ok {
		return true // tests / redirected readers
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice == 0
}

// readAllInput reads a file path, or stdin for "-".
func readAllInput(cmd *cobra.Command, path string) (string, error) {
	var r io.Reader
	if path == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 16<<20))
	return string(b), err
}

// promptLine prints prompt on stderr and reads one line from stdin. The
// input is echoed by the terminal: pipe it (echo ... | adesc ...) to avoid
// that.
func promptLine(cmd *cobra.Command, prompt string) (string, error) {
	if !stdinIsPipe(cmd) {
		fmt.Fprint(cmd.ErrOrStderr(), prompt)
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return "", errors.New("no se recibió ningún valor por la entrada estándar")
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// humanBytes formats a size in binary units ("-" when unknown).
func humanBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanSpeed(bps int64) string {
	if bps <= 0 {
		return "-"
	}
	return humanBytes(bps) + "/s"
}

func humanETA(sec int64) string {
	if sec < 0 {
		return "-"
	}
	d := time.Duration(sec) * time.Second
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func percent(done, size int64) string {
	if size <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", float64(done)*100/float64(size))
}

var stateLabels = map[core.State]string{
	core.StateQueued:         "en cola",
	core.StateResolving:      "resolviendo",
	core.StateWaiting:        "en espera",
	core.StateDownloading:    "descargando",
	core.StateVerifying:      "verificando",
	core.StatePostprocessing: "postproceso",
	core.StateCompleted:      "completada",
	core.StateFailed:         "fallida",
	core.StatePaused:         "pausada",
}

func stateLabel(st core.State, waitingUntil time.Time) string {
	l, ok := stateLabels[st]
	if !ok {
		l = string(st)
	}
	if st == core.StateWaiting && !waitingUntil.IsZero() {
		if left := time.Until(waitingUntil); left > 0 {
			l += " " + humanETA(int64(left.Seconds()))
		}
	}
	return l
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func yesNo(b bool) string {
	if b {
		return "sí"
	}
	return "no"
}
