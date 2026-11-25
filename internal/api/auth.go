package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"admindescargas/internal/config"
)

// Session cookie and CSRF header names.
const (
	SessionCookie = "adesc_session"
	CSRFHeader    = "X-CSRF-Token"
)

const (
	sessionTTL     = 7 * 24 * time.Hour
	maxSessions    = 256
	freeLoginFails = 5                // failures per IP before blocking
	loginBlockBase = 30 * time.Second // first block, doubled per failure
	loginBlockMax  = 15 * time.Minute
	loginForgetAge = 30 * time.Minute // failures older than this are forgotten
)

type session struct {
	csrf    string
	expires time.Time
	created time.Time
}

type loginRecord struct {
	fails        int
	lastFail     time.Time
	blockedUntil time.Time
}

// authenticator implements bearer-token and cookie-session authentication.
type authenticator struct {
	tokenDigest    [32]byte
	hasToken       bool
	passwordHash   string
	loopbackListen bool
	now            func() time.Time
	log            *slog.Logger
	// verifySem bounds concurrent Argon2 verifications (memory-hard).
	verifySem chan struct{}

	mu       sync.Mutex
	sessions map[string]*session // key: sha256(cookie value) hex
	logins   map[string]*loginRecord
}

func newAuthenticator(sc config.ServerConfig, now func() time.Time, log *slog.Logger) *authenticator {
	a := &authenticator{
		passwordHash: sc.PasswordHash,
		now:          now,
		log:          log,
		verifySem:    make(chan struct{}, 2),
		sessions:     map[string]*session{},
		logins:       map[string]*loginRecord{},
	}
	if sc.Token != "" {
		a.hasToken = true
		a.tokenDigest = sha256.Sum256([]byte(sc.Token))
	}
	host, _, err := net.SplitHostPort(sc.Listen)
	a.loopbackListen = err == nil && config.IsLoopbackHost(host)
	return a
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("api: crypto/rand failed: " + err.Error()) // never happens on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func digestHex(s string) string {
	d := sha256.Sum256([]byte(s))
	return hex.EncodeToString(d[:])
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// identity is the result of authenticating a request.
type identity struct {
	method string // "token" | "cookie" | ""
	sess   *session
	// failure, when method == "":
	status int
	code   string
	msg    string
}

func (a *authenticator) identify(r *http.Request) identity {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, tok, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || !a.hasToken {
			return identity{status: http.StatusUnauthorized, code: CodeUnauthorized, msg: "token no válido"}
		}
		d := sha256.Sum256([]byte(strings.TrimSpace(tok)))
		if subtle.ConstantTimeCompare(d[:], a.tokenDigest[:]) != 1 {
			return identity{status: http.StatusUnauthorized, code: CodeUnauthorized, msg: "token no válido"}
		}
		return identity{method: "token"}
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		if s := a.lookup(c.Value); s != nil {
			return identity{method: "cookie", sess: s}
		}
		return identity{status: http.StatusUnauthorized, code: CodeUnauthorized, msg: "la sesión ha caducado"}
	}
	return identity{status: http.StatusUnauthorized, code: CodeUnauthorized, msg: "se requiere autenticación"}
}

func (a *authenticator) lookup(cookie string) *session {
	key := digestHex(cookie)
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[key]
	if s == nil {
		return nil
	}
	if !a.now().Before(s.expires) {
		delete(a.sessions, key)
		return nil
	}
	return s
}

