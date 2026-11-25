package terabox

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"admindescargas/internal/hosters"
)

// FileInfo.Meta keys (no signed URLs are persisted: dlinks are re-listed).
const (
	metaSURL = "surl"
	metaFsID = "fs_id"
	metaDir  = "dir" // remote parent directory ("" = share root)
	metaHost = "host"
	metaMD5  = "md5"
)

// listItem is one entry of /share/list or /api/shorturlinfo.
type listItem struct {
	FsID  flexString `json:"fs_id"`
	Name  string     `json:"server_filename"`
	Path  string     `json:"path"`
	Size  flexInt    `json:"size"`
	IsDir flexInt    `json:"isdir"`
	MD5   string     `json:"md5"`
	DLink string     `json:"dlink"`
}

type listResponse struct {
	Errno   flexInt    `json:"errno"`
	Errmsg  string     `json:"errmsg"`
	ShowMsg string     `json:"show_msg"`
	List    []listItem `json:"list"`
	HasMore *flexInt   `json:"has_more"`
	Title   string     `json:"title"`
}

func (r listResponse) apiError() *APIError {
	if r.Errno == 0 {
		return nil
	}
	msg := r.ShowMsg
	if msg == "" {
		msg = r.Errmsg
	}
	return &APIError{Errno: int(r.Errno), Msg: truncate(msg, 200)}
}

// apiGet calls a JSON endpoint of the share, reloading the page tokens once
// if the errno says they are stale.
func (p *Plugin) apiGet(ctx context.Context, s *hosters.Session, l shareLink, endpoint string, params func(tokens) url.Values, out *listResponse) error {
	sc, err := p.shareContext(ctx, s, l, false)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		base, referer, tok := sc.get()
		r, err := p.get(ctx, s, base+endpoint+"?"+params(tok).Encode(), referer, true, 16<<20, http.Header{"Accept": {"application/json, text/plain, */*"}})
		if err != nil {
			return err
		}
		if r.resp.StatusCode != http.StatusOK {
			if he := p.Classify(r.resp, r.body, nil); he != nil {
				return he
			}
		}
		*out = listResponse{}
		if err := json.Unmarshal(r.body, out); err != nil {
			return hosters.NewTemporary(0, fmt.Errorf("terabox: unexpected %s response (not JSON)", endpoint))
		}
		ae := out.apiError()
		if ae == nil {
			return nil
		}
		if ruleFor(ae.Errno, ae.Msg).refresh && attempt == 0 {
			if sc, err = p.shareContext(ctx, s, l, true); err != nil {
				return err
			}
			continue
		}
		return p.errnoError(ae)
	}
}

// listDir lists every entry of dir ("" = root), page by page.
func (p *Plugin) listDir(ctx context.Context, s *hosters.Session, l shareLink, dir string, fn func(listItem) error) error {
	st := p.state(s)
	for page := 1; page <= maxPages; page++ {
		var lr listResponse
		err := p.apiGet(ctx, s, l, pathShareList, func(t tokens) url.Values {
			return shareListParams(t, l.SURL, dir, page, p.pageSize)
		}, &lr)
		if err != nil {
			return err
		}
		for _, it := range lr.List {
			if it.IsDir == 0 {
				st.putDLink(l.SURL, string(it.FsID), it.DLink)
			}
			if err := fn(it); err != nil {
				return err
			}
		}
		if len(lr.List) < p.pageSize || (lr.HasMore != nil && *lr.HasMore == 0) {
			return nil
		}
	}
	return hosters.NewFatal(fmt.Errorf("terabox: directory has more than %d pages", maxPages))
}

// Resolve implements hosters.Hoster.
func (p *Plugin) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return p.ResolveStream(ctx, s, link, nil)
}

var errStop = errors.New("stop")

