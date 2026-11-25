package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin name.
const Name = "dropbox"

func init() { hosters.RegisterFactory(Name, New) }

const (
	maxBody  = 16 << 20
	maxDepth = 64

	modeAuto = "auto"
	modeZip  = "zip"
	modeAPI  = "api"

	metaMode = "mode"
	metaPath = "path"
)

// Hoster is the Dropbox plugin.
type Hoster struct {
	cfg         config.HosterConfig
	apiBase     string
	contentBase string
	webBase     *url.URL
	folderMode  string
}

var (
	_ hosters.Hoster            = (*Hoster)(nil)
	_ hosters.StreamingResolver = (*Hoster)(nil)
	_ hosters.AccountChecker    = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.dropbox] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	h := &Hoster{cfg: cfg, apiBase: defaultAPIBase, contentBase: defaultContentBase, folderMode: modeAuto}
	for key, dst := range map[string]*string{"api_base": &h.apiBase, "content_base": &h.contentBase} {
		if v := strings.TrimSpace(cfg.Options[key]); v != "" {
			if _, err := url.ParseRequestURI(v); err != nil {
				return nil, fmt.Errorf("dropbox: invalid %s: %w", key, err)
			}
			*dst = strings.TrimRight(v, "/")
		}
	}
	if v := strings.TrimSpace(cfg.Options["web_base"]); v != "" {
		u, err := url.ParseRequestURI(v)
		if err != nil {
			return nil, fmt.Errorf("dropbox: invalid web_base: %w", err)
		}
		h.webBase = u
	}
	switch m := strings.ToLower(strings.TrimSpace(cfg.Options["folder_mode"])); m {
	case "":
	case modeAuto, modeZip, modeAPI:
		h.folderMode = m
	default:
		return nil, fmt.Errorf("dropbox: invalid folder_mode %q (auto, zip, api)", m)
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
		Resumable:         true,
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindAPIKey, accounts.KindOAuth},
		DefaultRetryAfter: time.Hour,
		QuotaScope:        hosters.ScopeFile,
	}
}

type linkInfo struct {
	folder bool
	short  bool
	id     string
	name   string
}

func parseLink(u *url.URL) (linkInfo, bool) {
	if u == nil || !hosts[strings.ToLower(u.Hostname())] {
		return linkInfo{}, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if strings.EqualFold(u.Hostname(), "db.tt") {
		return linkInfo{short: true, id: segs[0]}, segs[0] != ""
	}
	at := func(i int) string {
		if i < len(segs) {
			return segs[i]
		}
		return ""
	}
	var li linkInfo
	switch {
	case at(0) == "s" && at(1) != "":
		li = linkInfo{id: at(1), name: at(2)}
	case at(0) == "scl" && at(1) == "fi" && at(2) != "":
		li = linkInfo{id: at(2), name: at(3)}
	case at(0) == "scl" && at(1) == "fo" && at(2) != "":
		li = linkInfo{folder: true, id: at(2)}
	case at(0) == "sh" && at(1) != "":
		li = linkInfo{folder: true, id: at(1), name: at(3)}
	default:
		return linkInfo{}, false
	}
	return li, true
}

// Match implements hosters.Hoster.
func (h *Hoster) Match(u *url.URL) bool {
	_, ok := parseLink(u)
	return ok
}

// rewrite applies web_base (tests) to a shared link.
func (h *Hoster) rewrite(u *url.URL) *url.URL {
	c := *u
	if h.webBase != nil {
		c.Scheme, c.Host = h.webBase.Scheme, h.webBase.Host
	}
	return &c
}

// directURL returns the dl=1 variant of a shared link.
func (h *Hoster) directURL(link string) (string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", err
	}
	u = h.rewrite(u)
	if strings.EqualFold(u.Hostname(), "www.dropbox.com") || strings.EqualFold(u.Hostname(), "dropbox.com") || h.webBase != nil {
		q := u.Query()
		q.Del("raw")
		q.Set("dl", "1")
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	return u.String(), nil
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, func(hosters.FileInfo) {})
}

