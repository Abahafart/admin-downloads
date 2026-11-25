package onedrive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin name.
const Name = "onedrive"

func init() { hosters.RegisterFactory(Name, New) }

const (
	defaultAPIBase = "https://api.onedrive.com/v1.0"
	maxBody        = 16 << 20
	maxDepth       = 64
	// downloadURLTTL is how long a @content.downloadUrl is trusted.
	downloadURLTTL = 45 * time.Minute

	metaItem = "item"
	metaRoot = "root"
)

// Hoster is the OneDrive plugin.
type Hoster struct {
	cfg     config.HosterConfig
	apiBase *url.URL
	limiter *rate.Limiter
}

var (
	_ hosters.Hoster            = (*Hoster)(nil)
	_ hosters.StreamingResolver = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.onedrive] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	base := defaultAPIBase
	if v := strings.TrimSpace(cfg.Options["api_base"]); v != "" {
		base = v
	}
	u, err := url.ParseRequestURI(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("onedrive: invalid api_base: %w", err)
	}
	h := &Hoster{cfg: cfg, apiBase: u}
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
		DefaultRetryAfter: 5 * time.Minute,
		QuotaScope:        hosters.ScopeIP,
	}
}

// Match implements hosters.Hoster.
func (h *Hoster) Match(u *url.URL) bool {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	p := strings.Trim(u.Path, "/")
	switch host {
	case "1drv.ms":
		return p != "" && strings.Contains(p, "/")
	case "onedrive.live.com", "www.onedrive.live.com":
		q := u.Query()
		if q.Get("resid") != "" || q.Get("id") != "" || q.Get("redeem") != "" || q.Get("cid") != "" && q.Get("authkey") != "" {
			return true
		}
		return false
	}
	return false
}

// ShareID returns the share id of a sharing URL: "u!" followed by the
// unpadded base64url encoding of the URL.
func ShareID(link string) string {
	return "u!" + base64.RawURLEncoding.EncodeToString([]byte(link))
}

// item is a driveItem (only the fields we use).
type item struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"@content.downloadUrl"`
	File        *struct {
		Hashes struct {
			SHA1   string `json:"sha1Hash"`
			SHA256 string `json:"sha256Hash"`
			CRC32  string `json:"crc32Hash"`
		} `json:"hashes"`
	} `json:"file"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
}

