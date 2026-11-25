package gdrive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
)

// anonFile is what an anonymous probe learned about a file.
type anonFile struct {
	url       string // download URL that served the file (form already followed)
	name      string // from Content-Disposition
	size      int64  // -1 if unknown
	resumable bool
}

func (h *Hoster) anonDownloadURL(t target) string {
	v := url.Values{"id": {t.id}, "export": {"download"}, "confirm": {"t"}}
	if t.resourceKey != "" {
		v.Set("resourcekey", t.resourceKey)
	}
	return h.ucBase + "/download?" + v.Encode()
}

func (h *Hoster) anonExportURL(docType, id, format string) string {
	if docType == "drawings" {
		return h.docsBase + "/drawings/d/" + url.PathEscape(id) + "/export/" + url.PathEscape(format)
	}
	return h.docsBase + "/" + docType + "/d/" + url.PathEscape(id) + "/export?" + url.Values{"format": {format}}.Encode()
}

func (h *Hoster) get(ctx context.Context, s *hosters.Session, rawURL string, ranged bool) (*http.Response, error) {
	if err := h.limiter.Wait(ctx); err != nil {
		return nil, hosters.NewFatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("gdrive: build request: %w", err))
	}
	req.Header.Set("User-Agent", userAgent)
	if ranged {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, h.Classify(nil, nil, err)
	}
	return resp, nil
}

// anonProbe requests the anonymous download (1 byte), following the
// virus-scan confirmation form, and reports what it found.
func (h *Hoster) anonProbe(ctx context.Context, s *hosters.Session, t target) (*anonFile, error) {
	cur := h.anonDownloadURL(t)
	for hop := 0; hop < maxFormHops; hop++ {
		resp, err := h.get(ctx, s, cur, true)
		if err != nil {
			return nil, err
		}
		if resp.Request != nil && strings.EqualFold(resp.Request.URL.Hostname(), loginHost) {
			resp.Body.Close()
			return nil, hosters.NewNeedsAuth(errors.New("gdrive: the file is private: connect a Google account with access"))
		}
		if (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) &&
			(!isHTML(resp.Header) || isAttachment(resp.Header)) {
			resp.Body.Close()
			return &anonFile{
				url:       cur,
				name:      dispositionName(resp.Header),
				size:      totalSize(resp),
				resumable: resp.StatusCode == http.StatusPartialContent || strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes"),
			}, nil
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
		resp.Body.Close()
		if rerr != nil {
			return nil, hosters.NewTemporary(0, fmt.Errorf("gdrive: read page: %w", rerr))
		}
		if isHTML(resp.Header) {
			if next, ok := downloadForm(body, resp.Request.URL); ok {
				cur = next
				continue
			}
		}
		if err := h.Classify(resp, head(body), nil); err != nil && !hosters.IsExpired(err) {
			return nil, err
		}
		return nil, hosters.NewTemporary(0, errors.New("gdrive: unexpected page instead of the file (layout change?)"))
	}
	return nil, hosters.NewTemporary(0, errors.New("gdrive: virus-scan confirmation loop"))
}

// hasDownloadForm reports whether body holds the virus-scan form.
func hasDownloadForm(body []byte) bool {
	_, ok := downloadForm(body, nil)
	return ok
}

// downloadForm parses the virus-scan warning: the form#download-form (or
// any form posting to a download endpoint) with its hidden inputs (id,
// export, confirm, uuid, ...), or the legacy a#uc-download-link.
func downloadForm(body []byte, base *url.URL) (string, bool) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	resolve := func(raw string) (*url.URL, bool) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, false
		}
		if base != nil {
			u = base.ResolveReference(u)
		}
		return u, true
	}
	var out string
	doc.Find("form").EachWithBreak(func(_ int, f *goquery.Selection) bool {
		action, _ := f.Attr("action")
		id, _ := f.Attr("id")
		if id != "download-form" && !strings.Contains(action, "/download") && !strings.Contains(action, "/uc") {
			return true
		}
		u, ok := resolve(action)
		if !ok || action == "" {
			return true
		}
		q := u.Query()
		f.Find("input").Each(func(_ int, in *goquery.Selection) {
			name, _ := in.Attr("name")
			typ, _ := in.Attr("type")
			if name == "" || (typ != "" && !strings.EqualFold(typ, "hidden")) {
				return
			}
			val, _ := in.Attr("value")
			q.Set(name, val)
		})
		if q.Get("id") == "" {
			return true
		}
		u.RawQuery = q.Encode()
		out = u.String()
		return false
	})
	if out != "" {
		return out, true
	}
	if href, ok := doc.Find("a#uc-download-link").Attr("href"); ok {
		if u, ok := resolve(href); ok && u.Query().Get("confirm") != "" {
			return u.String(), true
		}
	}
	return "", false
}