// ResolveStream implements hosters.StreamingResolver: files are emitted as
// each directory page arrives.
func (p *Plugin) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !p.Match(u) {
		return nil, hosters.NewFatal(errNotShare)
	}
	l, _ := parseShareLink(u)

	// shorturlinfo validates the share (not found / login) and gives its title.
	var info listResponse
	if err := p.apiGet(ctx, s, l, pathShortURLInfo, func(t tokens) url.Values { return shortURLInfoParams(t, l.SURL) }, &info); err != nil {
		var he *hosters.Error
		if errors.As(err, &he) && he.Kind != hosters.KindTemporary {
			return nil, err
		}
		if s.Log != nil {
			s.Log.Debug("terabox: shorturlinfo failed, continuing with share/list", "err", err)
		}
	}

	var root []listItem
	if err := p.listDir(ctx, s, l, "", func(it listItem) error { root = append(root, it); return nil }); err != nil {
		return nil, err
	}

	res := &hosters.ResolveResult{Files: []hosters.FileInfo{}}
	add := func(fi hosters.FileInfo) {
		res.Files = append(res.Files, fi)
		if emit != nil {
			emit(fi)
		}
	}

	if l.FsID != "" {
		fi, err := p.findFile(ctx, s, l, root)
		if err != nil {
			return nil, err
		}
		add(fi)
		return res, nil
	}
	if len(root) == 1 && root[0].IsDir == 0 {
		add(p.fileInfo(l, root[0], "", ""))
		return res, nil
	}

	res.IsFolder = true
	visited := map[string]bool{}
	var walk func(items []listItem, dir, prefix string, depth int) error
	walk = func(items []listItem, dir, prefix string, depth int) error {
		for _, it := range items {
			name := safeComponent(it.Name, string(it.FsID))
			if it.IsDir == 0 {
				add(p.fileInfo(l, it, dir, prefix+name))
				continue
			}
			if depth >= maxDepth || it.Path == "" || visited[it.Path] {
				continue
			}
			visited[it.Path] = true
			var children []listItem
			if err := p.listDir(ctx, s, l, it.Path, func(c listItem) error { children = append(children, c); return nil }); err != nil {
				return err
			}
			if err := walk(children, it.Path, prefix+name+"/", depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if len(root) == 1 {
		// A share of one folder: the folder is the package.
		res.Name = safeComponent(root[0].Name, l.SURL)
		visited[root[0].Path] = true
		var children []listItem
		if err := p.listDir(ctx, s, l, root[0].Path, func(c listItem) error { children = append(children, c); return nil }); err != nil {
			return nil, err
		}
		err = walk(children, root[0].Path, "", 1)
	} else {
		res.Name = shareTitle(info.Title, l.SURL)
		err = walk(root, "", "", 0)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// findFile locates fsid anywhere in the share (breadth first).
func (p *Plugin) findFile(ctx context.Context, s *hosters.Session, l shareLink, root []listItem) (hosters.FileInfo, error) {
	type level struct {
		dir   string
		items []listItem
	}
	queue := []level{{"", root}}
	seen := map[string]bool{}
	for depth := 0; len(queue) > 0 && depth < 10000; depth++ {
		cur := queue[0]
		queue = queue[1:]
		for _, it := range cur.items {
			if it.IsDir == 0 && string(it.FsID) == l.FsID {
				return p.fileInfo(l, it, cur.dir, ""), nil
			}
		}
		for _, it := range cur.items {
			if it.IsDir != 0 && it.Path != "" && !seen[it.Path] {
				seen[it.Path] = true
				var children []listItem
				if err := p.listDir(ctx, s, l, it.Path, func(c listItem) error { children = append(children, c); return nil }); err != nil {
					return hosters.FileInfo{}, err
				}
				queue = append(queue, level{it.Path, children})
			}
		}
	}
	return hosters.FileInfo{}, hosters.NewNotFound(errors.New("terabox: file not found in share"))
}

func shareTitle(title, surl string) string {
	t := strings.Trim(path.Base("/"+strings.TrimSpace(title)), "/")
	if t == "" || t == "." {
		return "TeraBox " + surl
	}
	return safeComponent(t, "TeraBox "+surl)
}

func (p *Plugin) fileInfo(l shareLink, it listItem, dir, rel string) hosters.FileInfo {
	fsid := string(it.FsID)
	fi := hosters.FileInfo{
		ID:      fsid,
		Name:    safeComponent(it.Name, fsid),
		RelPath: rel,
		Size:    int64(it.Size),
		Link:    l.canonical() + "#fsid=" + fsid,
		Meta: map[string]string{
			metaSURL: l.SURL,
			metaFsID: fsid,
			metaDir:  dir,
			metaHost: l.Host,
		},
	}
	if isMD5(it.MD5) {
		fi.Meta[metaMD5] = strings.ToLower(it.MD5)
		if p.verifyMD5 {
			fi.Checksum = &hosters.Checksum{Algo: hosters.AlgoMD5, Value: strings.ToLower(it.MD5)}
		}
	}
	return fi
}

func isMD5(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16
}

// safeComponent keeps an untrusted name as one path component (fsutil does
// the full sanitizing downstream).
func safeComponent(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '_'
		}
		return r
	}, name)
	if strings.TrimSpace(name) == "" || name == "." || name == ".." {
		return fallback
	}
	return name
}

// Prepare implements hosters.Hoster: finds a fresh dlink for the file
// (listing its directory again if needed) and follows its redirects to the
// CDN URL, which is fetched with the session cookies.
func (p *Plugin) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	l := shareLink{Host: f.Meta[metaHost], SURL: f.Meta[metaSURL], FsID: f.Meta[metaFsID]}
	if l.FsID == "" {
		l.FsID = f.ID
	}
	if l.SURL == "" || l.Host == "" {
		u, err := url.Parse(f.Link)
		if err != nil || !p.Match(u) {
			return nil, hosters.NewFatal(errNotShare)
		}
		pl, _ := parseShareLink(u)
		l.Host, l.SURL = pl.Host, pl.SURL
		if l.FsID == "" {
			l.FsID = pl.FsID
		}
	}
	if l.FsID == "" {
		return nil, hosters.NewFatal(errors.New("terabox: file id missing (resolve the share again)"))
	}
	st := p.state(s)
	dlink := st.dlink(l.SURL, l.FsID)
	size := f.Size
	if dlink == "" {
		found := false
		err := p.listDir(ctx, s, l, f.Meta[metaDir], func(it listItem) error {
			if it.IsDir == 0 && string(it.FsID) == l.FsID {
				found, dlink = true, it.DLink
				if size <= 0 {
					size = int64(it.Size)
				}
				return errStop
			}
			return nil
		})
		if err != nil && !errors.Is(err, errStop) {
			return nil, err
		}
		if !found {
			// Unknown or changed parent directory: search the whole share.
			var root []listItem
			if err := p.listDir(ctx, s, l, "", func(it listItem) error { root = append(root, it); return nil }); err != nil {
				return nil, err
			}
			fi, err := p.findFile(ctx, s, l, root)
			if err != nil {
				return nil, err
			}
			if size <= 0 {
				size = fi.Size
			}
			dlink = st.dlink(l.SURL, l.FsID) // cached by listDir
		}
	}
	if dlink == "" {
		if s.Anonymous() {
			return nil, hosters.NewNeedsAuth(errors.New("terabox: no download link for anonymous users (add a TeraBox account with the ndus cookie)"))
		}
		return nil, hosters.NewNeedsAuth(errors.New("terabox: no download link (cookie expired?)"))
	}
	sc, err := p.shareContext(ctx, s, l, false)
	if err != nil {
		return nil, err
	}
	base, referer, _ := sc.get()
	final, err := p.followDLink(ctx, s, dlink, referer)
	if err != nil {
		st.dropDLink(l.SURL, l.FsID)
		return nil, err
	}
	if size == 0 {
		size = -1
	}
	spec := &hosters.DownloadSpec{
		URL:       final,
		Header:    http.Header{"User-Agent": {UserAgent}, "Referer": {base + "/"}},
		Size:      size,
		Resumable: true,
		MaxConns:  p.maxConns,
		Expires:   time.Now().Add(cdnTTL),
	}
	if p.verifyMD5 && isMD5(f.Meta[metaMD5]) {
		spec.Checksum = &hosters.Checksum{Algo: hosters.AlgoMD5, Value: f.Meta[metaMD5]}
	}
	return spec, nil
}

