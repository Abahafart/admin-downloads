package terabox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"admindescargas/internal/hosters"
)

// shareLink is a parsed share URL.
type shareLink struct {
	Host string // link host (e.g. www.terabox.com)
	SURL string // share id without the leading "1"
	// FsID restricts the link to one file ("#fsid=<id>", added by Resolve to
	// FileInfo.Link so a single file can be re-resolved).
	FsID string
}

var (
	errNotShare = errors.New("terabox: not a TeraBox share link")
	errNoToken  = errors.New("terabox: jsToken not found in the share page (the site may have changed; run `adesc selftest terabox`)")
)

func validSURL(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// parseShareLink accepts /s/1<surl>, /sharing/link?surl=, /sharing/embed?surl=
// and /wap/share/filelist?surl= (host checks are done by the caller).
func parseShareLink(u *url.URL) (shareLink, error) {
	l := shareLink{Host: strings.ToLower(u.Host)}
	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasPrefix(path, pathShortPrefix):
		id := strings.TrimPrefix(path, pathShortPrefix)
		if i := strings.IndexByte(id, '/'); i >= 0 {
			id = id[:i]
		}
		if len(id) < 2 || id[0] != '1' {
			return l, errNotShare
		}
		id = id[1:]
		l.SURL = id
	case path == pathSharingLink, path == pathSharingEmbd, path == pathWapFileList, path == pathWapHome:
		l.SURL = u.Query().Get(querySURL)
	default:
		return l, errNotShare
	}
	if !validSURL(l.SURL) {
		return l, errNotShare
	}
	if v, ok := strings.CutPrefix(u.Fragment, "fsid="); ok && isDigits(v) {
		l.FsID = v
	}
	return l, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// canonical is the share URL without the file fragment.
func (l shareLink) canonical() string {
	return "https://" + l.Host + pathShortPrefix + "1" + l.SURL
}

// tokens are values scraped from the share page.
type tokens struct {
	JSToken  string
	LogID    string
	BDSToken string
}

// shareCtx is the per-share state of a session: where to send API calls
// and the page tokens. Safe for concurrent use.
type shareCtx struct {
	mu      sync.Mutex
	base    string // scheme://host of the share page after redirects
	pageURL string // used as Referer
	tok     tokens
	fetched time.Time
}

func (c *shareCtx) get() (base, referer string, t tokens) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base, c.pageURL, c.tok
}

// sessState is the plugin state cached in a Session.
type sessState struct {
	mu            sync.Mutex
	cookies       []*http.Cookie // account cookies (nil = anonymous)
	cookiesLoaded bool
	shares        map[string]*shareCtx
	dlinks        map[string]dlinkEntry // surl|fsid -> dlink
}

type dlinkEntry struct {
	url string
	at  time.Time
}

const valState = "terabox.state"

func (p *Plugin) state(s *hosters.Session) *sessState {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if v, ok := s.Value(valState); ok {
		if st, ok := v.(*sessState); ok {
			return st
		}
	}
	st := &sessState{shares: map[string]*shareCtx{}, dlinks: map[string]dlinkEntry{}}
	s.SetValue(valState, st)
	return st
}

func (st *sessState) putDLink(surl, fsid, dlink string) {
	if dlink == "" || fsid == "" {
		return
	}
	st.mu.Lock()
	st.dlinks[surl+"|"+fsid] = dlinkEntry{url: dlink, at: time.Now()}
	st.mu.Unlock()
}

func (st *sessState) dlink(surl, fsid string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.dlinks[surl+"|"+fsid]
	if !ok || time.Since(e.at) > dlinkTTL {
		return ""
	}
	return e.url
}

func (st *sessState) dropDLink(surl, fsid string) {
	st.mu.Lock()
	delete(st.dlinks, surl+"|"+fsid)
	st.mu.Unlock()
}