func dispositionName(h http.Header) string {
	_, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["filename"])
}

// totalSize reads the size from Content-Range (206) or Content-Length (200).
func totalSize(resp *http.Response) int64 {
	if resp.StatusCode == http.StatusPartialContent {
		if m := reSizeRange.FindStringSubmatch(resp.Header.Get("Content-Range")); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				return n
			}
		}
		return -1
	}
	if resp.ContentLength >= 0 {
		return resp.ContentLength
	}
	return -1
}

func head(b []byte) []byte {
	if len(b) > 16<<10 {
		return b[:16<<10]
	}
	return b
}

// viewPageName reads the file name from drive.google.com/file/d/<id>/view
// (og:title), used when the download itself is blocked by the quota.
func (h *Hoster) viewPageName(ctx context.Context, s *hosters.Session, t target) string {
	u := h.driveBase + "/file/d/" + url.PathEscape(t.id) + "/view"
	if t.resourceKey != "" {
		u += "?resourcekey=" + url.QueryEscape(t.resourceKey)
	}
	resp, err := h.get(ctx, s, u, false)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return ""
	}
	if v, ok := doc.Find(`meta[property="og:title"]`).Attr("content"); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	title := strings.TrimSpace(doc.Find("title").First().Text())
	title = strings.TrimSpace(strings.TrimSuffix(title, "- Google Drive"))
	return title
}

func (h *Hoster) resolveAnon(ctx context.Context, s *hosters.Session, t target, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	switch t.kind {
	case tUnknown:
		nt, err := h.resolveOpen(ctx, s, t)
		if err != nil {
			return nil, err
		}
		return h.resolveAnon(ctx, s, nt, emit)
	case tFolder:
		return h.listEmbedded(ctx, s, t, emit)
	case tDoc:
		fi, err := h.anonDocInfo(ctx, s, t)
		if err != nil {
			return nil, err
		}
		emit(fi)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{fi}}, nil
	default:
		fi := hosters.FileInfo{ID: t.id, Link: fileLink(t), Size: -1}
		if t.resourceKey != "" {
			fi.Meta = map[string]string{metaResourceKey: t.resourceKey}
		}
		af, err := h.anonProbe(ctx, s, t)
		switch {
		case err == nil:
			fi.Name, fi.Size = af.name, af.size
		case hosters.IsQuota(err) && quotaScope(err) == hosters.ScopeFile:
			// Still list the file; Prepare will report the quota.
			fi.Name = h.viewPageName(ctx, s, t)
			logOf(s).Info("gdrive: file over its download quota, listed without size", "id", t.id)
		default:
			return nil, err
		}
		if fi.Name == "" {
			fi.Name = t.id
		}
		emit(fi)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{fi}}, nil
	}
}

