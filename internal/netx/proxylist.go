package netx

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// SessionPlaceholder in a proxy username is replaced by a random session id
// per egress instance (residential providers pin the exit IP to it, e.g.
// "customer-abc-session-{session}"). Rotate draws a new session id, which
// gives a new exit IP.
const SessionPlaceholder = "{session}"

// MaxProxyListSize caps proxy list files and downloads.
const MaxProxyListSize = 4 << 20

// MaxSessionsPerProxy caps the number of sticky-session instances of a line.
const MaxSessionsPerProxy = 256

// ProxySpec is one parsed proxy list entry.
type ProxySpec struct {
	Scheme   string // "http", "https" or "socks5" (socks5h is normalized to socks5)
	Host     string
	Port     string
	Username string // may contain SessionPlaceholder
	Password string
	// Sessions is the number of egress instances created from this entry
	// (only > 1 when Username contains SessionPlaceholder). Set with a
	// "#sessions=N" (or "?sessions=N") suffix on the line.
	Sessions int
}

// Kind returns KindSOCKS5 or KindHTTPProxy.
func (p ProxySpec) Kind() Kind {
	if p.Scheme == "socks5" {
		return KindSOCKS5
	}
	return KindHTTPProxy
}

// Addr returns host:port.
func (p ProxySpec) Addr() string { return net.JoinHostPort(p.Host, p.Port) }

// HasSessionTemplate reports whether the username has SessionPlaceholder.
func (p ProxySpec) HasSessionTemplate() bool {
	return strings.Contains(p.Username, SessionPlaceholder)
}

// Redacted renders the entry without its password: scheme://user@host:port.
func (p ProxySpec) Redacted() string {
	u := url.URL{Scheme: p.Scheme, Host: p.Addr()}
	if p.Username != "" {
		u.User = url.User(p.Username)
	}
	s := u.String()
	// url.User escapes the braces of the placeholder; show it verbatim.
	return strings.ReplaceAll(s, url.PathEscape(SessionPlaceholder), SessionPlaceholder)
}

// String implements fmt.Stringer without revealing the password.
func (p ProxySpec) String() string { return p.Redacted() }

// GoString implements fmt.GoStringer without revealing the password.
func (p ProxySpec) GoString() string { return "netx.ProxySpec(" + p.Redacted() + ")" }

// BaseID is the stable egress ID of the entry: "<scheme>:<host>:<port>",
// plus "@<hash>" of the username when there is one (so two accounts on the
// same gateway are different egresses without putting usernames in IDs).
func (p ProxySpec) BaseID() string {
	id := p.Scheme + ":" + p.Addr()
	if p.Username != "" {
		sum := sha256.Sum256([]byte(p.Username))
		id += "@" + hex.EncodeToString(sum[:4])
	}
	return id
}

// InstanceID is the ID of the i-th instance (0-based). A single-instance
// entry uses BaseID.
func (p ProxySpec) InstanceID(i int) string {
	if p.Sessions <= 1 && !p.HasSessionTemplate() {
		return p.BaseID()
	}
	return p.BaseID() + "#" + strconv.Itoa(i)
}

// Instances returns max(1, Sessions).
func (p ProxySpec) Instances() int {
	if p.Sessions < 1 {
		return 1
	}
	return p.Sessions
}

// LineError is a proxy list line that could not be parsed.
type LineError struct {
	Line int    `json:"line"`
	Text string `json:"text"` // redacted (no password)
	Err  string `json:"error"`
}

