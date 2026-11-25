package netx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RunArgv executes argv (argv[0] is the program, looked up in PATH) without
// a shell, bounded by timeout (0 = only ctx). The output is never logged;
// on failure the error carries the last bytes of it, trimmed. argv must
// come from config.toml, never from the API.
func RunArgv(ctx context.Context, argv []string, timeout time.Duration) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errors.New("netx: empty command")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out tailBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Stdin = nil
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("netx: command %s: %w", argv[0], ctx.Err())
		}
		if tail := strings.TrimSpace(out.String()); tail != "" {
			return fmt.Errorf("netx: command %s: %w: %s", argv[0], err, tail)
		}
		return fmt.Errorf("netx: command %s: %w", argv[0], err)
	}
	return nil
}

// tailBuffer keeps the last 256 bytes written.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 256 {
		t.b = t.b[len(t.b)-256:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(bytes.ToValidUTF8(t.b, nil)) }

func tempDir() string { return os.TempDir() }

// sleepCtx waits d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
