package gofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin name.
const Name = "gofile"

func init() { hosters.RegisterFactory(Name, New) }

const (
	maxBody         = 16 << 20
	maxDepth        = 64
	sessionTokenKey = "gofile.token"
	defaultWTTTL    = 6 * time.Hour
	defaultRPS      = 4
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9-]{3,64}$`)

// Hoster is the Gofile plugin.
type Hoster struct {
	cfg      config.HosterConfig
	apiBase  string
	siteBase string
	jsURL    string
	fixedWT  string
	wtTTL    time.Duration
	limiter  *rate.Limiter

	mu      sync.Mutex
	wt      string
	wtUntil time.Time
}

var (
	_ hosters.Hoster            = (*Hoster)(nil)
	_ hosters.StreamingResolver = (*Hoster)(nil)
	_ hosters.AccountChecker    = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.gofile] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	h := &Hoster{
		cfg:      cfg,
		apiBase:  defaultAPIBase,
		siteBase: defaultSiteBase,
		fixedWT:  strings.TrimSpace(cfg.Options["website_token"]),
		wtTTL:    defaultWTTTL,
	}
	for key, dst := range map[string]*string{"api_base": &h.apiBase, "site_base": &h.siteBase, "js_url": &h.jsURL} {
		if v := strings.TrimSpace(cfg.Options[key]); v != "" {
			if _, err := url.ParseRequestURI(v); err != nil {
				return nil, fmt.Errorf("gofile: invalid %s: %w", key, err)
			}
			*dst = strings.TrimRight(v, "/")
		}
	}
	if h.jsURL == "" {
		h.jsURL = h.siteBase + defaultJSPath
	}
	if v := strings.TrimSpace(cfg.Options["wt_ttl"]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("gofile: invalid wt_ttl: %w", err)
		}
		h.wtTTL = d
	}
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = defaultRPS
	}
	h.limiter = rate.NewLimiter(rate.Limit(rps), 2)
	return h, nil
}

// Name implements hosters.Hoster.
func (h *Hoster) Name() string { return Name }

// Caps implements hosters.Hoster.
func (h *Hoster) Caps() hosters.Caps {
	return hosters.Caps{
		MaxConnsPerFile:   h.cfg.MaxConnsPerFile,
		MaxParallel:       h.cfg.MaxParallel,
		Resumable:         true,
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindAPIKey},
		DefaultRetryAfter: 10 * time.Minute,
		QuotaScope:        hosters.ScopeIP,
	}
}

func contentID(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "gofile.io" {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 || segs[0] != "d" || !idRe.MatchString(segs[1]) {
		return "", false
	}
	return segs[1], true
}

// Match implements hosters.Hoster.
func (h *Hoster) Match(u *url.URL) bool {
	_, ok := contentID(u)
	return ok
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, func(hosters.FileInfo) {})
}

// ResolveStream implements hosters.StreamingResolver.
func (h *Hoster) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("gofile: parse link: %w", err))
	}
	id, ok := contentID(u)
	if !ok {
		return nil, hosters.NewFatal(errors.New("gofile: unsupported link"))
	}
	root, err := h.contents(ctx, s, id)
	if err != nil {
		return nil, fmt.Errorf("gofile: contents %s: %w", id, err)
	}
	canon := h.siteBase + "/d/" + id
	if root.Type == "file" {
		f := h.fileInfo(root, "", id, canon)
		emit(f)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
	}
	name := root.Name
	if name == "" {
		name = id
	}
	res := &hosters.ResolveResult{Name: name, IsFolder: true}
	if err := h.walk(ctx, s, root, "", canon, 0, func(f hosters.FileInfo) {
		res.Files = append(res.Files, f)
		emit(f)
	}); err != nil {
		return nil, err
	}
	return res, nil
}

// walk lists folder (already fetched) and recurses into its sub-folders.
func (h *Hoster) walk(ctx context.Context, s *hosters.Session, folder content, prefix, canon string, depth int, emit func(hosters.FileInfo)) error {
	if depth > maxDepth {
		return hosters.NewFatal(errors.New("gofile: folder tree too deep"))
	}
	children := sortedChildren(folder.Children)
	for _, c := range children {
		if c.Type == "file" {
			emit(h.fileInfo(c, path.Join(prefix, c.Name), folder.ID, canon))
		}
	}
	for _, c := range children {
		if c.Type != "folder" {
			continue
		}
		sub, err := h.contents(ctx, s, c.ID)
		if err != nil {
			return fmt.Errorf("gofile: contents %s: %w", c.ID, err)
		}
		name := c.Name
		if name == "" {
			name = sub.Name
		}
		if err := h.walk(ctx, s, sub, path.Join(prefix, name), canon, depth+1, emit); err != nil {
			return err
		}
	}
	return nil
}

func sortedChildren(m map[string]content) []content {
	out := make([]content, 0, len(m))
	for id, c := range m {
		if c.ID == "" {
			c.ID = id
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b content) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func (h *Hoster) fileInfo(c content, relPath, parent, canon string) hosters.FileInfo {
	f := hosters.FileInfo{
		ID:      c.ID,
		Name:    c.Name,
		RelPath: relPath,
		Size:    c.Size,
		Link:    canon,
		Meta:    map[string]string{"parent": parent},
	}
	if f.Name == "" {
		f.Name = c.ID
	}
	if f.Size <= 0 {
		f.Size = -1
	}
	if len(c.MD5) == 32 {
		f.Checksum = &hosters.Checksum{Algo: hosters.AlgoMD5, Value: strings.ToLower(c.MD5)}
	}
	return f
}

// Prepare implements hosters.Hoster: it lists the parent folder again to get
// the current link and installs the accountToken cookie.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	parent := f.Meta["parent"]
	if parent == "" {
		u, err := url.Parse(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(fmt.Errorf("gofile: parse link: %w", err))
		}
		id, ok := contentID(u)
		if !ok {
			return nil, hosters.NewFatal(errors.New("gofile: file without parent folder"))
		}
		parent = id
	}
	folder, err := h.contents(ctx, s, parent)
	if err != nil {
		return nil, fmt.Errorf("gofile: contents %s: %w", parent, err)
	}
	var file *content
	if folder.Type == "file" && (folder.ID == f.ID || f.ID == "") {
		file = &folder
	} else {
		for id, c := range folder.Children {
			if c.ID == "" {
				c.ID = id
			}
			if c.Type == "file" && c.ID == f.ID {
				file = &c
				break
			}
		}
	}
	if file == nil {
		return nil, hosters.NewNotFound(fmt.Errorf("gofile: file %s no longer in folder %s", f.ID, parent))
	}
	if file.Link == "" {
		return nil, hosters.NewNeedsAuth(errors.New("gofile: no download link (premium required?)"))
	}
	lu, err := url.Parse(file.Link)
	if err != nil || (lu.Scheme != "https" && lu.Scheme != "http") {
		return nil, hosters.NewFatal(errors.New("gofile: invalid download link"))
	}
	token, err := h.token(ctx, s)
	if err != nil {
		return nil, err
	}
	setTokenCookie(s, lu, token)
	spec := &hosters.DownloadSpec{
		URL:       file.Link,
		Filename:  file.Name,
		Size:      file.Size,
		Resumable: true,
	}
	if spec.Size <= 0 {
		spec.Size = -1
	}
	return spec, nil
}

func setTokenCookie(s *hosters.Session, u *url.URL, token string) {
	if s.Jar == nil {
		return
	}
	c := &http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: u.Scheme == "https"}
	host := strings.ToLower(u.Hostname())
	if host == "gofile.io" || strings.HasSuffix(host, ".gofile.io") {
		c.Domain = "gofile.io"
		u = &url.URL{Scheme: u.Scheme, Host: "gofile.io", Path: "/"}
	}
	s.Jar.SetCookies(u, []*http.Cookie{c})
}

// token returns the account token: the premium account's or a guest one.
func (h *Hoster) token(ctx context.Context, s *hosters.Session) (string, error) {
	if !s.Anonymous() {
		sec, err := s.Secret(ctx)
		if err != nil {
			return "", hosters.NewNeedsAuth(fmt.Errorf("gofile: load token: %w", err))
		}
		if t := strings.TrimSpace(sec.APIKey); t != "" {
			return t, nil
		}
		if t := strings.TrimSpace(sec.Session); t != "" {
			return t, nil
		}
		return "", hosters.NewNeedsAuth(errors.New("gofile: account has no token"))
	}
	if v, ok := s.Value(sessionTokenKey); ok {
		if t, ok := v.(string); ok && t != "" {
			return t, nil
		}
	}
	var resp apiResponse[accountData]
	if err := h.call(ctx, s, http.MethodPost, h.apiBase+pathAccounts, "", "", &resp); err != nil {
		return "", fmt.Errorf("gofile: create guest account: %w", err)
	}
	if resp.Data.Token == "" {
		return "", hosters.NewTemporary(0, errors.New("gofile: guest account without token"))
	}
	s.SetValue(sessionTokenKey, resp.Data.Token)
	return resp.Data.Token, nil
}

// websiteToken returns the wt (configured, cached or scraped).
func (h *Hoster) websiteToken(ctx context.Context, s *hosters.Session) (string, error) {
	if h.fixedWT != "" {
		return h.fixedWT, nil
	}
	h.mu.Lock()
	if h.wt != "" && time.Now().Before(h.wtUntil) {
		wt := h.wt
		h.mu.Unlock()
		return wt, nil
	}
	h.mu.Unlock()

	if err := h.limiter.Wait(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.jsURL, nil)
	if err != nil {
		return "", hosters.NewFatal(err)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", hosters.DefaultClassify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", hosters.NewTemporary(0, err)
	}
	if err := hosters.DefaultClassify(resp, nil, nil); err != nil {
		return "", fmt.Errorf("gofile: fetch site js: %w", err)
	}
	wt := ExtractWebsiteToken(body)
	if wt == "" {
		return "", hosters.NewFatal(errors.New("gofile: website token not found in site JS (set options.website_token)"))
	}
	h.mu.Lock()
	h.wt, h.wtUntil = wt, time.Now().Add(h.wtTTL)
	h.mu.Unlock()
	return wt, nil
}

// forgetWebsiteToken drops a cached wt rejected by the API.
func (h *Hoster) forgetWebsiteToken() {
	h.mu.Lock()
	h.wt = ""
	h.mu.Unlock()
}

// ExtractWebsiteToken finds the website token in the site JS, or "".
func ExtractWebsiteToken(js []byte) string {
	for _, re := range wtPatterns {
		if m := re.FindSubmatch(js); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// contents fetches /contents/<id>, retrying once with a fresh guest token
// or wt when the API rejects them.
func (h *Hoster) contents(ctx context.Context, s *hosters.Session, id string) (content, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		token, err := h.token(ctx, s)
		if err != nil {
			return content{}, err
		}
		wt, err := h.websiteToken(ctx, s)
		if err != nil {
			return content{}, err
		}
		q := url.Values{"wt": {wt}, "cache": {"true"}}
		if pw := h.cfg.Options["password_"+id]; pw != "" {
			sum := sha256.Sum256([]byte(pw))
			q.Set("password", hex.EncodeToString(sum[:]))
		}
		var resp apiResponse[content]
		err = h.call(ctx, s, http.MethodGet, h.apiBase+pathContents+url.PathEscape(id)+"?"+q.Encode(), token, wt, &resp)
		if err == nil {
			c := resp.Data
			if c.PasswordStatus != "" && c.PasswordStatus != "passwordOk" {
				return content{}, hosters.NewNeedsAuth(fmt.Errorf("gofile: folder is password protected (%s)", c.PasswordStatus))
			}
			if c.ID == "" {
				c.ID = id
			}
			return c, nil
		}
		lastErr = err
		var se *statusError
		if !errors.As(err, &se) || !se.authRelated() {
			return content{}, err
		}
		// Token or wt rejected: refresh what we can and retry once.
		if s.Anonymous() {
			s.DeleteValue(sessionTokenKey)
		}
		h.forgetWebsiteToken()
	}
	return content{}, lastErr
}

// statusError is a non-"ok" API status.
type statusError struct{ status string }

func (e *statusError) Error() string { return "gofile: api status " + e.status }

func (e *statusError) authRelated() bool {
	s := strings.ToLower(e.status)
	return strings.Contains(s, "auth") || strings.Contains(s, "token") || strings.Contains(s, "wt")
}

// call performs an API request and decodes the envelope into out.
func (h *Hoster) call(ctx context.Context, s *hosters.Session, method, u, token, wt string, out any) error {
	if err := h.limiter.Wait(ctx); err != nil {
		return err
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return hosters.NewFatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if wt != "" {
		req.Header.Set("X-Website-Token", wt)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return hosters.DefaultClassify(nil, nil, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return hosters.NewTemporary(0, err)
	}
	var env apiResponse[json.RawMessage]
	if json.Unmarshal(raw, &env) == nil && env.Status != "" && env.Status != "ok" {
		return classifyStatus(env.Status, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return hosters.DefaultClassify(resp, raw, nil)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return hosters.NewFatal(fmt.Errorf("decode response: %w", err))
	}
	return nil
}

// classifyStatus maps an API status ("error-notFound", ...) to an outcome.
func classifyStatus(status string, code int) error {
	se := &statusError{status: status}
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "notfound"), strings.Contains(s, "notexist"), strings.Contains(s, "removed"),
		strings.Contains(s, "deleted"), strings.Contains(s, "disabled"):
		return hosters.NewNotFound(se)
	case strings.Contains(s, "ratelimit"), strings.Contains(s, "toomany"), code == http.StatusTooManyRequests:
		return hosters.NewQuota(hosters.ScopeIP, 0, se)
	case strings.Contains(s, "password"), strings.Contains(s, "premium"), strings.Contains(s, "notallowed"):
		return hosters.NewNeedsAuth(se)
	case se.authRelated():
		return hosters.NewNeedsAuth(se)
	case code >= 500:
		return hosters.NewTemporary(0, se)
	default:
		return hosters.NewFatal(se)
	}
}

// Classify implements hosters.Hoster.
func (h *Hoster) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil || resp == nil {
		return hosters.DefaultClassify(resp, body, err)
	}
	var env apiResponse[json.RawMessage]
	if len(body) > 0 && body[0] == '{' && json.Unmarshal(body, &env) == nil && env.Status != "" && env.Status != "ok" {
		return classifyStatus(env.Status, resp.StatusCode)
	}
	return hosters.DefaultClassify(resp, body, nil)
}

// CheckAccount implements hosters.AccountChecker.
func (h *Hoster) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	if s.Anonymous() {
		return nil, hosters.ErrNoAccount
	}
	token, err := h.token(ctx, s)
	if err != nil {
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "sin token", QuotaTotal: -1, QuotaUsed: -1}, nil //nolint:nilerr // the failure is reported as the account status
	}
	var idResp apiResponse[accountData]
	err = h.call(ctx, s, http.MethodGet, h.apiBase+pathGetID, token, "", &idResp)
	if hosters.IsNeedsAuth(err) {
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "token rechazado", QuotaTotal: -1, QuotaUsed: -1}, nil
	}
	if err != nil {
		return nil, err
	}
	var acc apiResponse[accountData]
	if err := h.call(ctx, s, http.MethodGet, h.apiBase+pathAccount+url.PathEscape(idResp.Data.ID), token, "", &acc); err != nil {
		if hosters.IsNeedsAuth(err) {
			return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "token rechazado", QuotaTotal: -1, QuotaUsed: -1}, nil
		}
		return nil, err
	}
	tier := strings.ToLower(acc.Data.Tier)
	return &accounts.CheckResult{
		Status:     accounts.StatusOK,
		Message:    acc.Data.Tier,
		Premium:    tier == "premium" || tier == "donor",
		QuotaTotal: -1,
		QuotaUsed:  -1,
	}, nil
}
