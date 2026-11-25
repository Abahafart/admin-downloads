package onefichier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin name.
const Name = "onefichier"

func init() { hosters.RegisterFactory(Name, New) }

const (
	maxBody               = 8 << 20
	defaultMaxInlineWait  = 90 * time.Second
	defaultAPIInterval    = 2 * time.Second
	defaultFreeRetryAfter = 5 * time.Minute
	floodRetryAfter       = 2 * time.Minute
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9]{5,30}$`)

// Hoster is the 1fichier plugin.
type Hoster struct {
	cfg           config.HosterConfig
	webBase       *url.URL
	apiBase       string
	maxInlineWait time.Duration
	apiLimiter    *rate.Limiter
	domains       []string
}

var (
	_ hosters.Hoster         = (*Hoster)(nil)
	_ hosters.AccountChecker = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.onefichier] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	h := &Hoster{cfg: cfg, apiBase: defaultAPIBase, maxInlineWait: defaultMaxInlineWait}
	web := defaultWebBase
	if v := strings.TrimSpace(cfg.Options["web_base"]); v != "" {
		web = v
	}
	u, err := url.ParseRequestURI(strings.TrimRight(web, "/"))
	if err != nil {
		return nil, fmt.Errorf("onefichier: invalid web_base: %w", err)
	}
	h.webBase = u
	if v := strings.TrimSpace(cfg.Options["api_base"]); v != "" {
		if _, err := url.ParseRequestURI(v); err != nil {
			return nil, fmt.Errorf("onefichier: invalid api_base: %w", err)
		}
		h.apiBase = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(cfg.Options["max_inline_wait"]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("onefichier: invalid max_inline_wait: %w", err)
		}
		h.maxInlineWait = d
	}
	interval := defaultAPIInterval
	if v := strings.TrimSpace(cfg.Options["api_interval"]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("onefichier: invalid api_interval: %w", err)
		}
		interval = d
	}
	if interval > 0 {
		h.apiLimiter = rate.NewLimiter(rate.Every(interval), 1)
	} else {
		h.apiLimiter = rate.NewLimiter(rate.Inf, 1)
	}
	h.domains = append([]string{mainDomain}, aliasDomains...)
	for _, d := range cfg.Domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			h.domains = append(h.domains, d)
		}
	}
	return h, nil
}

// Name implements hosters.Hoster.
func (h *Hoster) Name() string { return Name }

// Caps implements hosters.Hoster.
func (h *Hoster) Caps() hosters.Caps {
	return hosters.Caps{
		MaxConnsPerFile:   h.cfg.MaxConnsPerFile,
		MaxParallel:       h.cfg.MaxParallel,
		Resumable:         true, // premium; free links are marked non-resumable per spec
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindAPIKey},
		DefaultRetryAfter: defaultFreeRetryAfter,
		QuotaScope:        hosters.ScopeIP,
	}
}

type parsedLink struct {
	folder bool
	id     string
}

func (h *Hoster) parse(u *url.URL) (parsedLink, bool) {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") {
		return parsedLink{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	sub := ""
	ok := false
	for _, d := range h.domains {
		if host == d {
			ok = true
			break
		}
		if strings.HasSuffix(host, "."+d) {
			sub, ok = strings.TrimSuffix(host, "."+d), true
			break
		}
	}
	if !ok {
		return parsedLink{}, false
	}
	path := strings.Trim(u.Path, "/")
	switch {
	case strings.HasPrefix(path, "dir/"):
		id := strings.SplitN(strings.TrimPrefix(path, "dir/"), "/", 2)[0]
		return parsedLink{folder: true, id: id}, idRe.MatchString(id)
	case path == "" && sub == "":
		id := strings.SplitN(u.RawQuery, "&", 2)[0]
		return parsedLink{id: id}, idRe.MatchString(id)
	case sub != "" && !strings.Contains(sub, ".") && (path == "" || !strings.Contains(path, "/")):
		// Old form <id>.1fichier.com (the API/CDN hosts a-NN, api, img are
		// not file links).
		if strings.Contains(sub, "-") || sub == "api" || sub == "img" || sub == "www" {
			return parsedLink{}, false
		}
		return parsedLink{id: sub}, idRe.MatchString(sub)
	}
	return parsedLink{}, false
}

// Match implements hosters.Hoster.
func (h *Hoster) Match(u *url.URL) bool {
	_, ok := h.parse(u)
	return ok
}

func canonicalLink(id string) string { return "https://" + mainDomain + "/?" + strings.ToLower(id) }

// pageURL is the file page on web_base.
func (h *Hoster) pageURL(id string) string { return h.webBase.String() + "/?" + strings.ToLower(id) }

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("onefichier: parse link: %w", err))
	}
	p, ok := h.parse(u)
	if !ok {
		return nil, hosters.NewFatal(errors.New("onefichier: unsupported link"))
	}
	if p.folder {
		return h.resolveDir(ctx, s, p.id)
	}
	f := hosters.FileInfo{ID: strings.ToLower(p.id), Name: strings.ToLower(p.id), Size: -1, Link: canonicalLink(p.id)}
	if key, err := h.apiKey(ctx, s); err != nil {
		return nil, err
	} else if key != "" {
		var info apiFile
		if err := h.api(ctx, s, key, apiFileInfo, map[string]any{"url": f.Link}, &info); err != nil {
			return nil, fmt.Errorf("onefichier: file info: %w", err)
		}
		if info.Filename != "" {
			f.Name = info.Filename
		}
		if info.Size > 0 {
			f.Size = info.Size
		}
		return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
	}
	lines, err := h.checkLinks(ctx, s, f.Link)
	if err != nil {
		return nil, fmt.Errorf("onefichier: check link: %w", err)
	}
	if len(lines) > 0 {
		l := lines[0]
		if l.notFound {
			return nil, hosters.NewNotFound(errors.New("onefichier: file not found"))
		}
		if l.name != "" {
			f.Name = l.name
		}
		if l.size > 0 {
			f.Size = l.size
		}
	}
	return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
}

func (h *Hoster) checkLinks(ctx context.Context, s *hosters.Session, link string) ([]checkLine, error) {
	form := url.Values{"links[]": {link}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.webBase.String()+pathCheckLinks, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := h.do(s, req)
	if err != nil {
		return nil, err
	}
	return parseCheckLinks(body), nil
}

func (h *Hoster) resolveDir(ctx context.Context, s *hosters.Session, id string) (*hosters.ResolveResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.webBase.String()+pathDir+url.PathEscape(id)+"?json=1", nil)
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	req.Header.Set("Accept", "application/json")
	body, err := h.do(s, req)
	if err != nil {
		return nil, fmt.Errorf("onefichier: folder %s: %w", id, err)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		if notFoundRe.Match(body) || len(trimmed) == 0 {
			return nil, hosters.NewNotFound(fmt.Errorf("onefichier: folder %s not found", id))
		}
		return nil, hosters.NewFatal(fmt.Errorf("onefichier: folder %s: unexpected answer", id))
	}
	var entries []dirEntry
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("onefichier: decode folder: %w", err))
	}
	res := &hosters.ResolveResult{Name: "1fichier-" + id, IsFolder: true}
	used := map[string]int{}
	for _, e := range entries {
		lu, err := url.Parse(e.Link)
		if err != nil {
			continue
		}
		p, ok := h.parse(lu)
		if !ok || p.folder {
			continue
		}
		f := hosters.FileInfo{
			ID:      strings.ToLower(p.id),
			Name:    e.Filename,
			Size:    e.Size,
			Link:    canonicalLink(p.id),
			RelPath: uniqueName(used, e.Filename, p.id),
		}
		if f.Name == "" {
			f.Name = f.ID
		}
		if f.Size <= 0 {
			f.Size = -1
		}
		res.Files = append(res.Files, f)
	}
	return res, nil
}

func uniqueName(used map[string]int, name, id string) string {
	if strings.TrimSpace(name) == "" {
		name = id
	}
	key := strings.ToLower(name)
	n := used[key]
	used[key] = n + 1
	if n == 0 {
		return name
	}
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		ext = name[i:]
	}
	cand := fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
	used[strings.ToLower(cand)]++
	return cand
}

// Prepare implements hosters.Hoster.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	id := f.ID
	if id == "" {
		u, err := url.Parse(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		p, ok := h.parse(u)
		if !ok || p.folder {
			return nil, hosters.NewFatal(errors.New("onefichier: not a file link"))
		}
		id = p.id
	}
	key, err := h.apiKey(ctx, s)
	if err != nil {
		return nil, err
	}
	if key != "" {
		var tok apiToken
		if err := h.api(ctx, s, key, apiGetToken, map[string]any{"url": canonicalLink(id), "cdn": 0}, &tok); err != nil {
			return nil, fmt.Errorf("onefichier: get token: %w", err)
		}
		if !validHTTPURL(tok.URL) {
			return nil, hosters.NewTemporary(0, errors.New("onefichier: get token returned no url"))
		}
		return &hosters.DownloadSpec{URL: tok.URL, Size: f.Size, Resumable: true}, nil
	}
	return h.prepareFree(ctx, s, id, f.Size)
}

// prepareFree runs the anonymous page flow.
func (h *Hoster) prepareFree(ctx context.Context, s *hosters.Session, id string, size int64) (*hosters.DownloadSpec, error) {
	page := h.pageURL(id)
	base, _ := url.Parse(page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	body, err := h.do(s, req)
	if err != nil {
		return nil, err
	}
	for step := 0; step < 3; step++ {
		p, err := parseFreePage(body, base)
		if err != nil {
			return nil, hosters.NewFatal(fmt.Errorf("onefichier: parse page: %w", err))
		}
		switch {
		case p.notFound:
			return nil, hosters.NewNotFound(errors.New("onefichier: file not found"))
		case p.password:
			return nil, hosters.NewNeedsAuth(errors.New("onefichier: file is password protected"))
		case p.link != "":
			return &hosters.DownloadSpec{URL: p.link, Size: size, Resumable: false, MaxConns: 1}, nil
		case p.busy:
			return nil, hosters.NewQuota(hosters.ScopeIP, 0, errors.New("onefichier: free users can download one file at a time"))
		case p.wait > 0 && (p.form == nil || p.wait > h.maxInlineWait):
			return nil, hosters.NewQuota(hosters.ScopeIP, p.wait, fmt.Errorf("onefichier: free download wait %s", p.wait))
		case p.form == nil:
			return nil, hosters.NewTemporary(0, errors.New("onefichier: download form not found"))
		}
		if p.wait > 0 {
			s.Log.Info("onefichier: waiting free countdown", "id", id, "wait", p.wait)
			t := time.NewTimer(p.wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		if !sameSite(p.form.action, base) {
			return nil, hosters.NewFatal(errors.New("onefichier: download form posts to another site"))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.form.action, strings.NewReader(p.form.values.Encode()))
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", page)
		if body, err = h.do(s, req); err != nil {
			return nil, err
		}
	}
	return nil, hosters.NewTemporary(0, errors.New("onefichier: download link not found"))
}

func sameSite(action string, base *url.URL) bool {
	u, err := url.Parse(action)
	return err == nil && strings.EqualFold(u.Hostname(), base.Hostname())
}

func validHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// apiKey returns the account's API key ("" for anonymous sessions).
func (h *Hoster) apiKey(ctx context.Context, s *hosters.Session) (string, error) {
	if s.Anonymous() {
		return "", nil
	}
	sec, err := s.Secret(ctx)
	if err != nil {
		return "", hosters.NewNeedsAuth(fmt.Errorf("onefichier: load api key: %w", err))
	}
	if k := strings.TrimSpace(sec.APIKey); k != "" {
		return k, nil
	}
	return "", hosters.NewNeedsAuth(errors.New("onefichier: account has no api key"))
}

// do sends req with the session client and returns the body.
func (h *Hoster) do(s *hosters.Session, req *http.Request) ([]byte, error) {
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, hosters.NewTemporary(0, err)
	}
	if resp.StatusCode >= 400 {
		return nil, h.Classify(resp, body, nil)
	}
	return body, nil
}

// api POSTs a JSON request to the 1fichier API.
func (h *Hoster) api(ctx context.Context, s *hosters.Session, key, path string, in, out any) error {
	if err := h.apiLimiter.Wait(ctx); err != nil {
		return err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return hosters.NewFatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.apiBase+path, bytes.NewReader(b))
	if err != nil {
		return hosters.NewFatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return hosters.NewTemporary(0, err)
	}
	var st apiStatus
	if json.Unmarshal(body, &st) == nil && strings.EqualFold(st.Status, "KO") {
		return classifyAPIMessage(st.Message, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return h.Classify(resp, body, nil)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return hosters.NewFatal(fmt.Errorf("decode response: %w", err))
	}
	return nil
}

// classifyAPIMessage maps an API "KO" message to an outcome. Messages look
// like "Resource not found #469".
func classifyAPIMessage(msg string, code int) error {
	m := strings.ToLower(msg)
	e := fmt.Errorf("onefichier api: %s", msg)
	switch {
	case strings.Contains(m, "not found"), strings.Contains(m, "deleted"), strings.Contains(m, "not exist"), code == http.StatusNotFound:
		return hosters.NewNotFound(e)
	case strings.Contains(m, "flood"), strings.Contains(m, "locked"):
		return hosters.NewTemporary(floodRetryAfter, e)
	case strings.Contains(m, "password"):
		return hosters.NewNeedsAuth(e)
	case strings.Contains(m, "authent"), strings.Contains(m, "api key"), strings.Contains(m, "apikey"),
		strings.Contains(m, "customer"), strings.Contains(m, "premium"), code == http.StatusUnauthorized, code == http.StatusForbidden:
		return hosters.NewNeedsAuth(e)
	case strings.Contains(m, "traffic"), strings.Contains(m, "quota"):
		return hosters.NewQuota(hosters.ScopeAccount, 0, e)
	case code >= 500 || code == 0:
		return hosters.NewTemporary(0, e)
	default:
		return hosters.NewFatal(e)
	}
}

// Classify implements hosters.Hoster.
func (h *Hoster) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil || resp == nil {
		return hosters.DefaultClassify(resp, body, err)
	}
	var st apiStatus
	if len(body) > 0 && body[0] == '{' && json.Unmarshal(body, &st) == nil && strings.EqualFold(st.Status, "KO") {
		return classifyAPIMessage(st.Message, resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.HasPrefix(ct, "text/html") && resp.Header.Get("Content-Disposition") == ""
	if isHTML && len(body) > 0 {
		base := h.webBase
		if resp.Request != nil && resp.Request.URL != nil {
			base = resp.Request.URL
		}
		if p, perr := parseFreePage(body, base); perr == nil {
			switch {
			case p.notFound:
				return hosters.NewNotFound(errors.New("onefichier: file not found"))
			case p.wait > 0:
				return hosters.NewQuota(hosters.ScopeIP, p.wait, fmt.Errorf("onefichier: free download wait %s", p.wait))
			case p.busy:
				return hosters.NewQuota(hosters.ScopeIP, 0, errors.New("onefichier: one free download at a time"))
			}
		}
		if resp.StatusCode < 400 {
			// A page instead of the file: the link is no longer valid.
			return hosters.NewExpired(errors.New("onefichier: got an HTML page instead of the file"))
		}
	}
	return hosters.DefaultClassify(resp, body, nil)
}

// CheckAccount implements hosters.AccountChecker.
func (h *Hoster) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	key, err := h.apiKey(ctx, s)
	if err != nil || key == "" {
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "sin API key", QuotaTotal: -1, QuotaUsed: -1}, nil //nolint:nilerr // the failure is reported as the account status
	}
	var info map[string]any
	err = h.api(ctx, s, key, apiUserInfo, map[string]any{}, &info)
	if hosters.IsNeedsAuth(err) {
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "API key rechazada (o cuenta sin premium)", QuotaTotal: -1, QuotaUsed: -1}, nil
	}
	if err != nil {
		return nil, err
	}
	res := &accounts.CheckResult{Status: accounts.StatusOK, QuotaTotal: -1, QuotaUsed: -1}
	for _, k := range []string{"offer", "subscription", "premium", "account_type"} {
		switch v := info[k].(type) {
		case string:
			if l := strings.ToLower(v); l != "" && l != "0" && l != "free" && l != "none" {
				res.Premium, res.Message = true, v
			}
		case float64:
			res.Premium = res.Premium || v > 0
		case bool:
			res.Premium = res.Premium || v
		}
	}
	if v, ok := info["premium_until"].(string); ok {
		if t, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
			res.PremiumUntil = t
		}
	}
	return res, nil
}
