package yandex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin name.
const Name = "yandex"

func init() { hosters.RegisterFactory(Name, New) }

const (
	defaultAPIBase  = "https://cloud-api.yandex.net"
	pathResources   = "/v1/disk/public/resources"
	pathDownload    = "/v1/disk/public/resources/download"
	defaultPageSize = 200
	maxPageSize     = 1000
	maxBody         = 16 << 20
	maxDepth        = 64
	maxItems        = 200000

	metaKey  = "public_key"
	metaPath = "path"
)

// defaultDomains are the matched hostnames.
var defaultDomains = []string{
	"yadi.sk", "disk.yandex.ru", "disk.yandex.com", "disk.yandex.by", "disk.yandex.kz", "disk.yandex.ua",
	"disk.yandex.uz", "disk.yandex.az", "disk.yandex.com.tr", "disk.yandex.com.am", "disk.yandex.co.il",
	"disk.yandex.ee", "disk.yandex.lt", "disk.yandex.lv", "disk.yandex.fr", "disk.yandex.tj", "disk.yandex.tm",
	"disk.yandex.kg", "disk.yandex.md", "disk.yandex.ge", "disk.360.yandex.ru", "disk.360.yandex.com",
}

// Hoster is the Yandex Disk plugin.
type Hoster struct {
	cfg      config.HosterConfig
	apiBase  string
	pageSize int
	domains  map[string]bool
	limiter  *rate.Limiter
}

var (
	_ hosters.Hoster            = (*Hoster)(nil)
	_ hosters.StreamingResolver = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.yandex] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	h := &Hoster{cfg: cfg, apiBase: defaultAPIBase, pageSize: defaultPageSize, domains: map[string]bool{}}
	if v := strings.TrimSpace(cfg.Options["api_base"]); v != "" {
		if _, err := url.ParseRequestURI(v); err != nil {
			return nil, fmt.Errorf("yandex: invalid api_base: %w", err)
		}
		h.apiBase = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(cfg.Options["page_size"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > maxPageSize {
			return nil, fmt.Errorf("yandex: invalid page_size %q (1-%d)", v, maxPageSize)
		}
		h.pageSize = n
	}
	for _, d := range append(append([]string{}, defaultDomains...), cfg.Domains...) {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			h.domains[d] = true
		}
	}
	if cfg.RequestsPerSecond > 0 {
		h.limiter = rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 2)
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
		DefaultRetryAfter: time.Hour,
		QuotaScope:        hosters.ScopeFile,
	}
}

// parsed is a public link split into the public key URL and a sub-path.
type parsed struct {
	publicKey string // URL accepted as public_key
	subPath   string // "" or "/a/b"
}

func (h *Hoster) parse(u *url.URL) (parsed, bool) {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") {
		return parsed{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if !h.domains[host] {
		return parsed{}, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(segs) >= 2 && (segs[0] == "d" || segs[0] == "i") && segs[1] != "":
		p := parsed{publicKey: "https://" + host + "/" + segs[0] + "/" + url.PathEscape(segs[1])}
		if len(segs) > 2 {
			// path.Clean of a rooted path never climbs above "/".
			if sub := path.Clean("/" + strings.Join(segs[2:], "/")); sub != "/" {
				p.subPath = sub
			}
		}
		return p, true
	case len(segs) >= 1 && segs[0] == "public":
		hash := u.Query().Get("hash")
		if hash == "" {
			return parsed{}, false
		}
		return parsed{publicKey: hash}, true
	}
	return parsed{}, false
}

// Match implements hosters.Hoster.
func (h *Hoster) Match(u *url.URL) bool {
	_, ok := h.parse(u)
	return ok
}

// resource is a public resource (only the fields we use).
type resource struct {
	Type     string `json:"type"` // dir | file
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	MD5      string `json:"md5"`
	SHA256   string `json:"sha256"`
	Embedded *struct {
		Items  []resource `json:"items"`
		Total  int        `json:"total"`
		Limit  int        `json:"limit"`
		Offset int        `json:"offset"`
		Path   string     `json:"path"`
	} `json:"_embedded"`
}

type link struct {
	Href   string `json:"href"`
	Method string `json:"method"`
}

type apiError struct {
	Error       string `json:"error"`
	Message     string `json:"message"`
	Description string `json:"description"`
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, func(hosters.FileInfo) {})
}