// ResolveStream implements hosters.StreamingResolver.
func (h *Hoster) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("dropbox: parse link: %w", err))
	}
	li, ok := parseLink(u)
	if !ok {
		return nil, hosters.NewFatal(errors.New("dropbox: unsupported link"))
	}
	if li.short {
		if u, err = h.expandShort(ctx, s, u); err != nil {
			return nil, err
		}
		if li, ok = parseLink(u); !ok || li.short {
			return nil, hosters.NewFatal(errors.New("dropbox: short link does not lead to a shared file or folder"))
		}
	}
	u.Fragment = ""
	link = u.String()

	if !li.folder {
		f := hosters.FileInfo{ID: li.id, Name: li.name, Size: -1, Link: link}
		if name, size, err := h.probe(ctx, s, link); err != nil {
			if !hosters.IsTemporary(err) {
				return nil, err
			}
			s.Log.Debug("dropbox: probe failed, size unknown", "id", li.id, "err", err)
		} else {
			if name != "" {
				f.Name = name
			}
			f.Size = size
		}
		if f.Name == "" {
			f.Name = "dropbox-" + li.id
		}
		emit(f)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
	}

	useAPI := h.folderMode == modeAPI || (h.folderMode == modeAuto && !s.Anonymous())
	if useAPI {
		token, err := h.token(ctx, s)
		if err != nil {
			return nil, err
		}
		return h.listFolder(ctx, s, token, link, li, emit)
	}
	// Whole folder as one zip.
	f := hosters.FileInfo{ID: li.id, Size: -1, Link: link, Meta: map[string]string{metaMode: modeZip}}
	name, size, err := h.probe(ctx, s, link)
	if err != nil && !hosters.IsTemporary(err) {
		return nil, err
	}
	f.Name, f.Size = name, size
	if f.Name == "" {
		f.Name = "dropbox-" + li.id + ".zip"
		f.Size = -1
	}
	emit(f)
	return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
}

// expandShort follows one redirect of a db.tt link.
func (h *Hoster) expandShort(ctx context.Context, s *hosters.Session, u *url.URL) (*url.URL, error) {
	c := *s.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.rewrite(u).String(), nil)
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		if err := h.Classify(resp, nil, nil); err != nil {
			return nil, err
		}
		return nil, hosters.NewFatal(errors.New("dropbox: short link did not redirect"))
	}
	loc, err := resp.Location()
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("dropbox: short link redirect: %w", err))
	}
	return loc, nil
}

// probe sends HEAD to the dl=1 URL and returns the advertised name and size
// (-1 if unknown).
func (h *Hoster) probe(ctx context.Context, s *hosters.Session, link string) (string, int64, error) {
	du, err := h.directURL(link)
	if err != nil {
		return "", -1, hosters.NewFatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, du, nil)
	if err != nil {
		return "", -1, hosters.NewFatal(err)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", -1, h.Classify(nil, nil, err)
	}
	resp.Body.Close()
	if err := h.Classify(resp, nil, nil); err != nil {
		return "", -1, err
	}
	name := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	size := int64(-1)
	if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil && n > 0 {
		size = n
	}
	return name, size, nil
}

