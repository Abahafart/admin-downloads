package accounts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Cookie import errors (wrapped with details; compare with errors.Is).
var (
	// ErrInvalidCookies means the text is neither a Netscape cookies.txt, a
	// JSON cookie export nor a "name=value; ..." header.
	ErrInvalidCookies = errors.New("accounts: unrecognized cookie format")
	// ErrNoMatchingCookies means no cookie belongs to the hoster's domains.
	ErrNoMatchingCookies = errors.New("accounts: no cookies for this hoster")
	// ErrCookiesExpired means every cookie for the hoster has expired.
	ErrCookiesExpired = errors.New("accounts: all cookies for this hoster have expired")
)

// NetscapeHeader is the first line of the normalized cookie text.
const NetscapeHeader = "# Netscape HTTP Cookie File"

// maxCookieInput bounds pasted text (a full browser export is far smaller).
const maxCookieInput = 4 << 20

// CookieImport is the outcome of ImportCookies.
type CookieImport struct {
	// Cookies are the accepted cookies (hoster domains only, not expired).
	Cookies []*http.Cookie
	// Normalized is the Netscape cookies.txt text to store in
	// Secret.Cookies.
	Normalized string
	// Format is "netscape", "json" or "header".
	Format string
	// Skipped counts cookies dropped because of another domain.
	Skipped int
	// Expired counts cookies dropped because they expired.
	Expired int
	// EarliestExpiry is the soonest expiry among accepted persistent
	// cookies (zero if all are session cookies).
	EarliestExpiry time.Time
}

// ImportCookies parses a pasted cookie export, keeps the cookies that belong
// to domains (e.g. the hoster's domain list; empty = keep all) and are not
// expired at now, and returns them with their normalized Netscape text.
//
// Accepted formats:
//   - Netscape cookies.txt (curl/wget/"Get cookies.txt" extensions), with
//     tabs or, if pasting replaced them, runs of spaces; "#HttpOnly_" lines;
//   - JSON arrays from EditThisCookie / Cookie-Editor ("expirationDate"),
//     Puppeteer/Selenium ("expires"/"expiry"), and Playwright storage state
//     ({"cookies": [...]});
//   - a raw "name=value; name2=value2" Cookie header (assigned to the first
//     domain, which is then required).
func ImportCookies(text string, domains []string, now time.Time) (*CookieImport, error) {
	if len(text) > maxCookieInput {
		return nil, fmt.Errorf("%w: input larger than %d bytes", ErrInvalidCookies, maxCookieInput)
	}
	text = strings.TrimPrefix(text, "\ufeff")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty input", ErrInvalidCookies)
	}
	var (
		all    []*http.Cookie
		format string
		err    error
	)
	switch {
	case trimmed[0] == '[' || trimmed[0] == '{':
		format = "json"
		all, err = parseJSONCookies([]byte(trimmed))
	case looksNetscape(trimmed):
		format = "netscape"
		all, err = parseNetscapeCookies(trimmed)
	default:
		format = "header"
		if len(domains) == 0 {
			return nil, fmt.Errorf("%w: a Cookie header needs a target domain", ErrInvalidCookies)
		}
		all, err = parseCookieHeader(trimmed, domains[0])
	}
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%w: no cookies found", ErrInvalidCookies)
	}

	res := &CookieImport{Format: format}
	var foreign []string
	for _, c := range all {
		if !domainMatchesAny(c.Domain, domains) {
			res.Skipped++
			if d := strings.TrimPrefix(c.Domain, "."); !slices.Contains(foreign, d) && len(foreign) < 5 {
				foreign = append(foreign, d)
			}
			continue
		}
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			res.Expired++
			continue
		}
		if !c.Expires.IsZero() && (res.EarliestExpiry.IsZero() || c.Expires.Before(res.EarliestExpiry)) {
			res.EarliestExpiry = c.Expires
		}
		res.Cookies = append(res.Cookies, c)
	}
	if len(res.Cookies) == 0 {
		if res.Expired > 0 {
			return nil, fmt.Errorf("%w (%d expired)", ErrCookiesExpired, res.Expired)
		}
		return nil, fmt.Errorf("%w: expected %s, found %s", ErrNoMatchingCookies,
			strings.Join(domains, ", "), strings.Join(foreign, ", "))
	}
	res.Cookies = dedupeCookies(res.Cookies)
	res.Normalized = FormatNetscape(res.Cookies)
	return res, nil
}