// followDLink follows the dlink redirect chain (dlink -> 302 -> CDN) with a
// 1-byte range request and returns the final URL.
func (p *Plugin) followDLink(ctx context.Context, s *hosters.Session, dlink, referer string) (string, error) {
	cur := dlink
	for hop := 0; hop < 8; hop++ {
		r, err := p.get(ctx, s, cur, referer, false, 4096, http.Header{"Range": {"bytes=0-0"}})
		if err != nil {
			return "", err
		}
		code := r.resp.StatusCode
		switch {
		case code >= 300 && code < 400:
			loc, err := r.resp.Location()
			if err != nil {
				return "", hosters.NewTemporary(0, errors.New("terabox: redirect without Location"))
			}
			cur = loc.String()
			continue
		case code == http.StatusOK || code == http.StatusPartialContent:
			if ae, ok := apiErrorFromBody(r.body); ok {
				return "", p.errnoError(ae)
			}
			if strings.Contains(r.resp.Header.Get("Content-Type"), "text/html") {
				return "", hosters.NewNeedsAuth(errors.New("terabox: download link returned a web page (login required?)"))
			}
			return cur, nil
		default:
			if he := p.Classify(r.resp, r.body, nil); he != nil {
				return "", he
			}
			return "", hosters.NewTemporary(0, fmt.Errorf("terabox: download link answered HTTP %d", code))
		}
	}
	return "", hosters.NewTemporary(0, errors.New("terabox: too many redirects on download link"))
}