// ParseProxyList parses a proxy list: one entry per line, blank lines and
// comments ("#", ";" or "//" at the start, or " #" after an entry) ignored.
// Accepted forms:
//
//	scheme://user:pass@host:port   (http, https, socks5, socks5h, socks)
//	user:pass@host:port            (http)
//	host:port                      (http)
//	host:port:user:pass            (http; the common "provider export" form)
//
// An optional "#sessions=N" suffix (no space before '#') creates N sticky
// session instances when the username contains SessionPlaceholder.
// Duplicate entries (same BaseID) are dropped.
func ParseProxyList(text string) ([]ProxySpec, []LineError) {
	var specs []ProxySpec
	var errs []LineError
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	n := 0
	for sc.Scan() {
		n++
		line := stripComment(sc.Text())
		if line == "" {
			continue
		}
		spec, err := ParseProxyLine(line)
		if err != nil {
			errs = append(errs, LineError{Line: n, Text: redactLine(line), Err: err.Error()})
			continue
		}
		if seen[spec.BaseID()] {
			continue
		}
		seen[spec.BaseID()] = true
		specs = append(specs, spec)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, LineError{Line: n + 1, Err: err.Error()})
	}
	return specs, errs
}

func stripComment(line string) string {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if line == "" || line[0] == '#' || line[0] == ';' || strings.HasPrefix(line, "//") {
		return ""
	}
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		rest := strings.TrimSpace(line[i:])
		if rest == "" || rest[0] == '#' || rest[0] == ';' || strings.HasPrefix(rest, "//") {
			line = line[:i]
		}
	}
	return strings.TrimSpace(line)
}

// redactLine hides what looks like a password in an unparsable line.
func redactLine(line string) string {
	if i := strings.Index(line, "://"); i >= 0 {
		if at := strings.LastIndex(line, "@"); at > i {
			return line[:i+3] + "***@" + line[at+1:]
		}
		return line
	}
	if at := strings.LastIndex(line, "@"); at >= 0 {
		return "***@" + line[at+1:]
	}
	if parts := strings.Split(line, ":"); len(parts) >= 4 {
		return parts[0] + ":" + parts[1] + ":***"
	}
	return line
}

// ParseProxyLine parses one entry (see ParseProxyList).
func ParseProxyLine(line string) (ProxySpec, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return ProxySpec{}, errors.New("empty entry")
	}
	var spec ProxySpec
	sessions := 0
	if i := strings.LastIndexByte(line, '#'); i >= 0 && strings.HasPrefix(strings.ToLower(line[i+1:]), "sessions") {
		n, err := parseSessions(line[i+1:])
		if err != nil {
			return ProxySpec{}, err
		}
		line, sessions = line[:i], n
	}

	if strings.Contains(line, "://") {
		// url.Parse rejects braces in the userinfo: escape the placeholder
		// (Username() decodes it back).
		u, err := url.Parse(strings.ReplaceAll(line, SessionPlaceholder, "%7Bsession%7D"))
		if err != nil {
			return ProxySpec{}, errors.New("invalid URL")
		}
		switch strings.ToLower(u.Scheme) {
		case "http":
			spec.Scheme = "http"
		case "https":
			spec.Scheme = "https"
		case "socks5", "socks5h", "socks":
			spec.Scheme = "socks5"
		default:
			return ProxySpec{}, fmt.Errorf("unsupported scheme %q (want http, https, socks5 or socks5h)", u.Scheme)
		}
		if u.Path != "" && u.Path != "/" {
			return ProxySpec{}, errors.New("unexpected path in proxy URL")
		}
		if q := u.Query().Get("sessions"); q != "" && sessions == 0 {
			n, err := parseSessions("sessions=" + q)
			if err != nil {
				return ProxySpec{}, err
			}
			sessions = n
		}
		spec.Host, spec.Port = u.Hostname(), u.Port()
		if u.User != nil {
			spec.Username = u.User.Username()
			spec.Password, _ = u.User.Password()
		}
	} else {
		spec.Scheme = "http"
		hostport := line
		if at := strings.LastIndex(line, "@"); at >= 0 {
			user, pass, _ := strings.Cut(line[:at], ":")
			spec.Username, spec.Password = user, pass
			hostport = line[at+1:]
		} else if !strings.HasPrefix(line, "[") {
			parts := strings.Split(line, ":")
			switch len(parts) {
			case 2:
			case 4:
				hostport = parts[0] + ":" + parts[1]
				spec.Username, spec.Password = parts[2], parts[3]
			default:
				return ProxySpec{}, errors.New("want host:port, host:port:user:pass or scheme://user:pass@host:port")
			}
		}
		h, p, err := net.SplitHostPort(hostport)
		if err != nil {
			return ProxySpec{}, errors.New("invalid host:port")
		}
		spec.Host, spec.Port = h, p
	}
	if spec.Host == "" {
		return ProxySpec{}, errors.New("missing host")
	}
	if strings.ContainsAny(spec.Host, " /\\") {
		return ProxySpec{}, errors.New("invalid host")
	}
	port, err := strconv.Atoi(spec.Port)
	if err != nil || port < 1 || port > 65535 {
		return ProxySpec{}, errors.New("missing or invalid port")
	}
	if len(spec.Username) > 255 || len(spec.Password) > 255 {
		return ProxySpec{}, errors.New("username or password too long")
	}
	if spec.Password != "" && spec.Username == "" && spec.Scheme == "socks5" {
		return ProxySpec{}, errors.New("SOCKS5 password without username")
	}
	if spec.HasSessionTemplate() {
		spec.Sessions = max(1, sessions)
	} else {
		spec.Sessions = 1
	}
	return spec, nil
}