// resolveOpen follows drive.google.com/open?id= to learn whether the id is
// a file, a folder or a document.
func (h *Hoster) resolveOpen(ctx context.Context, s *hosters.Session, t target) (target, error) {
	v := url.Values{"id": {t.id}}
	if t.resourceKey != "" {
		v.Set("resourcekey", t.resourceKey)
	}
	resp, err := h.get(ctx, s, h.driveBase+"/open?"+v.Encode(), false)
	if err != nil {
		return target{}, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusNotFound {
		return target{}, hosters.NewNotFound(errors.New("gdrive: the file does not exist"))
	}
	final := *resp.Request.URL
	if strings.EqualFold(final.Hostname(), loginHost) {
		return target{}, hosters.NewNeedsAuth(errors.New("gdrive: the item is private: connect a Google account with access"))
	}
	// Map the test/base host back to the public one before parsing.
	switch {
	case strings.HasPrefix(final.String(), h.docsBase+"/"):
		final.Host, final.Scheme = "docs.google.com", "https"
	case strings.HasPrefix(final.String(), h.driveBase+"/"):
		final.Host, final.Scheme = "drive.google.com", "https"
	}
	nt := parseLink(&final)
	if nt.kind == tNone || nt.kind == tUnknown || nt.id != t.id {
		nt = target{kind: tFile, id: t.id}
	}
	if nt.resourceKey == "" {
		nt.resourceKey = t.resourceKey
	}
	return nt, nil
}

// anonDocInfo learns the exported file name from the export response
// headers (the body is not read).
func (h *Hoster) anonDocInfo(ctx context.Context, s *hosters.Session, t target) (hosters.FileInfo, error) {
	format := h.exports[t.docType]
	fi := hosters.FileInfo{
		ID: t.id, Size: -1, Link: fileLink(t),
		Meta: map[string]string{metaDocType: t.docType, metaExport: format},
	}
	resp, err := h.get(ctx, s, h.anonExportURL(t.docType, t.id, format), false)
	if err != nil {
		return fi, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 || (isHTML(resp.Header) && format != "zip") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err := h.Classify(resp, body, nil); err != nil {
			return fi, err
		}
		return fi, hosters.NewNeedsAuth(errors.New("gdrive: the document is not public"))
	}
	fi.Name = dispositionName(resp.Header)
	if fi.Name == "" {
		fi.Name = t.id
	}
	fi.Name = exportName(fi.Name, format)
	if resp.ContentLength > 0 {
		fi.Size = resp.ContentLength
	}
	return fi, nil
}

func (h *Hoster) prepareAnonExport(t target, f hosters.FileInfo) *hosters.DownloadSpec {
	format := h.exports[t.docType]
	if v := f.Meta[metaExport]; v != "" {
		format = v
	}
	spec := &hosters.DownloadSpec{
		URL:       h.anonExportURL(t.docType, t.id, format),
		Header:    http.Header{"User-Agent": {userAgent}},
		Size:      -1,
		Resumable: false,
		MaxConns:  1,
	}
	if f.Name == "" {
		spec.Filename = t.id + "." + format
	}
	return spec
}

func (h *Hoster) prepareAnonFile(ctx context.Context, s *hosters.Session, t target, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	af, err := h.anonProbe(ctx, s, t)
	if err != nil {
		return nil, err
	}
	spec := &hosters.DownloadSpec{
		URL:       af.url,
		Header:    http.Header{"User-Agent": {userAgent}},
		Size:      af.size,
		Resumable: af.resumable,
		Checksum:  f.Checksum,
	}
	if spec.Size < 0 {
		spec.Size = f.Size
	}
	if f.Name == "" && af.name != "" {
		spec.Filename = af.name
	}
	if !af.resumable {
		spec.MaxConns = 1
	}
	return spec, nil
}

// listEmbedded lists a public folder without credentials through
// drive.google.com/embeddedfolderview (best effort).
func (h *Hoster) listEmbedded(ctx context.Context, s *hosters.Session, root target, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	res := &hosters.ResolveResult{IsFolder: true}
	visited := map[string]bool{}
	var walk func(t target, prefix string, depth int) error
	walk = func(t target, prefix string, depth int) error {
		if visited[t.id] {
			return nil
		}
		visited[t.id] = true
		if depth > maxFolderDepth {
			return hosters.NewFatal(fmt.Errorf("gdrive: folder tree deeper than %d levels", maxFolderDepth))
		}
		title, entries, err := h.embeddedPage(ctx, s, t)
		if err != nil {
			return err
		}
		if depth == 0 {
			res.Name = title
		}
		var subs []embeddedEntry
		for _, e := range entries {
			switch e.t.kind {
			case tFolder:
				subs = append(subs, e)
			case tFile, tDoc:
				name := e.title
				meta := map[string]string{metaListing: "embedded"}
				if e.t.kind == tDoc {
					format := h.exports[e.t.docType]
					name = exportName(name, format)
					meta[metaDocType], meta[metaExport] = e.t.docType, format
				}
				if e.t.resourceKey != "" {
					meta[metaResourceKey] = e.t.resourceKey
				}
				fi := hosters.FileInfo{
					ID: e.t.id, Name: name, Size: -1, Link: fileLink(e.t), Meta: meta,
					RelPath: path.Join(prefix, fsutil.SanitizeName(name)),
				}
				res.Files = append(res.Files, fi)
				emit(fi)
			}
		}
		for _, sub := range subs {
			if err := walk(sub.t, path.Join(prefix, fsutil.SanitizeName(sub.title)), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, "", 0); err != nil {
		return nil, err
	}
	if res.Name == "" {
		res.Name = root.id
	}
	logOf(s).Warn("gdrive: folder listed without credentials (embedded view): sizes are unknown and very large folders may be incomplete; add a Google account or an api_key for a complete listing",
		"folder", root.id, "files", len(res.Files))
	return res, nil
}

type embeddedEntry struct {
	t     target
	title string
}

func (h *Hoster) embeddedPage(ctx context.Context, s *hosters.Session, t target) (string, []embeddedEntry, error) {
	v := url.Values{"id": {t.id}}
	if t.resourceKey != "" {
		v.Set("resourcekey", t.resourceKey)
	}
	resp, err := h.get(ctx, s, h.driveBase+"/embeddedfolderview?"+v.Encode(), false)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", nil, hosters.NewTemporary(0, fmt.Errorf("gdrive: read folder view: %w", err))
	}
	if resp.Request != nil && strings.EqualFold(resp.Request.URL.Hostname(), loginHost) {
		return "", nil, hosters.NewNeedsAuth(errors.New("gdrive: the folder is private: connect a Google account with access"))
	}
	if resp.StatusCode >= 400 {
		if err := h.Classify(resp, head(body), nil); err != nil {
			return "", nil, err
		}
	}
	title, entries, err := parseEmbedded(body)
	if err != nil {
		return "", nil, err
	}
	return title, entries, nil
}

// parseEmbedded parses the embedded folder view HTML.
func parseEmbedded(body []byte) (string, []embeddedEntry, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", nil, hosters.NewTemporary(0, fmt.Errorf("gdrive: parse folder view: %w", err))
	}
	title := strings.TrimSpace(doc.Find("div.folder-title").First().Text())
	if title == "" {
		title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(doc.Find("title").First().Text()), "- Google Drive"))
	}
	var out []embeddedEntry
	doc.Find("div.flip-entry").Each(func(_ int, e *goquery.Selection) {
		href, _ := e.Find("a").First().Attr("href")
		u, err := url.Parse(strings.TrimSpace(href))
		if err != nil {
			return
		}
		t := parseLink(u)
		if t.kind == tUnknown {
			t.kind = tFile
		}
		if t.kind == tNone {
			id, _ := e.Attr("id")
			id = strings.TrimPrefix(id, "entry-")
			if !reID.MatchString(id) {
				return
			}
			t = target{kind: tFile, id: id}
		}
		name := strings.TrimSpace(e.Find("div.flip-entry-title").First().Text())
		if name == "" {
			name = t.id
		}
		out = append(out, embeddedEntry{t: t, title: name})
	})
	if len(out) == 0 && doc.Find("div.flip-entries, div.flip-entry-list, #folder-view").Length() == 0 {
		if err := pageError(body); err != nil {
			return "", nil, err
		}
		return "", nil, hosters.NewTemporary(0, errors.New("gdrive: folder view without entries list (layout change, or private folder?)"))
	}
	return title, out, nil
}
