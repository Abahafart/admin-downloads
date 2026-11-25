package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"admindescargas/internal/accounts"
	"admindescargas/internal/core"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
	"admindescargas/internal/store"
)

// Body size limits.
const (
	maxBody      = 1 << 20 // ordinary JSON requests
	maxBigBody   = 8 << 20 // cookies, proxy lists, WireGuard confs, links
	maxErrorText = 500     // characters of an error message sent to clients
	jsonType     = "application/json"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", jsonType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, OKResponse{OK: true}) }

func writeError(w http.ResponseWriter, status int, code, msg string) {
	if len(msg) > maxErrorText {
		msg = msg[:maxErrorText] + "…"
	}
	writeJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: msg}})
}

func notImplemented(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, CodeNotImplemented, "esta función no está disponible en este servidor")
}

// fail maps err to an HTTP answer. Client errors carry the error message;
// unexpected errors are logged and answered with a generic message so that
// internals never leak.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := classify(err)
	if status >= 500 && status != http.StatusServiceUnavailable {
		s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", redactCreds(err.Error()))
		writeError(w, status, code, "error interno")
		return
	}
	writeError(w, status, code, redactCreds(err.Error()))
}

func classify(err error) (int, string) {
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, core.ErrNotFound), errors.Is(err, store.ErrNotFound), errors.Is(err, accounts.ErrSecretNotFound),
		errors.Is(err, accounts.ErrAccountNotFound), errors.Is(err, rotation.ErrUnknownEgress):
		return http.StatusNotFound, CodeNotFound
	case errors.Is(err, accounts.ErrWrongPassword):
		return http.StatusForbidden, CodeWrongPassword
	case errors.Is(err, core.ErrInvalidArgument), errors.As(err, &mbe),
		errors.Is(err, accounts.ErrInvalidAccount), errors.Is(err, accounts.ErrInvalidCookies),
		errors.Is(err, accounts.ErrNoMatchingCookies), errors.Is(err, accounts.ErrCookiesExpired),
		errors.Is(err, accounts.ErrInvalidMFACode), errors.Is(err, accounts.ErrBadRedirect),
		errors.Is(err, accounts.ErrWeakPassword), errors.Is(err, accounts.ErrOAuthState),
		errors.Is(err, accounts.ErrOAuthDenied), errors.Is(err, accounts.ErrOAuthScope):
		return http.StatusBadRequest, CodeInvalidArgument
	case errors.Is(err, core.ErrInvalidState), errors.Is(err, netx.ErrRotateUnsupported),
		errors.Is(err, rotation.ErrExists), errors.Is(err, rotation.ErrNotImported),
		errors.Is(err, accounts.ErrNotLockable), errors.Is(err, accounts.ErrNoChecker),
		errors.Is(err, accounts.ErrOAuthNotConfigured):
		return http.StatusConflict, CodeInvalidState
	case errors.Is(err, accounts.ErrLocked):
		return http.StatusLocked, CodeLocked
	case errors.Is(err, rotation.ErrNoEgress), errors.Is(err, rotation.ErrClosed):
		return http.StatusServiceUnavailable, CodeUnavailable
	case isWait(err):
		return http.StatusConflict, CodeInvalidState
	case errors.Is(err, core.ErrStopped):
		return http.StatusServiceUnavailable, CodeUnavailable
	}
	return http.StatusInternalServerError, CodeInternal
}

// badRequest wraps a message as core.ErrInvalidArgument.
func badRequest(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), core.ErrInvalidArgument)
}

// decode reads a JSON body (at most limit bytes) into v. An empty body
// leaves v untouched when allowEmpty.
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any, allowEmpty bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) && allowEmpty {
			return nil
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return badRequest("el cuerpo supera %d bytes", mbe.Limit)
		}
		return badRequest("JSON no válido: %v", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("id %q no válido", v)
	}
	return id, nil
}

func isWait(err error) bool {
	_, ok := rotation.AsWait(err)
	return ok
}