func parseSessions(frag string) (int, error) {
	k, v, ok := strings.Cut(strings.TrimSpace(frag), "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(k), "sessions") {
		return 0, fmt.Errorf("unknown suffix %q (want #sessions=N)", "#"+frag)
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > MaxSessionsPerProxy {
		return 0, fmt.Errorf("sessions must be 1..%d", MaxSessionsPerProxy)
	}
	return n, nil
}

// ReadProxyFile reads a local proxy list (at most MaxProxyListSize bytes).
func ReadProxyFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("proxy list: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxProxyListSize+1))
	if err != nil {
		return "", fmt.Errorf("proxy list %s: %w", path, err)
	}
	if len(b) > MaxProxyListSize {
		return "", fmt.Errorf("proxy list %s: larger than %d bytes", path, MaxProxyListSize)
	}
	return string(b), nil
}

// FetchProxyList downloads a remote proxy list with client (build it with
// NewHTTPClient(NewDirect(), nil)). The URL is not included in errors since
// list URLs often embed API keys.
func FetchProxyList(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errors.New("proxy list: invalid URL")
	}
	req.Header.Set("Accept", "text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("proxy list: %s", SanitizeError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("proxy list: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxProxyListSize+1))
	if err != nil {
		return "", fmt.Errorf("proxy list: %s", SanitizeError(err))
	}
	if len(b) > MaxProxyListSize {
		return "", fmt.Errorf("proxy list: larger than %d bytes", MaxProxyListSize)
	}
	return string(b), nil
}

// NewProxyEgress builds the i-th instance of spec: an *HTTPProxy or a
// *SOCKS5.
func NewProxyEgress(spec ProxySpec, instance int) (Egress, error) {
	if spec.Kind() == KindSOCKS5 {
		return NewSOCKS5(spec.InstanceID(instance), spec)
	}
	return NewHTTPProxy(spec.InstanceID(instance), spec)
}

// newSessionID returns a random session id (10 chars, [a-z0-9]).
func newSessionID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:])
}

// proxyCreds holds the credentials of a proxy egress and its current
// sticky session.
type proxyCreds struct {
	user     string // template
	password string
	session  string
}

func newProxyCreds(spec ProxySpec) proxyCreds {
	c := proxyCreds{user: spec.Username, password: spec.Password}
	if spec.HasSessionTemplate() {
		c.session = newSessionID()
	}
	return c
}

func (c proxyCreds) username() string {
	if c.session == "" {
		return c.user
	}
	return strings.ReplaceAll(c.user, SessionPlaceholder, c.session)
}
