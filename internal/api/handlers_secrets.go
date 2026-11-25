package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"admindescargas/internal/accounts"
	"admindescargas/internal/core"
)

// SecretsService manages the secret store that keeps account credentials
// (implemented by an adapter over accounts.Manager). Unlock / ChangePassword
// return accounts.ErrWrongPassword (wrapped) on a bad master password,
// accounts.ErrWeakPassword for a too short new one and
// accounts.ErrNotLockable when the store has no master password (keyring).
type SecretsService interface {
	Status(ctx context.Context) (accounts.StoreStatus, error)
	// Unlock opens the vault; the first unlock of a new vault chooses the
	// master password.
	Unlock(ctx context.Context, password string) error
	Lock(ctx context.Context) error
	ChangePassword(ctx context.Context, oldPassword, newPassword string) error
}

// UnlockRequest is the body of POST /api/v1/secrets/unlock.
type UnlockRequest struct {
	Password string `json:"password"`
}

// ChangePasswordRequest is the body of POST /api/v1/secrets/password.
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// OAuthCompleteRequest is the body of POST /api/v1/oauth/google/complete:
// the final address the browser showed after authorizing (used when the
// browser could not reach the daemon's callback).
type OAuthCompleteRequest struct {
	URL string `json:"url"`
}

// KnownLinksRequest is the body of POST /api/v1/links/known.
type KnownLinksRequest struct {
	URLs []string `json:"urls"`
}

// KnownLinksResponse lists the (normalized) links of the request that are
// already queued, downloaded or in the LinkGrabber.
type KnownLinksResponse struct {
	Known []string `json:"known"`
}

// maxKnownLinks bounds one POST /links/known.
const maxKnownLinks = 1000

func (s *server) secretsSvc(w http.ResponseWriter) SecretsService {
	if s.d.Secrets == nil {
		notImplemented(w)
	}
	return s.d.Secrets
}

func (s *server) secretsStatus(w http.ResponseWriter, r *http.Request) {
	svc := s.secretsSvc(w)
	if svc == nil {
		return
	}
	st, err := svc.Status(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// vaultKey separates the unlock failure records from the login ones.
func vaultKey(r *http.Request) string { return "vault|" + clientIP(r) }

func (s *server) secretsUnlock(w http.ResponseWriter, r *http.Request) {
	svc := s.secretsSvc(w)
	if svc == nil {
		return
	}
	key := vaultKey(r)
	if wait := s.auth.blocked(key); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "demasiados intentos fallidos; espera antes de volver a intentarlo")
		return
	}
	var req UnlockRequest
	if err := decode(w, r, 4<<10, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Password == "" {
		s.fail(w, r, badRequest("falta password"))
		return
	}
	if err := svc.Unlock(r.Context(), req.Password); err != nil {
		if errors.Is(err, accounts.ErrWrongPassword) {
			s.auth.recordFailure(key)
			s.log.Warn("secret store unlock failed", "remote", clientIP(r))
		}
		s.fail(w, r, err)
		return
	}
	s.auth.forget(key)
	s.secretsStatus(w, r)
}

func (s *server) secretsLock(w http.ResponseWriter, r *http.Request) {
	svc := s.secretsSvc(w)
	if svc == nil {
		return
	}
	if err := svc.Lock(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.secretsStatus(w, r)
}

func (s *server) secretsPassword(w http.ResponseWriter, r *http.Request) {
	svc := s.secretsSvc(w)
	if svc == nil {
		return
	}
	key := vaultKey(r)
	if wait := s.auth.blocked(key); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "demasiados intentos fallidos; espera antes de volver a intentarlo")
		return
	}
	var req ChangePasswordRequest
	if err := decode(w, r, 4<<10, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		s.fail(w, r, badRequest("faltan old_password o new_password"))
		return
	}
	if err := svc.ChangePassword(r.Context(), req.OldPassword, req.NewPassword); err != nil {
		if errors.Is(err, accounts.ErrWrongPassword) {
			s.auth.recordFailure(key)
		}
		s.fail(w, r, err)
		return
	}
	s.auth.forget(key)
	writeOK(w)
}

func (s *server) oauthComplete(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notImplemented(w)
		return
	}
	var req OAuthCompleteRequest
	if err := decode(w, r, 16<<10, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		s.fail(w, r, badRequest("falta url"))
		return
	}
	if err := s.d.OAuth.CompleteURL(r.Context(), req.URL); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

func (s *server) knownLinks(w http.ResponseWriter, r *http.Request) {
	lc, ok := s.d.Engine.(core.LinkChecker)
	if s.d.Engine == nil || !ok {
		notImplemented(w)
		return
	}
	var req KnownLinksRequest
	if err := decode(w, r, maxBigBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(req.URLs) > maxKnownLinks {
		s.fail(w, r, badRequest("como mucho %d enlaces por petición", maxKnownLinks))
		return
	}
	known, err := lc.KnownLinks(r.Context(), req.URLs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, KnownLinksResponse{Known: nonNil(known)})
}