// require wraps a handler that needs authentication. Cookie sessions must
// also send the CSRF token on unsafe methods.
func (a *authenticator) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := a.identify(r)
		if id.method == "" {
			if id.status == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="adesc"`)
			}
			writeError(w, id.status, id.code, id.msg)
			return
		}
		if id.method == "cookie" && !safeMethod(r.Method) {
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(id.sess.csrf)) != 1 {
				writeError(w, http.StatusForbidden, CodeCSRF, "falta el token CSRF o no es válido")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopbackRemote(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

// blocked returns how long ip must wait before trying again (0 = allowed).
func (a *authenticator) blocked(ip string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for k, rec := range a.logins { // forget stale records
		if now.Sub(rec.lastFail) > loginForgetAge && !now.Before(rec.blockedUntil) {
			delete(a.logins, k)
		}
	}
	if rec := a.logins[ip]; rec != nil && now.Before(rec.blockedUntil) {
		return rec.blockedUntil.Sub(now)
	}
	return 0
}

func (a *authenticator) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	rec := a.logins[ip]
	if rec == nil {
		rec = &loginRecord{}
		a.logins[ip] = rec
	}
	rec.fails++
	rec.lastFail = now
	if rec.fails >= freeLoginFails {
		exp := float64(rec.fails - freeLoginFails)
		d := time.Duration(float64(loginBlockBase) * math.Pow(2, math.Min(exp, 10)))
		rec.blockedUntil = now.Add(min(d, loginBlockMax))
	}
}

// forget drops the failure record of key (after a success).
func (a *authenticator) forget(key string) {
	a.mu.Lock()
	delete(a.logins, key)
	a.mu.Unlock()
}

func (a *authenticator) newSession() (string, *session) {
	cookie := randomToken()
	now := a.now()
	s := &session{csrf: randomToken(), created: now, expires: now.Add(sessionTTL)}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.sessions {
		if !now.Before(v.expires) {
			delete(a.sessions, k)
		}
	}
	for len(a.sessions) >= maxSessions { // evict the oldest
		var oldest string
		var t time.Time
		for k, v := range a.sessions {
			if oldest == "" || v.created.Before(t) {
				oldest, t = k, v.created
			}
		}
		delete(a.sessions, oldest)
	}
	a.sessions[digestHex(cookie)] = s
	return cookie, s
}

func (a *authenticator) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait := a.blocked(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "demasiados intentos fallidos; espera antes de volver a intentarlo")
		return
	}
	var req LoginRequest
	if err := decode(w, r, 4<<10, &req, false); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	if a.passwordHash == "" {
		// No Web UI password: only local clients of a loopback daemon.
		if !a.loopbackListen || !isLoopbackRemote(r) {
			writeError(w, http.StatusForbidden, CodeForbidden, "no hay contraseña configurada (adesc password)")
			return
		}
	} else {
		select {
		case a.verifySem <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		ok, err := VerifyPassword(a.passwordHash, req.Password)
		<-a.verifySem
		if err != nil {
			a.log.Error("server.password_hash is not a valid argon2id PHC string", "err", err)
			writeError(w, http.StatusInternalServerError, CodeInternal, "error interno")
			return
		}
		if !ok {
			a.recordFailure(ip)
			a.log.Warn("web UI login failed", "remote", ip)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "contraseña incorrecta")
			return
		}
		a.mu.Lock()
		delete(a.logins, ip)
		a.mu.Unlock()
	}
	cookie, s := a.newSession()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly+SameSite=Strict; Secure only over TLS (loopback is plain http)
		Name:     SessionCookie,
		Value:    cookie,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, LoginResponse{CSRF: s.csrf, ExpiresAt: s.expires})
}

func (a *authenticator) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		a.mu.Lock()
		delete(a.sessions, digestHex(c.Value))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly+SameSite=Strict; Secure only over TLS (loopback is plain http)
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	writeOK(w)
}

func (a *authenticator) session(w http.ResponseWriter, r *http.Request) {
	info := SessionInfo{PasswordRequired: a.passwordHash != ""}
	id := a.identify(r)
	switch id.method {
	case "token":
		info.Authenticated, info.Method = true, "token"
	case "cookie":
		info.Authenticated, info.Method = true, "cookie"
		info.CSRF, info.ExpiresAt = id.sess.csrf, id.sess.expires
	}
	writeJSON(w, http.StatusOK, info)
}
