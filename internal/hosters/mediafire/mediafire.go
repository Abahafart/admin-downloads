package mediafire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
)

// Name is the plugin identifier.
const Name = "mediafire"

func init() { hosters.RegisterFactory(Name, New) }

// Hoster is the MediaFire plugin. It is safe for concurrent use.
type Hoster struct {
	cfg     config.HosterConfig
	domains []string
	limiter *rate.Limiter

	// Base URLs (overridden by tests).
	webBase string
	apiBase string
}

var (
	_ hosters.Hoster            = (*Hoster)(nil)
	_ hosters.StreamingResolver = (*Hoster)(nil)
	_ hosters.AccountChecker    = (*Hoster)(nil)
	_ hosters.SelfTester        = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.mediafire] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = defaultRPS
	}
	domains := []string{"mediafire.com", "www.mediafire.com", "m.mediafire.com"}
	for _, d := range cfg.Domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			domains = append(domains, d)
		}
	}
	return &Hoster{
		cfg:     cfg,
		domains: domains,
		limiter: rate.NewLimiter(rate.Limit(rps), 2),
		webBase: defaultWebBase,
		apiBase: defaultAPIBase,
	}, nil
}

// Name implements hosters.Hoster.
func (h *Hoster) Name() string { return Name }

// Caps implements hosters.Hoster.
func (h *Hoster) Caps() hosters.Caps {
	c := hosters.Caps{
		MaxConnsPerFile:   4,
		MaxParallel:       4,
		Resumable:         true,
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindCookie},
		DefaultRetryAfter: captchaRetryAfter,
		QuotaScope:        hosters.ScopeIP,
	}
	if h.cfg.MaxConnsPerFile > 0 {
		c.MaxConnsPerFile = h.cfg.MaxConnsPerFile
	}
	if h.cfg.MaxParallel > 0 {
		c.MaxParallel = h.cfg.MaxParallel
	}
	if h.cfg.RetryAfter.Duration > 0 {
		c.DefaultRetryAfter = h.cfg.RetryAfter.Duration
	}
	return c
}

// linkKind is what a MediaFire URL points to.
type linkKind int

const (
	kindNone    linkKind = iota
	kindFile             // quick key
	kindFolder           // folder key
	kindUnknown          // /?<key>: file or folder, decided by the API
)

type parsedLink struct {
	kind linkKind
	key  string
}

// Match implements hosters.Hoster (no I/O).
func (h *Hoster) Match(u *url.URL) bool {
	return h.parse(u).kind != kindNone
}

func (h *Hoster) hostOK(host string) bool {
	host = strings.ToLower(host)
	for _, d := range h.domains {
		if host == d {
			return true
		}
	}
	return false
}

func (h *Hoster) parse(u *url.URL) parsedLink {
	if u == nil || !h.hostOK(u.Hostname()) {
		return parsedLink{}
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(segs) >= 2 {
		key := segs[1]
		switch strings.ToLower(segs[0]) {
		case "file", "file_premium", "view", "download":
			if reKey.MatchString(key) {
				return parsedLink{kindFile, key}
			}
		case "folder":
			if reKey.MatchString(key) {
				return parsedLink{kindFolder, key}
			}
		}
	}
	// /view/?<key>, /download.php?<key>, /file/?<key>, /?<key>
	q := firstQueryKey(u.RawQuery)
	if q != "" {
		switch {
		case len(segs) == 0:
			return parsedLink{kindUnknown, q}
		case len(segs) == 1 && (segs[0] == "download.php" || segs[0] == "file" || segs[0] == "view" || segs[0] == "download"):
			return parsedLink{kindFile, q}
		case len(segs) == 1 && segs[0] == "folder":
			return parsedLink{kindFolder, q}
		}
	}
	// /#<folderkey> and /folder/#<key>
	if (len(segs) == 0 || (len(segs) == 1 && segs[0] == "folder")) && reKey.MatchString(u.Fragment) {
		return parsedLink{kindFolder, u.Fragment}
	}
	return parsedLink{}
}

// firstQueryKey returns the bare key of "?<key>" or "?quickkey=<key>"
// style queries.
func firstQueryKey(raw string) string {
	if raw == "" {
		return ""
	}
	first, _, _ := strings.Cut(raw, "&")
	if k, v, ok := strings.Cut(first, "="); ok {
		switch strings.ToLower(k) {
		case "quickkey", "quick_key", "key", "folderkey", "folder_key":
			first = v
		default:
			return ""
		}
	}
	if reKey.MatchString(first) {
		return first
	}
	return ""
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, nil)
}