// ParseCookies parses stored cookie text (normally Secret.Cookies, which
// ImportCookies normalizes) without domain filtering, dropping expired
// cookies.
func ParseCookies(text string, now time.Time) ([]*http.Cookie, error) {
	r, err := ImportCookies(text, nil, now)
	if err != nil {
		return nil, err
	}
	return r.Cookies, nil
}

// LoadCookies parses stored cookie text and adds to jar the cookies that
// apply to u (domain match), returning how many were set. Plugins call it
// with Secret.Cookies and Session.Jar before the first request.
func LoadCookies(jar http.CookieJar, u *url.URL, text string, now time.Time) (int, error) {
	cs, err := ParseCookies(text, now)
	if err != nil {
		return 0, err
	}
	return SetJarCookies(jar, u, cs), nil
}

// SetJarCookies adds the cookies that domain-match u's host to jar. Host
// only cookies (no leading dot) are set for their exact host.
func SetJarCookies(jar http.CookieJar, u *url.URL, cs []*http.Cookie) int {
	host := strings.ToLower(u.Hostname())
	var set []*http.Cookie
	for _, c := range cs {
		d := strings.ToLower(c.Domain)
		hostOnly := !strings.HasPrefix(d, ".")
		d = strings.TrimPrefix(d, ".")
		if !(host == d || (!hostOnly && strings.HasSuffix(host, "."+d))) {
			continue
		}
		cc := *c
		if hostOnly {
			cc.Domain = "" // the jar then makes it host-only for u
		}
		set = append(set, &cc)
	}
	if len(set) > 0 {
		jar.SetCookies(u, set)
	}
	return len(set)
}

// RetargetCookies copies cs to another domain (e.g. a TeraBox mirror the
// user did not log in on), as domain cookies of domain.
func RetargetCookies(cs []*http.Cookie, domain string) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(cs))
	for _, c := range cs {
		cc := *c
		cc.Domain = "." + strings.TrimPrefix(strings.ToLower(domain), ".")
		out = append(out, &cc)
	}
	return out
}

// CookieHeader renders cs as a "name=value; ..." header value.
func CookieHeader(cs []*http.Cookie) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// FormatNetscape renders cookies as a Netscape cookies.txt document.
func FormatNetscape(cs []*http.Cookie) string {
	var b strings.Builder
	b.WriteString(NetscapeHeader)
	b.WriteString("\n")
	for _, c := range cs {
		domain := c.Domain
		if c.HttpOnly {
			domain = "#HttpOnly_" + domain
		}
		var exp int64
		if !c.Expires.IsZero() {
			exp = c.Expires.Unix()
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", domain, boolTF(strings.HasPrefix(c.Domain, ".")),
			path, boolTF(c.Secure), exp, c.Name, c.Value)
	}
	return b.String()
}

func boolTF(v bool) string {
	if v {
		return "TRUE"
	}
	return "FALSE"
}

// domainMatchesAny reports whether a cookie for cookieDomain is relevant to
// one of domains: same domain, a subdomain of it, or a parent domain cookie
// (".example.com" applies to "www.example.com").
func domainMatchesAny(cookieDomain string, domains []string) bool {
	if len(domains) == 0 {
		return true
	}
	cd := strings.TrimPrefix(strings.ToLower(cookieDomain), ".")
	parent := strings.HasPrefix(cookieDomain, ".")
	for _, d := range domains {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" {
			continue
		}
		if cd == d || strings.HasSuffix(cd, "."+d) || (parent && strings.HasSuffix(d, "."+cd)) {
			return true
		}
	}
	return false
}

func dedupeCookies(cs []*http.Cookie) []*http.Cookie {
	type key struct{ d, p, n string }
	idx := map[key]int{}
	var out []*http.Cookie
	for _, c := range cs {
		k := key{strings.ToLower(c.Domain), c.Path, c.Name}
		if i, ok := idx[k]; ok {
			out[i] = c // last one wins
			continue
		}
		idx[k] = len(out)
		out = append(out, c)
	}
	return out
}

func looksNetscape(s string) bool {
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 64<<10), maxCookieInput)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#HttpOnly_") {
			return true
		}
		if strings.HasPrefix(line, "#") {
			if strings.Contains(strings.ToLower(line), "netscape") {
				return true
			}
			continue
		}
		f := strings.Fields(line)
		return len(f) >= 6 && isTF(f[1]) && isTF(f[3])
	}
	return false
}

