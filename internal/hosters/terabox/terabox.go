package terabox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin identifier.
const Name = "terabox"

// Plugin defaults.
const (
	DefaultRetryAfter        = 30 * time.Minute
	DefaultMaxConnsPerFile   = 4
	DefaultRequestsPerSecond = 2.0
	// dlinkTTL is how long a dlink from a listing is reused by Prepare.
	dlinkTTL = 20 * time.Minute
	// tokenTTL is how long the share page tokens are reused.
	tokenTTL = 20 * time.Minute
	// cdnTTL is the Expires given to CDN URLs (TeraBox does not say; the
	// downloader simply calls Prepare again).
	cdnTTL = time.Hour
)

// Options understood in [hosters.terabox.options]:
//
//	base_url         send page and API requests to this base URL instead of
//	                 the link's host (e.g. "https://www.1024tera.com"; tests)
//	account_url      base URL for account checks (default https://www.terabox.com)
//	replace_domains  "true": [hosters.terabox].domains replaces the defaults
//	page_size        /share/list page size (default 100)
//	verify_md5       "true": advertise the listing md5 as checksum (off by
//	                 default: TeraBox md5 values are often not the real file md5)
//	selftest_link    public share used by `adesc selftest terabox`
const (
	OptBaseURL        = "base_url"
	OptAccountURL     = "account_url"
	OptReplaceDomains = "replace_domains"
	OptPageSize       = "page_size"
	OptVerifyMD5      = "verify_md5"
	OptSelfTestLink   = "selftest_link"
)

func init() { hosters.RegisterFactory(Name, New) }

// Plugin implements hosters.Hoster for TeraBox and its mirrors.
type Plugin struct {
	domains      []string
	base         string // optional override for page/API requests
	accountBase  string
	retryAfter   time.Duration
	maxConns     int
	maxParallel  int
	pageSize     int
	verifyMD5    bool
	selfTestLink string
	limiter      *rate.Limiter

	stateMu sync.Mutex
}

var (
	_ hosters.Hoster            = (*Plugin)(nil)
	_ hosters.StreamingResolver = (*Plugin)(nil)
	_ hosters.AccountChecker    = (*Plugin)(nil)
	_ hosters.SelfTester        = (*Plugin)(nil)
)

// New builds the plugin from its [hosters.terabox] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	p := &Plugin{
		accountBase: "https://www.terabox.com",
		retryAfter:  DefaultRetryAfter,
		maxConns:    DefaultMaxConnsPerFile,
		maxParallel: cfg.MaxParallel,
		pageSize:    defaultPageSize,
	}
	if cfg.Options[OptReplaceDomains] != "true" || len(cfg.Domains) == 0 {
		p.domains = append(p.domains, DefaultDomains...)
	}
	for _, d := range cfg.Domains {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "www.")
		if d != "" {
			p.domains = append(p.domains, d)
		}
	}
	if cfg.RetryAfter.Duration > 0 {
		p.retryAfter = cfg.RetryAfter.Duration
	}
	if cfg.MaxConnsPerFile > 0 {
		p.maxConns = cfg.MaxConnsPerFile
	}
	for opt, dst := range map[string]*string{OptBaseURL: &p.base, OptAccountURL: &p.accountBase} {
		if v := strings.TrimSpace(cfg.Options[opt]); v != "" {
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("terabox: invalid option %s", opt)
			}
			*dst = strings.TrimRight(v, "/")
		}
	}
	if v := strings.TrimSpace(cfg.Options[OptPageSize]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return nil, errors.New("terabox: invalid option page_size (1-1000)")
		}
		p.pageSize = n
	}
	p.verifyMD5 = cfg.Options[OptVerifyMD5] == "true"
	p.selfTestLink = strings.TrimSpace(cfg.Options[OptSelfTestLink])
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = DefaultRequestsPerSecond
	}
	p.limiter = rate.NewLimiter(rate.Limit(rps), max(1, int(rps)))
	return p, nil
}

// Name implements hosters.Hoster.
func (p *Plugin) Name() string { return Name }

// Match implements hosters.Hoster: share links on any configured domain.
func (p *Plugin) Match(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || !hostMatches(u.Host, p.domains) {
		return false
	}
	_, err := parseShareLink(u)
	return err == nil
}