// ResolveStream implements hosters.StreamingResolver.
func (h *Hoster) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("mediafire: parse link: %w", err))
	}
	pl := h.parse(u)
	if err := h.ensureLogin(ctx, s); err != nil {
		return nil, err
	}
	switch pl.kind {
	case kindFile:
		return h.resolveFile(ctx, s, pl.key, emit)
	case kindFolder:
		return h.resolveFolder(ctx, s, pl.key, emit)
	case kindUnknown:
		res, err := h.resolveFile(ctx, s, pl.key, emit)
		if err == nil || !hosters.IsNotFound(err) {
			return res, err
		}
		return h.resolveFolder(ctx, s, pl.key, emit)
	default:
		return nil, hosters.NewFatal(fmt.Errorf("mediafire: unsupported link"))
	}
}

func (h *Hoster) resolveFile(ctx context.Context, s *hosters.Session, key string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	fi, err := h.fileInfo(ctx, s, key)
	if err != nil {
		return nil, err
	}
	f := fi.toFileInfo(h.webBase, "", false)
	if emit != nil {
		emit(f)
	}
	return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
}

func (h *Hoster) resolveFolder(ctx context.Context, s *hosters.Session, key string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	info, err := h.folderInfo(ctx, s, key)
	if err != nil {
		return nil, err
	}
	res := &hosters.ResolveResult{Name: info.Name, IsFolder: true}
	if res.Name == "" {
		res.Name = key
	}
	visited := map[string]bool{}
	var walk func(folderKey, prefix string, depth int) error
	walk = func(folderKey, prefix string, depth int) error {
		if visited[folderKey] {
			return nil
		}
		visited[folderKey] = true
		if depth > maxFolderDepth {
			return hosters.NewFatal(fmt.Errorf("mediafire: folder tree deeper than %d levels", maxFolderDepth))
		}
		files, err := h.folderContent(ctx, s, folderKey, "files")
		if err != nil {
			return err
		}
		for _, fl := range files.Files {
			f := fl.toFileInfo(h.webBase, prefix, true)
			res.Files = append(res.Files, f)
			if emit != nil {
				emit(f)
			}
		}
		folders, err := h.folderContent(ctx, s, folderKey, "folders")
		if err != nil {
			return err
		}
		for _, sub := range folders.Folders {
			if sub.FolderKey == "" {
				continue
			}
			name := fsutil.SanitizeName(sub.Name)
			if err := walk(sub.FolderKey, path.Join(prefix, name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(key, "", 0); err != nil {
		return nil, err
	}
	return res, nil
}

// Prepare implements hosters.Hoster: it scrapes a fresh direct link from
// the file page.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	key := f.ID
	if key == "" {
		if u, err := url.Parse(f.Link); err == nil {
			key = h.parse(u).key
		}
	}
	if key == "" {
		return nil, hosters.NewFatal(errors.New("mediafire: file has no quick key"))
	}
	if err := h.ensureLogin(ctx, s); err != nil {
		return nil, err
	}
	direct, err := h.directLink(ctx, s, key)
	if err != nil {
		return nil, err
	}
	spec := &hosters.DownloadSpec{
		URL:       direct,
		Size:      f.Size,
		Resumable: true,
		Checksum:  f.Checksum,
		Header:    http.Header{"User-Agent": {userAgent}},
	}
	return spec, nil
}