func isTF(s string) bool { return strings.EqualFold(s, "TRUE") || strings.EqualFold(s, "FALSE") }

func parseNetscapeCookies(s string) ([]*http.Cookie, error) {
	var out []*http.Cookie
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 64<<10), maxCookieInput)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		httpOnly := false
		if rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "#HttpOnly_"); ok {
			line, httpOnly = rest, true
		} else if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 || len(f) > 7 {
			// Pasting from a web page often turns tabs into spaces.
			f = strings.Fields(line)
			if len(f) > 7 {
				f = append(f[:6], strings.Join(f[6:], " "))
			}
		}
		if len(f) == 6 {
			f = append(f, "") // empty value
		}
		if len(f) != 7 {
			return nil, fmt.Errorf("%w: cookies.txt line %d has %d fields, expected 7", ErrInvalidCookies, n, len(f))
		}
		domain := strings.TrimSpace(f[0])
		if domain == "" || !isTF(f[1]) || !isTF(f[3]) {
			return nil, fmt.Errorf("%w: cookies.txt line %d is malformed", ErrInvalidCookies, n)
		}
		exp, err := strconv.ParseFloat(strings.TrimSpace(f[4]), 64)
		if err != nil || math.IsNaN(exp) {
			return nil, fmt.Errorf("%w: cookies.txt line %d has an invalid expiry %q", ErrInvalidCookies, n, f[4])
		}
		c := &http.Cookie{
			Domain:   normalizeDomain(domain, strings.EqualFold(f[1], "TRUE")),
			Path:     f[2],
			Secure:   strings.EqualFold(f[3], "TRUE"),
			Expires:  unixToTime(exp),
			Name:     f[5],
			Value:    f[6],
			HttpOnly: httpOnly,
		}
		if err := validateCookie(c); err != nil {
			return nil, fmt.Errorf("%w: cookies.txt line %d: %v", ErrInvalidCookies, n, err)
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCookies, err)
	}
	return out, nil
}

// normalizeDomain returns ".example.com" for domain cookies and
// "example.com" for host-only ones.
func normalizeDomain(d string, includeSubdomains bool) string {
	d = strings.ToLower(strings.TrimSpace(d))
	if includeSubdomains || strings.HasPrefix(d, ".") {
		return "." + strings.TrimPrefix(d, ".")
	}
	return d
}