// listFolder lists a shared folder recursively through the API.
func (h *Hoster) listFolder(ctx context.Context, s *hosters.Session, token, link string, li linkInfo, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	res := &hosters.ResolveResult{IsFolder: true, Name: li.name}
	var walk func(rel string, depth int) error
	walk = func(rel string, depth int) error {
		if depth > maxDepth {
			return hosters.NewFatal(errors.New("dropbox: folder tree too deep"))
		}
		apiPath := ""
		if rel != "" {
			apiPath = "/" + rel
		}
		var page listFolderResult
		if err := h.rpc(ctx, s, token, h.apiBase+pathListFolder, listFolderArg{Path: apiPath, SharedLink: sharedLink{URL: link}, Limit: 2000}, &page); err != nil {
			return fmt.Errorf("dropbox: list folder: %w", err)
		}
		var subdirs []string
		for {
			for _, e := range page.Entries {
				name := strings.TrimSpace(e.Name)
				if name == "" {
					continue
				}
				switch e.Tag {
				case "file":
					f := hosters.FileInfo{
						ID:      e.ID,
						Name:    e.Name,
						RelPath: path.Join(rel, e.Name),
						Size:    e.Size,
						Link:    link,
						Meta:    map[string]string{metaMode: modeAPI, metaPath: "/" + path.Join(rel, e.Name)},
					}
					if f.Size <= 0 {
						f.Size = -1
					}
					res.Files = append(res.Files, f)
					emit(f)
				case "folder":
					subdirs = append(subdirs, path.Join(rel, e.Name))
				}
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			cursor := page.Cursor
			page = listFolderResult{}
			if err := h.rpc(ctx, s, token, h.apiBase+pathListFolderContinue, map[string]string{"cursor": cursor}, &page); err != nil {
				return fmt.Errorf("dropbox: list folder (continue): %w", err)
			}
		}
		for _, d := range subdirs {
			if err := walk(d, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if res.Name == "" {
		var meta entry
		if err := h.rpc(ctx, s, token, h.apiBase+pathSharedLinkMeta, sharedLink{URL: link}, &meta); err == nil {
			res.Name = strings.TrimSpace(meta.Name)
		} else if !hosters.IsTemporary(err) && !hosters.IsFatal(err) {
			return nil, fmt.Errorf("dropbox: shared link metadata: %w", err)
		}
	}
	if err := walk("", 0); err != nil {
		return nil, err
	}
	if res.Name == "" {
		res.Name = "dropbox-" + li.id
	}
	return res, nil
}

// Prepare implements hosters.Hoster.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	switch f.Meta[metaMode] {
	case modeAPI:
		token, err := h.token(ctx, s)
		if err != nil {
			return nil, err
		}
		arg, err := headerJSON(map[string]string{"url": f.Link, "path": f.Meta[metaPath]})
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer "+token)
		hdr.Set("Dropbox-API-Arg", arg)
		return &hosters.DownloadSpec{
			URL:       h.contentBase + pathSharedLinkFile,
			Method:    http.MethodPost,
			Header:    hdr,
			Size:      f.Size,
			Resumable: true,
		}, nil
	case modeZip:
		du, err := h.directURL(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		// The zip is generated on the fly: no Range, unknown size.
		return &hosters.DownloadSpec{URL: du, Size: -1, Resumable: false, MaxConns: 1}, nil
	default:
		du, err := h.directURL(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		return &hosters.DownloadSpec{URL: du, Size: f.Size, Resumable: true}, nil
	}
}

// token returns the access token of the session's account.
func (h *Hoster) token(ctx context.Context, s *hosters.Session) (string, error) {
	if s.Anonymous() {
		return "", hosters.NewNeedsAuth(errors.New("dropbox: folder listing through the API needs an access token account"))
	}
	sec, err := s.Secret(ctx)
	if err != nil {
		return "", hosters.NewNeedsAuth(fmt.Errorf("dropbox: load token: %w", err))
	}
	if t := strings.TrimSpace(sec.APIKey); t != "" {
		return t, nil
	}
	if sec.OAuthToken != nil && sec.OAuthToken.AccessToken != "" {
		if !sec.OAuthToken.Expiry.IsZero() && time.Now().After(sec.OAuthToken.Expiry) {
			return "", hosters.NewNeedsAuth(errors.New("dropbox: access token expired"))
		}
		return sec.OAuthToken.AccessToken, nil
	}
	return "", hosters.NewNeedsAuth(errors.New("dropbox: account has no access token"))
}

// rpc POSTs a JSON RPC request to the Dropbox API.
func (h *Hoster) rpc(ctx context.Context, s *hosters.Session, token, u string, in, out any) error {
	body := []byte("null")
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return hosters.NewFatal(err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return hosters.NewFatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return hosters.NewTemporary(0, err)
	}
	if err := h.Classify(resp, raw, nil); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return hosters.NewFatal(fmt.Errorf("decode response: %w", err))
	}
	return nil
}

// CheckAccount implements hosters.AccountChecker.
func (h *Hoster) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	token, err := h.token(ctx, s)
	if err != nil {
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "sin token de acceso", QuotaTotal: -1, QuotaUsed: -1}, nil //nolint:nilerr // the failure is reported as the account status
	}
	var acc currentAccount
	err = h.rpc(ctx, s, token, h.apiBase+pathCurrentAccount, nil, &acc)
	if hosters.IsNeedsAuth(err) {
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "token rechazado o caducado", QuotaTotal: -1, QuotaUsed: -1}, nil
	}
	if err != nil {
		return nil, err
	}
	tag := acc.AccountType.Tag
	return &accounts.CheckResult{Status: accounts.StatusOK, Message: tag, Premium: tag != "" && tag != "basic", QuotaTotal: -1, QuotaUsed: -1}, nil
}