// ResolveStream implements hosters.StreamingResolver.
func (h *Hoster) ResolveStream(ctx context.Context, s *hosters.Session, rawLink string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(rawLink))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("yandex: parse link: %w", err))
	}
	p, ok := h.parse(u)
	if !ok {
		return nil, hosters.NewFatal(errors.New("yandex: unsupported link"))
	}
	canon := p.publicKey + p.subPath
	if !strings.HasPrefix(canon, "http") {
		canon = u.String()
	}
	root, err := h.list(ctx, s, p.publicKey, p.subPath, 0)
	if err != nil {
		return nil, fmt.Errorf("yandex: resource: %w", err)
	}
	if root.Type == "file" {
		f := h.fileInfo(*root, "", p.publicKey, canon)
		emit(f)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
	}
	rootPath := root.Path
	if root.Embedded != nil && root.Embedded.Path != "" {
		rootPath = root.Embedded.Path
	}
	if rootPath == "" {
		rootPath = p.subPath
	}
	res := &hosters.ResolveResult{Name: root.Name, IsFolder: true}
	if res.Name == "" {
		res.Name = "yandex-disk"
	}
	count := 0
	var walk func(dir *resource, dirPath string, depth int) error
	walk = func(dir *resource, dirPath string, depth int) error {
		if depth > maxDepth {
			return hosters.NewFatal(errors.New("yandex: folder tree too deep"))
		}
		var subdirs []resource
		offset := 0
		page := dir
		for page.Embedded != nil {
			for _, it := range page.Embedded.Items {
				switch it.Type {
				case "dir":
					subdirs = append(subdirs, it)
				case "file":
					count++
					if count > maxItems {
						return hosters.NewFatal(errors.New("yandex: too many files"))
					}
					f := h.fileInfo(it, relPath(rootPath, it.Path, it.Name, dirPath), p.publicKey, canon)
					res.Files = append(res.Files, f)
					emit(f)
				}
			}
			n := len(page.Embedded.Items)
			offset += n
			if n == 0 || offset >= page.Embedded.Total {
				break
			}
			next, err := h.list(ctx, s, p.publicKey, dirPath, offset)
			if err != nil {
				return fmt.Errorf("yandex: list %s: %w", dirPath, err)
			}
			page = next
		}
		for _, d := range subdirs {
			sub, err := h.list(ctx, s, p.publicKey, d.Path, 0)
			if err != nil {
				return fmt.Errorf("yandex: list %s: %w", d.Path, err)
			}
			if err := walk(sub, d.Path, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, rootPath, 0); err != nil {
		return nil, err
	}
	return res, nil
}

// relPath returns itemPath relative to rootPath, falling back to
// dirPath/name if the API path is missing or outside the root.
func relPath(rootPath, itemPath, name, dirPath string) string {
	root := strings.TrimRight(rootPath, "/")
	if itemPath != "" && strings.HasPrefix(itemPath, root+"/") {
		return strings.TrimPrefix(itemPath, root+"/")
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(dirPath, root), "/")
	return path.Join(rel, name)
}

func (h *Hoster) fileInfo(r resource, rel, publicKey, canon string) hosters.FileInfo {
	f := hosters.FileInfo{
		ID:      publicKey + ":" + r.Path,
		Name:    r.Name,
		RelPath: rel,
		Size:    r.Size,
		Link:    canon,
		Meta:    map[string]string{metaKey: publicKey, metaPath: r.Path},
	}
	if f.Size <= 0 {
		f.Size = -1
	}
	switch {
	case len(r.SHA256) == 64:
		f.Checksum = &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: strings.ToLower(r.SHA256)}
	case len(r.MD5) == 32:
		f.Checksum = &hosters.Checksum{Algo: hosters.AlgoMD5, Value: strings.ToLower(r.MD5)}
	}
	return f
}