// unixToTime converts seconds (possibly fractional) since the epoch; 0 and
// negative values mean a session cookie. Millisecond timestamps (some
// exporters) are detected by magnitude.
func unixToTime(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	if v > 1e11 { // > year 5138 in seconds: must be milliseconds
		v /= 1000
	}
	if v > 253402300799 { // after 9999-12-31: clamp
		v = 253402300799
	}
	sec, frac := math.Modf(v)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// jsonCookie covers EditThisCookie, Cookie-Editor, Puppeteer, Selenium and
// Playwright field names.
type jsonCookie struct {
	Name           *string         `json:"name"`
	Value          *string         `json:"value"`
	Domain         string          `json:"domain"`
	Host           string          `json:"host"`
	Path           string          `json:"path"`
	Secure         bool            `json:"secure"`
	HTTPOnly       bool            `json:"httpOnly"`
	HostOnly       *bool           `json:"hostOnly"`
	Session        bool            `json:"session"`
	ExpirationDate json.RawMessage `json:"expirationDate"`
	Expires        json.RawMessage `json:"expires"`
	Expiry         json.RawMessage `json:"expiry"`
	SameSite       *string         `json:"sameSite"`
}

func parseJSONCookies(b []byte) ([]*http.Cookie, error) {
	var arr []jsonCookie
	if b[0] == '{' {
		var wrap struct {
			Cookies []jsonCookie `json:"cookies"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		if err := dec.Decode(&wrap); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidCookies, err)
		}
		if wrap.Cookies == nil {
			return nil, fmt.Errorf("%w: JSON object without a \"cookies\" array", ErrInvalidCookies)
		}
		arr = wrap.Cookies
	} else if err := json.Unmarshal(b, &arr); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidCookies, err)
	}
	out := make([]*http.Cookie, 0, len(arr))
	for i, j := range arr {
		if j.Name == nil || j.Value == nil {
			return nil, fmt.Errorf("%w: JSON cookie #%d lacks name or value", ErrInvalidCookies, i+1)
		}
		domain := j.Domain
		if domain == "" {
			domain = j.Host
		}
		if strings.TrimSpace(domain) == "" {
			return nil, fmt.Errorf("%w: JSON cookie #%d (%s) has no domain", ErrInvalidCookies, i+1, *j.Name)
		}
		includeSub := strings.HasPrefix(domain, ".")
		if j.HostOnly != nil {
			includeSub = !*j.HostOnly
		}
		var exp time.Time
		if !j.Session {
			for _, raw := range []json.RawMessage{j.ExpirationDate, j.Expires, j.Expiry} {
				t, ok, err := parseJSONExpiry(raw)
				if err != nil {
					return nil, fmt.Errorf("%w: JSON cookie #%d (%s): %v", ErrInvalidCookies, i+1, *j.Name, err)
				}
				if ok {
					exp = t
					break
				}
			}
		}
		path := j.Path
		if path == "" {
			path = "/"
		}
		c := &http.Cookie{
			Name:     *j.Name,
			Value:    *j.Value,
			Domain:   normalizeDomain(domain, includeSub),
			Path:     path,
			Secure:   j.Secure,
			HttpOnly: j.HTTPOnly,
			Expires:  exp,
		}
		if j.SameSite != nil {
			switch strings.ToLower(*j.SameSite) {
			case "lax":
				c.SameSite = http.SameSiteLaxMode
			case "strict":
				c.SameSite = http.SameSiteStrictMode
			case "none", "no_restriction":
				c.SameSite = http.SameSiteNoneMode
			}
		}
		if err := validateCookie(c); err != nil {
			return nil, fmt.Errorf("%w: JSON cookie #%d: %v", ErrInvalidCookies, i+1, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// parseJSONExpiry accepts a number (seconds or ms; <= 0 = session), a
// numeric string or an RFC 3339 / HTTP date string. ok is false when absent.
func parseJSONExpiry(raw json.RawMessage) (time.Time, bool, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}, false, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return unixToTime(f), true, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return time.Time{}, false, fmt.Errorf("invalid expiry %s", s)
	}
	str = strings.TrimSpace(str)
	if str == "" || strings.EqualFold(str, "session") {
		return time.Time{}, true, nil
	}
	if f, err := strconv.ParseFloat(str, 64); err == nil {
		return unixToTime(f), true, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123, http.TimeFormat, "Mon, 02-Jan-2006 15:04:05 MST"} {
		if t, err := time.Parse(layout, str); err == nil {
			return t.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("invalid expiry %q", str)
}

func parseCookieHeader(s, domain string) ([]*http.Cookie, error) {
	s = strings.TrimSpace(s)
	if name, rest, ok := strings.Cut(s, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "cookie") {
		s = rest
	}
	if strings.ContainsAny(s, "\n\r") {
		return nil, fmt.Errorf("%w: expected cookies.txt, a JSON export or a single \"name=value; ...\" line", ErrInvalidCookies)
	}
	var out []*http.Cookie
	for part := range strings.SplitSeq(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%w: %q is not name=value", ErrInvalidCookies, truncateForError(part))
		}
		c := &http.Cookie{
			Name:   strings.TrimSpace(name),
			Value:  strings.Trim(strings.TrimSpace(value), `"`),
			Domain: normalizeDomain(domain, true),
			Path:   "/",
		}
		if err := validateCookie(c); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCookies, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// truncateForError keeps error messages short (they never include values
// of valid cookies, only of unparsable fragments).
func truncateForError(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

func validateCookie(c *http.Cookie) error {
	if c.Name == "" {
		return errors.New("empty cookie name")
	}
	for _, r := range c.Name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return fmt.Errorf("invalid character in cookie name %q", truncateForError(c.Name))
		}
	}
	for _, r := range c.Value {
		if r < 0x20 || r == 0x7f || r == ';' {
			return fmt.Errorf("invalid character in the value of cookie %q", c.Name)
		}
	}
	d := strings.TrimPrefix(c.Domain, ".")
	if d == "" || strings.ContainsAny(d, " /\\:\t") {
		return fmt.Errorf("invalid domain for cookie %q", c.Name)
	}
	if c.Path != "" && !strings.HasPrefix(c.Path, "/") {
		return fmt.Errorf("invalid path for cookie %q", c.Name)
	}
	return nil
}
