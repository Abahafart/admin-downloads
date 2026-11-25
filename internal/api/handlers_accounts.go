package api

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"admindescargas/internal/accounts"
)

func (s *server) accountsSvc(w http.ResponseWriter) AccountService {
	if s.d.Accounts == nil {
		notImplemented(w)
	}
	return s.d.Accounts
}

func (s *server) listAccounts(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	out, err := svc.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *server) createAccount(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	var req AccountCreateRequest
	if err := decode(w, r, maxBigBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	req.Hoster = strings.TrimSpace(req.Hoster)
	if req.Hoster == "" {
		s.fail(w, r, badRequest("falta hoster"))
		return
	}
	if !accounts.Kind(req.Kind).Valid() {
		s.fail(w, r, badRequest("kind %q no válido (password, cookie, oauth o apikey)", req.Kind))
		return
	}
	if req.Enabled == nil {
		t := true
		req.Enabled = &t
	}
	a, err := svc.Create(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) updateAccount(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	var p AccountPatch
	if err := decode(w, r, maxBigBody, &p, false); err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := svc.Update(r.Context(), r.PathValue("id"), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	if err := svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

func (s *server) checkAccount(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	a, err := svc.Check(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) submit2FA(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	var req TwoFARequest
	if err := decode(w, r, 4<<10, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Code = strings.TrimSpace(req.Code); req.Code == "" {
		s.fail(w, r, badRequest("falta code"))
		return
	}
	a, err := svc.Submit2FA(r.Context(), r.PathValue("id"), req.Code)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) importCookies(w http.ResponseWriter, r *http.Request) {
	svc := s.accountsSvc(w)
	if svc == nil {
		return
	}
	var req ImportCookiesRequest
	if err := decode(w, r, maxBigBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Hoster = strings.TrimSpace(req.Hoster); req.Hoster == "" || strings.TrimSpace(req.Cookies) == "" {
		s.fail(w, r, badRequest("faltan hoster o cookies"))
		return
	}
	a, err := svc.ImportCookies(r.Context(), req.Hoster, strings.TrimSpace(req.Label), req.Cookies)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// callbackPath is where Google redirects after consent.
const callbackPath = "/api/v1/oauth/google/callback"

func (s *server) oauthStart(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notImplemented(w)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if id == "" {
		s.fail(w, r, badRequest("falta account_id"))
		return
	}
	u, err := s.d.OAuth.Start(r.Context(), id, s.oauthRedirect(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, URLResponse{URL: u})
}

// oauthRedirect builds the OAuth redirect URL. Google "Desktop" clients only
// accept loopback redirects, so when the UI reaches the daemon through a
// non-loopback host (e.g. http://nas:7979) the redirect still points at
// 127.0.0.1 on the daemon's port. On the machine running the browser that
// address is not the daemon, so the user finishes with the paste-URL flow
// (POST /api/v1/oauth/google/complete).
func (s *server) oauthRedirect(r *http.Request) string {
	// r.Host was validated by the guard (loopback, IP or allowed host).
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, ""
	}
	if isLoopbackHost(host) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return fmt.Sprintf("%s://%s%s", scheme, r.Host, callbackPath)
	}
	if _, p, err := net.SplitHostPort(s.d.Server.Listen); err == nil && p != "" && p != "0" {
		port = p
	}
	if port == "" {
		port = "80"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + callbackPath
}

func isLoopbackHost(host string) bool {
	h := strings.Trim(strings.ToLower(host), "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.IsLoopback()
}

var oauthPage = template.Must(template.New("oauth").Parse(`<!doctype html>
<html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AdminDescargas · Google</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5}</style>
</head><body><h1>{{.Title}}</h1><p>{{.Message}}</p><p><a href="/">Volver a AdminDescargas</a></p></body></html>`))

func (s *server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notImplemented(w)
		return
	}
	page := struct{ Title, Message string }{"Cuenta de Google conectada", "Ya puedes cerrar esta ventana."}
	status := http.StatusOK
	if err := s.d.OAuth.Callback(r.Context(), r.URL.Query()); err != nil {
		// The message is not echoed (it may contain query data); it is logged.
		s.log.Warn("google oauth callback failed", "err", err)
		page = struct{ Title, Message string }{"No se pudo conectar la cuenta", "La autorización de Google falló o caducó. Vuelve a intentarlo desde Cuentas."}
		status, _ = classify(err)
		if status >= 500 {
			status = http.StatusBadRequest
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = oauthPage.Execute(w, page)
}