// accountCookies loads and parses the account cookies once per session.
func (p *Plugin) accountCookies(ctx context.Context, s *hosters.Session) ([]*http.Cookie, error) {
	if s.Anonymous() {
		return nil, nil
	}
	st := p.state(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cookiesLoaded {
		return st.cookies, nil
	}
	sec, err := s.Secret(ctx)
	if err != nil {
		return nil, hosters.NewNeedsAuth(fmt.Errorf("terabox: loading account cookies: %w", err))
	}
	cs, err := parseCookies(sec.Cookies, p.domains)
	if err != nil {
		return nil, hosters.NewNeedsAuth(err)
	}
	if !hasCookie(cs, cookieLogin) {
		return nil, hosters.NewNeedsAuth(errors.New("terabox: the account cookies have no ndus cookie (export them while logged in)"))
	}
	st.cookies, st.cookiesLoaded = cs, true
	return cs, nil
}

// applyCookies puts the account cookies (and extraCookies) into the jar for
// u's host when it is a TeraBox host (or the configured base host).
func (p *Plugin) applyCookies(s *hosters.Session, cs []*http.Cookie, req *http.Request) {
	host := req.URL.Host
	if !hostMatches(host, p.domains) && !p.isBaseHost(host) {
		return
	}
	all := make([]*http.Cookie, 0, len(cs)+len(extraCookies))
	for k, v := range extraCookies {
		all = append(all, &http.Cookie{Name: k, Value: v, Path: "/"})
	}
	all = append(all, cs...)
	if s.Jar != nil {
		s.Jar.SetCookies(&url.URL{Scheme: req.URL.Scheme, Host: host, Path: "/"}, all)
		return
	}
	for _, c := range all {
		req.AddCookie(c)
	}
}

func (p *Plugin) isBaseHost(host string) bool {
	if p.base == "" {
		return false
	}
	u, err := url.Parse(p.base)
	return err == nil && strings.EqualFold(u.Host, host)
}

// response is a fetched HTTP response with (part of) its body.
type response struct {
	resp *http.Response
	body []byte
}

// get performs a rate-limited GET with browser headers and account cookies.
// follow=false returns 3xx responses instead of following them. readLimit
// caps the body read (the rest is discarded).
func (p *Plugin) get(ctx context.Context, s *hosters.Session, rawURL, referer string, follow bool, readLimit int64, hdr http.Header) (*response, error) {
	cs, err := p.accountCookies(ctx, s)
	if err != nil {
		return nil, err
	}
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("terabox: building request: %w", scrubURLError(err)))
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	p.applyCookies(s, cs, req)
	c := *s.Client
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("terabox: too many redirects")
		}
		// Mirrors redirect between domains (terabox.com -> terabox.app):
		// make sure the new host gets the account cookies too.
		p.applyCookies(s, cs, r)
		r.Header.Set("User-Agent", UserAgent)
		return nil
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, p.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return nil, hosters.NewTemporary(0, fmt.Errorf("terabox: reading response: %w", scrubURLError(err)))
	}
	return &response{resp: resp, body: body}, nil
}

// findFirst returns the first submatch of any pattern.
func findFirst(page []byte, pats []*regexp.Regexp) string {
	for _, re := range pats {
		if m := re.FindSubmatch(page); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// extractTokens pulls jsToken, dp-logid and bdstoken from a share page.
func extractTokens(page []byte) tokens {
	return tokens{
		JSToken:  findFirst(page, jsTokenPatterns),
		LogID:    findFirst(page, logIDPatterns),
		BDSToken: findFirst(page, bdstokenPatterns),
	}
}

// siteBase returns where share pages are requested for a link host.
func (p *Plugin) siteBase(host string) string {
	if p.base != "" {
		return p.base
	}
	return "https://" + host
}

// shareContext loads (or reuses) the share page and its tokens. force
// reloads it (stale jsToken).
func (p *Plugin) shareContext(ctx context.Context, s *hosters.Session, l shareLink, force bool) (*shareCtx, error) {
	st := p.state(s)
	st.mu.Lock()
	sc := st.shares[l.SURL]
	if sc == nil {
		sc = &shareCtx{}
		st.shares[l.SURL] = sc
	}
	st.mu.Unlock()

	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !force && sc.tok.JSToken != "" && time.Since(sc.fetched) < tokenTTL {
		return sc, nil
	}
	pageURL := p.siteBase(l.Host) + sharePagePath(l.SURL)
	r, err := p.get(ctx, s, pageURL, "", true, 8<<20, nil)
	if err != nil {
		return nil, err
	}
	if he := p.Classify(r.resp, r.body, nil); he != nil {
		return nil, he
	}
	t := extractTokens(r.body)
	if t.JSToken == "" {
		if ae, ok := apiErrorFromBody(r.body); ok {
			return nil, p.errnoError(ae)
		}
		return nil, hosters.NewTemporary(0, errNoToken)
	}
	final := r.resp.Request.URL
	sc.base = final.Scheme + "://" + final.Host
	if p.base != "" {
		sc.base = p.base
	}
	sc.pageURL = sc.base + sharePagePath(l.SURL)
	p.keepBrowserID(s, final, r.body)
	sc.tok = t
	sc.fetched = time.Now()
	return sc, nil
}

// keepBrowserID stores the browserid embedded in the page when the site did
// not set it as a cookie (API calls expect it).
func (p *Plugin) keepBrowserID(s *hosters.Session, pageURL *url.URL, page []byte) {
	if s.Jar == nil {
		return
	}
	for _, c := range s.Jar.Cookies(pageURL) {
		if c.Name == cookieBrowserID {
			return
		}
	}
	if id := findFirst(page, browserIDPatterns); id != "" && validCookieValue(id) {
		s.Jar.SetCookies(&url.URL{Scheme: pageURL.Scheme, Host: pageURL.Host, Path: "/"}, []*http.Cookie{{Name: cookieBrowserID, Value: id, Path: "/"}})
	}
}
