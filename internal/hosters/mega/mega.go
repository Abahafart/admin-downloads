package mega

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin identifier.
const Name = "mega"

// Plugin defaults.
const (
	DefaultRetryAfter      = time.Hour
	DefaultMaxConnsPerFile = 4
)

// Options understood in [hosters.mega.options]:
//
//	api_url        API base URL (default https://g.api.mega.co.nz; tests)
//	ssl            "false" asks for http:// storage URLs (content is
//	               encrypted anyway; some networks are faster), default true
//	selftest_link  public file or folder link used by `adesc selftest mega`
//	hashcash_timeout  max time to solve a 402 proof-of-work (Go duration,
//	               default 2m)
const (
	OptAPIURL          = "api_url"
	OptSSL             = "ssl"
	OptSelfTestLink    = "selftest_link"
	OptHashcashTimeout = "hashcash_timeout"
)

func init() { hosters.RegisterFactory(Name, New) }

// Plugin implements hosters.Hoster for MEGA. It is safe for concurrent use;
// login state lives in the Session.
type Plugin struct {
	domains         []string
	apiURL          string
	ssl             bool
	retryAfter      time.Duration
	maxConns        int
	maxParallel     int
	selfTestLink    string
	hashcashTimeout time.Duration
	apiRetries      int

	seq     atomic.Uint64
	loginMu sync.Mutex
}

var (
	_ hosters.Hoster            = (*Plugin)(nil)
	_ hosters.StreamingResolver = (*Plugin)(nil)
	_ hosters.AccountChecker    = (*Plugin)(nil)
	_ hosters.SelfTester        = (*Plugin)(nil)
)

// New builds the plugin from its [hosters.mega] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	p := &Plugin{
		apiURL:          DefaultAPIURL,
		ssl:             true,
		retryAfter:      DefaultRetryAfter,
		maxConns:        DefaultMaxConnsPerFile,
		maxParallel:     cfg.MaxParallel,
		hashcashTimeout: 2 * time.Minute,
		apiRetries:      4,
	}
	p.domains = append(p.domains, defaultDomains...)
	for _, d := range cfg.Domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			p.domains = append(p.domains, strings.TrimPrefix(d, "www."))
		}
	}
	if cfg.RetryAfter.Duration > 0 {
		p.retryAfter = cfg.RetryAfter.Duration
	}
	if cfg.MaxConnsPerFile > 0 {
		p.maxConns = cfg.MaxConnsPerFile
	}
	if v := strings.TrimSpace(cfg.Options[OptAPIURL]); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, errors.New("mega: invalid option api_url")
		}
		p.apiURL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(cfg.Options[OptSSL]); v == "false" || v == "0" {
		p.ssl = false
	}
	p.selfTestLink = strings.TrimSpace(cfg.Options[OptSelfTestLink])
	if v := strings.TrimSpace(cfg.Options[OptHashcashTimeout]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, errors.New("mega: invalid option hashcash_timeout")
		}
		p.hashcashTimeout = d
	}
	var seed [8]byte
	_, _ = rand.Read(seed[:])
	p.seq.Store(binary.LittleEndian.Uint64(seed[:]) >> 34)
	return p, nil
}

// Name implements hosters.Hoster.
func (p *Plugin) Name() string { return Name }

// Match implements hosters.Hoster: any file/folder/embed link (new and
// legacy formats) on a MEGA domain. It does no I/O.
func (p *Plugin) Match(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || !hostMatches(u.Host, p.domains) {
		return false
	}
	_, err := parseLink(u)
	return err == nil || errors.Is(err, errPasswordLink)
}

// Caps implements hosters.Hoster.
func (p *Plugin) Caps() hosters.Caps {
	return hosters.Caps{
		MaxConnsPerFile:   p.maxConns,
		MaxParallel:       p.maxParallel,
		Resumable:         true,
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindPassword},
		DefaultRetryAfter: p.retryAfter,
		QuotaScope:        hosters.ScopeIP,
	}
}

// Classify implements hosters.Hoster:
//
//	509 (storage bandwidth quota)  -> Quota{IP}, X-MEGA-Time-Left / Retry-After
//	                                   or the configured retry_after
//	API body with an error code    -> mapped like Resolve/Prepare errors
//	anything else                  -> hosters.DefaultClassify
//
// Transport errors are stripped of their URL (it holds the sid or a signed
// storage URL) before being returned.
func (p *Plugin) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil {
		return hosters.DefaultClassify(nil, nil, scrubURLError(err))
	}
	if resp == nil {
		return hosters.DefaultClassify(nil, nil, nil)
	}
	if resp.StatusCode == 509 {
		ra := parseTimeLeft(resp.Header)
		if ra <= 0 {
			ra = p.retryAfter
		}
		return hosters.NewQuota(hosters.ScopeIP, ra, errors.New("mega: transfer quota exceeded for this IP (HTTP 509)"))
	}
	if resp.StatusCode == http.StatusOK && isAPIResponse(resp) && len(body) > 0 && len(body) < 256 {
		if _, aerr := parseAPIResponse(body); aerr != nil {
			var ae *APIError
			if errors.As(aerr, &ae) {
				return p.linkError(ae, hosters.ScopeIP)
			}
		}
		return nil
	}
	return hosters.DefaultClassify(resp, body, nil)
}

// isAPIResponse reports whether resp comes from the JSON API (as opposed to
// a storage server whose body is ciphertext).
func isAPIResponse(resp *http.Response) bool {
	if resp.Request != nil && resp.Request.URL != nil {
		return strings.HasSuffix(resp.Request.URL.Path, "/cs")
	}
	return strings.Contains(resp.Header.Get("Content-Type"), "json")
}