// Classify implements hosters.Hoster.
func (h *Hoster) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil || resp == nil {
		return hosters.DefaultClassify(resp, body, err)
	}
	lower := strings.ToLower(string(body))
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	code := resp.StatusCode

	// API errors (JSON with error_summary).
	var ae apiError
	if code >= 400 && json.Unmarshal(body, &ae) == nil && ae.ErrorSummary != "" {
		sum := strings.ToLower(ae.ErrorSummary)
		e := fmt.Errorf("dropbox: %s", strings.TrimRight(ae.ErrorSummary, "/."))
		switch {
		case strings.Contains(sum, "not_found"), strings.Contains(sum, "not_file"), strings.Contains(sum, "shared_link_not_found"):
			return hosters.NewNotFound(e)
		case strings.Contains(sum, "access_denied"), strings.Contains(sum, "invalid_access_token"), strings.Contains(sum, "expired_access_token"),
			strings.Contains(sum, "missing_scope"), strings.Contains(sum, "email_not_verified"), code == http.StatusUnauthorized:
			return hosters.NewNeedsAuth(e)
		case strings.Contains(sum, "too_many"), code == http.StatusTooManyRequests:
			return hosters.NewTemporary(hosters.ParseRetryAfter(resp.Header, time.Now()), e)
		case code >= 500:
			return hosters.NewTemporary(0, e)
		default:
			return hosters.NewFatal(e)
		}
	}
	if containsAny(lower, trafficPhrases) || code == http.StatusTooManyRequests || code == 509 {
		return hosters.NewQuota(hosters.ScopeFile, hosters.ParseRetryAfter(resp.Header, time.Now()),
			fmt.Errorf("dropbox: link traffic limit reached (HTTP %d)", code))
	}
	if code < 400 {
		// An HTML page instead of an attachment means an interstitial.
		if strings.HasPrefix(ct, "text/html") && resp.Header.Get("Content-Disposition") == "" && len(body) > 0 {
			if containsAny(lower, passwordPhrases) {
				return hosters.NewNeedsAuth(errors.New("dropbox: link is password protected"))
			}
		}
		return nil
	}
	if containsAny(lower, tooLargePhrases) {
		return hosters.NewFatal(errors.New("dropbox: folder too large to download as zip (add a Dropbox access token account to list it through the API)"))
	}
	if code == http.StatusForbidden && containsAny(lower, passwordPhrases) {
		return hosters.NewNeedsAuth(errors.New("dropbox: link is password protected"))
	}
	if code == http.StatusForbidden || code == http.StatusGone {
		// Shared links do not expire like signed URLs: 403/410 means the
		// link was disabled or the file removed.
		return hosters.NewNotFound(fmt.Errorf("dropbox: HTTP %d", code))
	}
	return hosters.DefaultClassify(resp, body, nil)
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// headerJSON encodes v as JSON with every non-ASCII character escaped, as
// required for the Dropbox-API-Arg HTTP header.
func headerJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, r := range string(b) {
		switch {
		case r < 0x80:
			sb.WriteRune(r)
		case r > 0xFFFF:
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&sb, `\u%04x\u%04x`, r1, r2)
		default:
			fmt.Fprintf(&sb, `\u%04x`, r)
		}
	}
	return sb.String(), nil
}