type children struct {
	Value    []item `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (h *Hoster) shareURL(share string, parts ...string) string {
	return h.apiBase.String() + "/shares/" + share + "/" + strings.Join(parts, "/")
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, func(hosters.FileInfo) {})
}

// ResolveStream implements hosters.StreamingResolver.
func (h *Hoster) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	link = strings.TrimSpace(link)
	u, err := url.Parse(link)
	if err != nil || !h.Match(u) {
		return nil, hosters.NewFatal(errors.New("onedrive: unsupported link"))
	}
	share := ShareID(link)
	var root item
	if err := h.getJSON(ctx, s, h.shareURL(share, "root"), &root); err != nil {
		return nil, fmt.Errorf("onedrive: shared item: %w", err)
	}
	if root.Folder == nil {
		f := toFileInfo(root, "", link)
		f.Meta[metaRoot] = "1"
		emit(f)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
	}
	res := &hosters.ResolveResult{Name: root.Name, IsFolder: true}
	if res.Name == "" {
		res.Name = "onedrive-" + root.ID
	}
	var walk func(listURL, prefix string, depth int) error
	walk = func(listURL, prefix string, depth int) error {
		if depth > maxDepth {
			return hosters.NewFatal(errors.New("onedrive: folder tree too deep"))
		}
		var dirs []item
		next := listURL
		for next != "" {
			var page children
			if err := h.getJSON(ctx, s, next, &page); err != nil {
				return fmt.Errorf("onedrive: list children: %w", err)
			}
			for _, it := range page.Value {
				if strings.TrimSpace(it.Name) == "" {
					continue
				}
				if it.Folder != nil {
					dirs = append(dirs, it)
					continue
				}
				f := toFileInfo(it, path.Join(prefix, it.Name), link)
				res.Files = append(res.Files, f)
				emit(f)
			}
			next = ""
			if page.NextLink != "" {
				nu, err := url.Parse(page.NextLink)
				if err != nil || nu.Scheme != h.apiBase.Scheme || nu.Host != h.apiBase.Host {
					return hosters.NewFatal(errors.New("onedrive: unexpected @odata.nextLink host"))
				}
				next = nu.String()
			}
		}
		for _, d := range dirs {
			if err := walk(h.shareURL(share, "items", url.PathEscape(d.ID), "children"), path.Join(prefix, d.Name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(h.shareURL(share, "root", "children"), "", 0); err != nil {
		return nil, err
	}
	return res, nil
}

func toFileInfo(it item, relPath, link string) hosters.FileInfo {
	f := hosters.FileInfo{
		ID:      it.ID,
		Name:    it.Name,
		RelPath: relPath,
		Size:    it.Size,
		Link:    link,
		Meta:    map[string]string{metaItem: it.ID},
	}
	if f.Size <= 0 {
		f.Size = -1
	}
	if it.File != nil {
		hs := it.File.Hashes
		switch {
		case hs.SHA256 != "":
			f.Checksum = &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: strings.ToLower(hs.SHA256)}
		case hs.SHA1 != "":
			f.Checksum = &hosters.Checksum{Algo: hosters.AlgoSHA1, Value: strings.ToLower(hs.SHA1)}
		case hs.CRC32 != "":
			f.Checksum = &hosters.Checksum{Algo: hosters.AlgoCRC32, Value: strings.ToLower(hs.CRC32)}
		}
	}
	return f
}

// Prepare implements hosters.Hoster.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	share := ShareID(strings.TrimSpace(f.Link))
	var itemPath []string
	if f.Meta[metaRoot] == "1" || f.Meta[metaItem] == "" && f.ID == "" {
		itemPath = []string{"root"}
	} else {
		id := f.Meta[metaItem]
		if id == "" {
			id = f.ID
		}
		itemPath = []string{"items", url.PathEscape(id)}
	}
	var it item
	if err := h.getJSON(ctx, s, h.shareURL(share, itemPath...), &it); err != nil {
		return nil, fmt.Errorf("onedrive: item: %w", err)
	}
	if it.Folder != nil {
		return nil, hosters.NewFatal(errors.New("onedrive: item is a folder"))
	}
	spec := &hosters.DownloadSpec{Filename: it.Name, Size: it.Size, Resumable: true}
	if spec.Size <= 0 {
		spec.Size = -1
	}
	if du, err := url.Parse(it.DownloadURL); err == nil && (du.Scheme == "https" || du.Scheme == "http") && du.Host != "" {
		spec.URL = it.DownloadURL
		spec.Expires = time.Now().Add(downloadURLTTL)
	} else {
		spec.URL = h.shareURL(share, append(itemPath, "content")...)
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
	if json.Unmarshal(body, &ae) == nil && ae.Error.Code != "" {
		c := strings.ToLower(ae.Error.Code)
		e := fmt.Errorf("onedrive: %s", ae.Error.Code)
		retry := hosters.ParseRetryAfter(resp.Header, time.Now())
		switch {
		case strings.Contains(c, "notfound"), c == "resourcemodified" && code == http.StatusNotFound:
			return hosters.NewNotFound(e)
		case strings.Contains(c, "accessdenied"), strings.Contains(c, "unauthenticated"), strings.Contains(c, "invalidauthenticationtoken"),
			code == http.StatusUnauthorized:
			return hosters.NewNeedsAuth(e)
		case strings.Contains(c, "activitylimitreached"), strings.Contains(c, "toomanyrequests"), code == http.StatusTooManyRequests:
			return hosters.NewQuota(hosters.ScopeIP, retry, e)
		case strings.Contains(c, "quotalimitreached"):
			return hosters.NewQuota(hosters.ScopeFile, retry, e)
		case code >= 500, strings.Contains(c, "servicenotavailable"):
			return hosters.NewTemporary(retry, e)
		}
	}
	if code == http.StatusForbidden && strings.Contains(strings.ToLower(string(body)), "accessdenied") {
		return hosters.NewNeedsAuth(fmt.Errorf("onedrive: HTTP %d", code))
	}
	return hosters.DefaultClassify(resp, body, nil)
}
