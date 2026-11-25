package clipboard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	xclip "golang.design/x/clipboard"

	"admindescargas/internal/config"
)

// ErrNoClipboard is returned by NewSource when no backend can reach the
// clipboard (a server without display, GNOME on pure Wayland without
// wl-clipboard...).
var ErrNoClipboard = errors.New("clipboard: no clipboard backend available")

// NewSource picks the best backend for this session:
//   - golang.design/x/clipboard (Windows, macOS, Linux X11/XWayland and
//     Wayland compositors with data-control);
//   - `wl-paste --watch` (wl-clipboard) on Wayland when the native backend
//     cannot reach the clipboard and cfg.UseWlPaste is set.
func NewSource(cfg config.ClipboardConfig) (Source, error) {
	nativeErr := xclip.Init()
	if nativeErr == nil {
		return nativeSource{}, nil
	}
	if cfg.UseWlPaste && WlPasteAvailable() {
		return &WlPasteSource{}, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrNoClipboard, nativeErr)
}

// nativeSource uses golang.design/x/clipboard.Watch (text only).
type nativeSource struct{}

func (nativeSource) Name() string { return "native" }

func (nativeSource) Watch(ctx context.Context) (<-chan Clip, error) {
	if err := xclip.Init(); err != nil {
		return nil, err
	}
	in := xclip.Watch(ctx, xclip.FmtText)
	out := make(chan Clip)
	go func() {
		defer close(out)
		for d := range in {
			select {
			case out <- Clip{Text: string(d.Bytes), Sensitive: d.Sensitive}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// WlPasteAvailable reports whether this is a Wayland session with the
// wl-paste binary in PATH. Only meaningful on Linux and the BSDs.
func WlPasteAvailable() bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" || os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	_, err := exec.LookPath("wl-paste")
	return err == nil
}

// WlPasteSource runs `wl-paste --type text --watch echo`, which prints one
// line per clipboard change (echo ignores the content on its stdin), and
// reads the new text with `wl-paste --no-newline --type text`. The content
// never goes through a shell. It needs a compositor with the wlroots
// data-control protocol (KDE, Sway, Hyprland...).
type WlPasteSource struct {
	// Bin is the wl-paste binary (default "wl-paste").
	Bin string
}

// Name implements Source.
func (s *WlPasteSource) Name() string { return "wl-paste" }

func (s *WlPasteSource) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "wl-paste"
}

// Watch implements Source.
func (s *WlPasteSource) Watch(ctx context.Context) (<-chan Clip, error) {
	cmd := exec.CommandContext(ctx, s.bin(), "--type", "text", "--watch", "echo")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("wl-paste: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("wl-paste: %w", err)
	}
	out := make(chan Clip)
	go func() {
		defer close(out)
		defer func() { _ = cmd.Wait() }()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			text, err := s.read(ctx)
			if err != nil {
				continue // empty or non-text selection
			}
			select {
			case out <- Clip{Text: text}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (s *WlPasteSource) read(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.bin(), "--no-newline", "--type", "text")
	b, err := cmd.Output()
	if err != nil {
		return "", err
	}
	if len(b) > MaxTextSize {
		return "", errors.New("clipboard text too large")
	}
	return string(b), nil
}
