package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"admindescargas/internal/config"
)

// guard validates Host and Origin (DNS rebinding and cross-site requests),
// answers CORS preflights for the allowed extra origins and sets security
// headers on every response.
type guard struct {
	hosts   map[string]bool // lowercase hostnames (no port) or host:port
	origins map[string]bool // normalized scheme://host[:port]
}

func newGuard(sc config.ServerConfig, extraOrigins []string) *guard {
	g := &guard{hosts: map[string]bool{"localhost": true}, origins: map[string]bool{}}
	if h, _, err := net.SplitHostPort(sc.Listen); err == nil && h != "" {
		g.hosts[strings.ToLower(h)] = true
	}
	for _, h := range sc.AllowedHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			g.hosts[strings.TrimSuffix(h, ".")] = true
		}
	}
	for _, list := range [][]string{sc.AllowedOrigins, extraOrigins} {
		for _, o := range list {
			if n := normalizeOrigin(o); n != "" {
				g.origins[n] = true
			}
		}
	}
	return g
}

// normalizeOrigin lowercases scheme and host and drops a trailing slash and
// default ports. Opaque origins ("null") and garbage return "".
func normalizeOrigin(o string) string {
	o = strings.TrimSuffix(strings.TrimSpace(o), "/")
	u, err := url.Parse(o)
	if err != nil || u.Scheme == "" || u.Host == "" || o == "null" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

func (g *guard) hostAllowed(hostport string) bool {
	hostport = strings.ToLower(strings.TrimSpace(hostport))
	if hostport == "" {
		return false
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	// IP literals cannot be the result of DNS rebinding (the browser's
	// origin is then the IP itself).
	if net.ParseIP(host) != nil {
		return true
	}
	return g.hosts[host] || g.hosts[hostport]
}

// originAllowed reports whether origin is the server's own origin or an
// explicitly allowed one; cross is true for the latter (CORS needed).
func (g *guard) originAllowed(r *http.Request, origin string) (ok, cross bool) {
	n := normalizeOrigin(origin)
	if n == "" {
		return false, false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if n == normalizeOrigin(scheme+"://"+r.Host) {
		return true, false
	}
	if g.origins[n] {
		return true, true
	}
	return false, false
}

// extensionSchemes are the Origin schemes of browser extensions. Requests
// from them are accepted cross-origin (any extension id) but only with the
// Bearer token: cookies are stripped so a browser session of the Web UI is
// never usable from an extension, and they cannot log in.
var extensionSchemes = []string{"chrome-extension://", "moz-extension://", "safari-web-extension://"}

func isExtensionOrigin(normalized string) bool {
	for _, p := range extensionSchemes {
		if strings.HasPrefix(normalized, p) {
			return true
		}
	}
	return false
}

func (g *guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Add("Vary", "Origin")

		if !g.hostAllowed(r.Host) {
			writeError(w, http.StatusForbidden, CodeBadHost, "cabecera Host no permitida")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			ok, cross := g.originAllowed(r, origin)
			if ext := isExtensionOrigin(normalizeOrigin(origin)); ext {
				ok, cross = true, true
				r.Header.Del("Cookie")
				if strings.HasPrefix(r.URL.Path, "/api/v1/auth/login") {
					writeError(w, http.StatusForbidden, CodeBadOrigin, "las extensiones usan el token de la API, no la sesión")
					return
				}
			}
			if !ok {
				writeError(w, http.StatusForbidden, CodeBadOrigin, "origen no permitido")
				return
			}
			if cross {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Expose-Headers", "Retry-After")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Last-Event-ID, "+CSRFHeader)
					h.Set("Access-Control-Max-Age", "600")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
