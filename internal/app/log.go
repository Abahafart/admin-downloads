package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"admindescargas/internal/config"
)

// NewLogger builds the slog logger described by cfg (text or JSON, to
// stderr or to cfg.File). Never log secrets: accounts.Secret redacts itself,
// but cookies, tokens and signed URLs passed as plain strings do not.
func NewLogger(cfg config.LogConfig) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(orDefault(cfg.Level, "info")))); err != nil {
		return nil, fmt.Errorf("app: log.level %q: %w", cfg.Level, err)
	}
	var w io.Writer = os.Stderr
	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o700); err != nil {
			return nil, fmt.Errorf("app: log dir: %w", err)
		}
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("app: log file: %w", err)
		}
		w = f // closed at process exit
	}
	opts := &slog.HandlerOptions{Level: level}
	switch orDefault(cfg.Format, "text") {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("app: log.format %q: want text|json", cfg.Format)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
