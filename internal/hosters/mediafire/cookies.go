package mediafire

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// parseCookies accepts the formats users paste into a cookie account:
//
//   - Netscape cookies.txt (tab separated, "#HttpOnly_" prefixes allowed);
//   - a JSON array of {name, value, domain, path, secure, expirationDate}
//     objects (browser extension exports), or {"cookies": [...]};
//   - a raw "name=value; name2=value2" header (domain = defaultDomain).
//
// Cookies without a domain get defaultDomain.
func parseCookies(raw, defaultDomain string) ([]*http.Cookie, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if raw == "" {
		return nil, errors.New("empty cookie data")
	}
	var out []*http.Cookie
	var err error
	switch {
	case strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{"):
		out, err = parseJSONCookies(raw)
	case strings.Contains(raw, "\t"):
		out = parseNetscape(raw)
	default:
		out = parseHeaderCookies(raw)
	}
	if err != nil {
		return nil, err
	}
	for _, c := range out {
		if c.Domain == "" {
			c.Domain = defaultDomain
		}
		if c.Path == "" {
			c.Path = "/"
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no cookies found")
	}
	return out, nil
}

func parseNetscape(raw string) []*http.Cookie {
	var out []*http.Cookie
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		httpOnly := false
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
			httpOnly = true
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		c := &http.Cookie{
			Domain:   f[0],
			Path:     f[2],
			Secure:   strings.EqualFold(f[3], "TRUE"),
			Name:     f[5],
			Value:    strings.Join(f[6:], "\t"),
			HttpOnly: httpOnly,
		}
		if exp, err := strconv.ParseInt(f[4], 10, 64); err == nil && exp > 0 {
			c.Expires = time.Unix(exp, 0)
		}
		if c.Name != "" {
			out = append(out, c)
		}
	}
	return out
}

type jsonCookie struct {
	Name           string   `json:"name"`
	Value          string   `json:"value"`
	Domain         string   `json:"domain"`
	Path           string   `json:"path"`
	Secure         bool     `json:"secure"`
	HTTPOnly       bool     `json:"httpOnly"`
	ExpirationDate *float64 `json:"expirationDate"`
	Expires        any      `json:"expires"`
}

func parseJSONCookies(raw string) ([]*http.Cookie, error) {
	var list []jsonCookie
	if strings.HasPrefix(raw, "{") {
		var wrap struct {
			Cookies []jsonCookie `json:"cookies"`
		}
		if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
			return nil, errors.New("invalid JSON cookie export")
		}
		list = wrap.Cookies
	} else if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, errors.New("invalid JSON cookie export")
	}
	out := make([]*http.Cookie, 0, len(list))
	for _, j := range list {
		if j.Name == "" {
			continue
		}
		c := &http.Cookie{Name: j.Name, Value: j.Value, Domain: j.Domain, Path: j.Path, Secure: j.Secure, HttpOnly: j.HTTPOnly}
		if j.ExpirationDate != nil && *j.ExpirationDate > 0 && *j.ExpirationDate < math.MaxInt32*4 {
			c.Expires = time.Unix(int64(*j.ExpirationDate), 0)
		} else if f, ok := j.Expires.(float64); ok && f > 0 {
			c.Expires = time.Unix(int64(f), 0)
		}
		out = append(out, c)
	}
	return out, nil
}

func parseHeaderCookies(raw string) []*http.Cookie {
	raw = strings.TrimPrefix(raw, "Cookie:")
	var out []*http.Cookie
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return out
}

// loadCookies stores cookies in jar, grouped by domain so that domain
// cookies (".mediafire.com") reach every subdomain, download servers
// included.
func loadCookies(jar http.CookieJar, cookies []*http.Cookie) int {
	byDomain := map[string][]*http.Cookie{}
	for _, c := range cookies {
		d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		if d == "" {
			continue
		}
		cc := *c
		cc.Domain = d
		byDomain[d] = append(byDomain[d], &cc)
	}
	n := 0
	for d, list := range byDomain {
		u := &url.URL{Scheme: "https", Host: d, Path: "/"}
		jar.SetCookies(u, list)
		n += len(list)
	}
	return n
}
