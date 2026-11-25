package terabox

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// parseCookies reads account cookies pasted by the user in any of these
// forms and returns name/value pairs:
//
//   - Netscape cookies.txt (tab separated, "#HttpOnly_" prefixes allowed);
//   - a JSON array of {"name","value","domain"} objects (browser exports)
//     or a JSON object {"name": "value"};
//   - a "k=v; k2=v2" header string (optionally prefixed by "Cookie:").
//
// Cookies whose domain is known and is not a TeraBox domain are dropped so a
// full browser export never leaks unrelated cookies to TeraBox.
func parseCookies(raw string, domains []string) ([]*http.Cookie, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "\ufeff"))
	if raw == "" {
		return nil, nil
	}
	var pairs [][2]string
	add := func(domain, name, value string) {
		domain = strings.TrimPrefix(strings.TrimSpace(domain), ".")
		if domain != "" && !hostMatches(domain, domains) {
			return
		}
		name, value = strings.TrimSpace(name), strings.Trim(strings.TrimSpace(value), `"`)
		if !validCookieName(name) || !validCookieValue(value) {
			return
		}
		pairs = append(pairs, [2]string{name, value})
	}
	switch {
	case raw[0] == '[':
		var list []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return nil, errors.New("terabox: cookies: invalid JSON array")
		}
		for _, c := range list {
			add(c.Domain, c.Name, c.Value)
		}
	case raw[0] == '{':
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, errors.New("terabox: cookies: invalid JSON object")
		}
		for k, v := range m {
			add("", k, v)
		}
	case strings.Contains(raw, "\t"):
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimRight(line, "\r")
			line = strings.TrimPrefix(line, "#HttpOnly_")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) < 7 {
				continue
			}
			add(f[0], f[5], f[6])
		}
	default:
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Cookie:"))
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' }) {
			k, v, ok := strings.Cut(part, "=")
			if ok {
				add("", k, v)
			}
		}
	}
	// Dedupe by name, last value wins.
	idx := map[string]int{}
	var out []*http.Cookie
	for _, p := range pairs {
		if i, ok := idx[p[0]]; ok {
			out[i].Value = p[1]
			continue
		}
		idx[p[0]] = len(out)
		out = append(out, &http.Cookie{Name: p[0], Value: p[1], Path: "/"})
	}
	return out, nil
}

func validCookieName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte("()<>@,;:\\\"/[]?={}", c) >= 0 {
			return false
		}
	}
	return true
}

func validCookieValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '"' || c == ';' || c == '\\' || c == ',' {
			return false
		}
	}
	return true
}

func hasCookie(cs []*http.Cookie, name string) bool {
	for _, c := range cs {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}