// Caps implements hosters.Hoster.
func (p *Plugin) Caps() hosters.Caps {
	return hosters.Caps{
		MaxConnsPerFile:   p.maxConns,
		MaxParallel:       p.maxParallel,
		Resumable:         true,
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindCookie},
		DefaultRetryAfter: p.retryAfter,
		QuotaScope:        hosters.ScopeIP,
	}
}

// APIError is a non-zero errno returned by a TeraBox API.
type APIError struct {
	Errno int
	Msg   string // server message (errmsg / show_msg), may be empty
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("terabox errno %d (%s)", e.Errno, e.Msg)
	}
	return fmt.Sprintf("terabox errno %d", e.Errno)
}

// ruleFor finds the rule for an errno (table, then message heuristics).
func ruleFor(errno int, msg string) errnoRule {
	if r, ok := errnoRules[errno]; ok {
		return r
	}
	for _, m := range errmsgRules {
		if m.re.MatchString(msg) {
			return m.rule
		}
	}
	return errnoRule{kind: hosters.KindFatal, msg: "request rejected"}
}

// errnoError maps an errno to a hosters outcome.
func (p *Plugin) errnoError(e *APIError) error {
	r := ruleFor(e.Errno, e.Msg)
	cause := fmt.Errorf("terabox: %s: %w", r.msg, e)
	switch r.kind {
	case hosters.KindQuota:
		return hosters.NewQuota(r.scope, p.retryAfter, cause)
	case hosters.KindNeedsAuth:
		return hosters.NewNeedsAuth(cause)
	case hosters.KindNotFound:
		return hosters.NewNotFound(cause)
	case hosters.KindTemporary:
		return hosters.NewTemporary(0, cause)
	case hosters.KindExpired:
		return hosters.NewExpired(cause)
	default:
		return hosters.NewFatal(cause)
	}
}

// apiErrorFromBody extracts {"errno": n, "errmsg"/"show_msg": ...} from a
// JSON body; ok is false if body is not such an object or errno is 0.
func apiErrorFromBody(body []byte) (*APIError, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}
	var e struct {
		Errno   *flexInt `json:"errno"`
		Errmsg  string   `json:"errmsg"`
		ShowMsg string   `json:"show_msg"`
	}
	if json.Unmarshal(body, &e) != nil || e.Errno == nil || *e.Errno == 0 {
		return nil, false //nolint:nilerr // a body that is not JSON is not an API error
	}
	msg := e.ShowMsg
	if msg == "" {
		msg = e.Errmsg
	}
	return &APIError{Errno: int(*e.Errno), Msg: truncate(msg, 200)}, true
}

// Classify implements hosters.Hoster. JSON bodies carrying a non-zero errno
// are mapped with errnoRules; everything else uses DefaultClassify (CDN 403
// = expired link, so the engine calls Prepare again). Transport errors are
// stripped of their URL (signed CDN links).
func (p *Plugin) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil {
		return hosters.DefaultClassify(nil, nil, scrubURLError(err))
	}
	if resp != nil && (resp.StatusCode >= 400 || isJSON(resp)) {
		if ae, ok := apiErrorFromBody(body); ok {
			return p.errnoError(ae)
		}
	}
	return hosters.DefaultClassify(resp, body, nil)
}

func isJSON(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "json") || strings.Contains(ct, "javascript")
}

// scrubURLError removes the request URL from transport errors.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request: %w", ue.Op, ue.Err)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// hostMatches reports whether host is one of domains or a subdomain.
func hostMatches(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func splitHostPort(h string) (string, string, error) {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		return h[:i], h[i+1:], nil
	}
	return h, "", errors.New("no port")
}

// flexInt decodes a JSON number or a numeric string.
type flexInt int64

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if s == "true" || s == "false" {
		*f = map[bool]flexInt{true: 1, false: 0}[s == "true"]
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		fl, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return fmt.Errorf("terabox: not a number: %q", truncate(s, 32))
		}
		n = int64(fl)
	}
	*f = flexInt(n)
	return nil
}

// flexString decodes a JSON string or number (fs_id exceeds float64
// precision on some accounts, so numbers are kept verbatim).
type flexString string

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flexString(b)
	return nil
}