// list fetches a resource (and one page of its items).
func (h *Hoster) list(ctx context.Context, s *hosters.Session, publicKey, p string, offset int) (*resource, error) {
	q := url.Values{"public_key": {publicKey}, "limit": {strconv.Itoa(h.pageSize)}}
	if p != "" && p != "/" {
		q.Set("path", p)
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	var r resource
	if err := h.getJSON(ctx, s, h.apiBase+pathResources+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Prepare implements hosters.Hoster.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	key, p := f.Meta[metaKey], f.Meta[metaPath]
	if key == "" {
		u, err := url.Parse(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		pl, ok := h.parse(u)
		if !ok {
			return nil, hosters.NewFatal(errors.New("yandex: not a public link"))
		}
		key, p = pl.publicKey, pl.subPath
	}
	q := url.Values{"public_key": {key}}
	if p != "" && p != "/" {
		q.Set("path", p)
	}
	var l link
	if err := h.getJSON(ctx, s, h.apiBase+pathDownload+"?"+q.Encode(), &l); err != nil {
		return nil, fmt.Errorf("yandex: download link: %w", err)
	}
	du, err := url.Parse(l.Href)
	if err != nil || (du.Scheme != "https" && du.Scheme != "http") || du.Host == "" {
		return nil, hosters.NewTemporary(0, errors.New("yandex: invalid download href"))
	}
	spec := &hosters.DownloadSpec{URL: l.Href, Size: f.Size, Resumable: true}
	if m := strings.ToUpper(l.Method); m != "" && m != http.MethodGet {
		spec.Method = m
	}
	return spec, nil
}

func (h *Hoster) getJSON(ctx context.Context, s *hosters.Session, u string, out any) error {
	if h.limiter != nil {
		if err := h.limiter.Wait(ctx); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return hosters.NewFatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return hosters.NewTemporary(0, err)
	}
	if err := h.Classify(resp, body, nil); err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return hosters.NewFatal(fmt.Errorf("decode response: %w", err))
	}
	return nil
}

// Classify implements hosters.Hoster.
func (h *Hoster) Classify(resp *http.Response, body []byte, err error) error {
	if err != nil || resp == nil {
		return hosters.DefaultClassify(resp, body, err)
	}
	code := resp.StatusCode
	if code < 400 {
		return nil
	}
	var ae apiError
	if json.Unmarshal(body, &ae) == nil && ae.Error != "" {
		c := strings.ToLower(ae.Error)
		e := fmt.Errorf("yandex: %s", ae.Error)
		retry := hosters.ParseRetryAfter(resp.Header, time.Now())
		switch {
		case strings.Contains(c, "downloadlimitexceeded"), strings.Contains(c, "limitexceeded") && !strings.Contains(c, "toomany"):
			return hosters.NewQuota(hosters.ScopeFile, retry, e)
		case strings.Contains(c, "toomanyrequests"), code == http.StatusTooManyRequests:
			return hosters.NewQuota(hosters.ScopeIP, retry, e)
		case strings.Contains(c, "notfound"), strings.Contains(c, "doesntexist"), strings.Contains(c, "notexist"):
			return hosters.NewNotFound(e)
		case strings.Contains(c, "password"), strings.Contains(c, "unauthorized"), strings.Contains(c, "forbidden"),
			strings.Contains(c, "accessdenied"):
			return hosters.NewNeedsAuth(e)
		case strings.Contains(c, "blocked"), strings.Contains(c, "infected"):
			return hosters.NewNotFound(e)
		case code >= 500:
			return hosters.NewTemporary(retry, e)
		default:
			return hosters.NewFatal(e)
		}
	}
	return hosters.DefaultClassify(resp, body, nil)
}
