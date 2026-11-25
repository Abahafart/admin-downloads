package mediafire

import (
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"admindescargas/internal/hosters"
)

// logOf returns the session logger (slog.Default if the session has none).
func logOf(s *hosters.Session) *slog.Logger {
	if s != nil && s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// redactURLError strips the query string from *url.Error values so that
// error messages (which end up in logs and in the database) never carry
// signed URLs, confirmation uuids or session tokens.
func redactURLError(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) {
		return err
	}
	clean := ue.URL
	if u, perr := url.Parse(ue.URL); perr == nil {
		u.RawQuery, u.Fragment, u.User = "", "", nil
		clean = u.String()
	} else if i := strings.IndexAny(clean, "?#"); i >= 0 {
		clean = clean[:i]
	}
	if clean == ue.URL {
		return err
	}
	return &url.Error{Op: ue.Op, URL: clean, Err: ue.Err}
}
