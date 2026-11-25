package gdrive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"admindescargas/internal/accounts"
	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

// valAPI caches the *apiClient of a session.
const valAPI = "gdrive:api"

// apiClient is the Drive API access of one session.
type apiClient struct {
	svc   *drive.Service
	http  *http.Client // authenticated, routed through the session egress
	oauth bool         // user token (copy workaround possible) vs API key
}

// baseTransport is the session's egress-bound transport.
func baseTransport(s *hosters.Session) (http.RoundTripper, error) {
	if s.Client != nil && s.Client.Transport != nil {
		return s.Client.Transport, nil
	}
	if s.Egress != nil {
		return netx.NewTransport(s.Egress), nil
	}
	return nil, hosters.NewFatal(errors.New("gdrive: session has no egress-bound client"))
}

// api returns the session's Drive API client, or nil when the session has
// no credentials (anonymous mode).
func (h *Hoster) api(ctx context.Context, s *hosters.Session) (*apiClient, error) {
	if v, ok := s.Value(valAPI); ok {
		if ac, _ := v.(*apiClient); ac != nil {
			return ac, nil
		}
	}
	base, err := baseTransport(s)
	if err != nil {
		return nil, err
	}
	var hc *http.Client
	oauth := false
	switch {
	case s.Account != nil && s.Account.Kind == accounts.KindOAuth:
		hc, err = h.oauthClient(ctx, s, base)
		if err != nil {
			return nil, err
		}
		oauth = true
	case s.Account != nil && s.Account.Kind == accounts.KindAPIKey:
		sec, err := s.Secret(ctx)
		if err != nil || sec.APIKey == "" {
			return nil, hosters.NewNeedsAuth(errors.New("gdrive: API key account has no key"))
		}
		hc = &http.Client{Transport: &apiKeyTransport{key: sec.APIKey, base: base}}
	case s.Account != nil:
		return nil, hosters.NewNeedsAuth(fmt.Errorf("gdrive: unsupported account kind %q (use oauth or apikey)", s.Account.Kind))
	case strings.TrimSpace(h.cfg.Options["api_key"]) != "":
		hc = &http.Client{Transport: &apiKeyTransport{key: strings.TrimSpace(h.cfg.Options["api_key"]), base: base}}
	default:
		return nil, nil
	}
	svc, err := drive.NewService(ctx, option.WithHTTPClient(hc), option.WithEndpoint(h.apiBase+"/drive/v3/"))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("gdrive: build API client: %w", err))
	}
	ac := &apiClient{svc: svc, http: hc, oauth: oauth}
	s.SetValue(valAPI, ac)
	return ac, nil
}

// oauthClient builds an http.Client that authenticates with the account's
// token, refreshes it through the session egress and persists refreshed
// tokens with Session.SetSecret.
func (h *Hoster) oauthClient(ctx context.Context, s *hosters.Session, base http.RoundTripper) (*http.Client, error) {
	sec, err := s.Secret(ctx)
	if err != nil {
		return nil, hosters.NewNeedsAuth(fmt.Errorf("gdrive: load account secret: %w", err))
	}
	tok := sec.OAuthToken
	if tok == nil || (tok.AccessToken == "" && tok.RefreshToken == "") {
		return nil, hosters.NewNeedsAuth(errors.New("gdrive: account has no OAuth token (connect the Google account again)"))
	}
	clientID := strings.TrimSpace(h.cfg.Options["client_id"])
	clientSecret := strings.TrimSpace(h.cfg.Options["client_secret"])
	if clientID == "" {
		clientID, clientSecret = sec.Extra["client_id"], sec.Extra["client_secret"]
	}
	// The token source outlives this call (it refreshes later), so it must
	// not inherit the request's cancellation; it still carries its values.
	bg := context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, &http.Client{Transport: base})
	var src oauth2.TokenSource
	if clientID != "" && tok.RefreshToken != "" {
		conf := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: h.oauthEndpoint, Scopes: []string{driveScope}}
		src = conf.TokenSource(bg, tok)
	} else {
		if !tok.Valid() {
			return nil, hosters.NewNeedsAuth(errors.New("gdrive: OAuth token expired and cannot be refreshed (set client_id/client_secret in [hosters.gdrive.options] or reconnect the account)"))
		}
		src = oauth2.StaticTokenSource(tok)
	}
	ps := &persistingSource{src: src, last: tok.AccessToken, save: func(t *oauth2.Token) {
		cur, err := s.Secret(bg)
		if err != nil {
			logOf(s).Warn("gdrive: cannot reload secret to persist refreshed OAuth token", "err", err)
			return
		}
		cur.OAuthToken = t
		if err := s.SetSecret(bg, cur); err != nil {
			logOf(s).Warn("gdrive: cannot persist refreshed OAuth token", "err", err)
			return
		}
		logOf(s).Debug("gdrive: refreshed OAuth token persisted", "expiry", t.Expiry)
	}}
	return &http.Client{Transport: &oauth2.Transport{Source: ps, Base: base}}, nil
}

// persistingSource calls save whenever the wrapped source returns a new
// access token (i.e. after a refresh).
type persistingSource struct {
	src  oauth2.TokenSource
	save func(*oauth2.Token)

	mu   sync.Mutex
	last string
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	t, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if t.AccessToken != p.last {
		p.last = t.AccessToken
		p.save(t)
	}
	return t, nil
}

// apiKeyTransport adds the API key header to every request.
type apiKeyTransport struct {
	key  string
	base http.RoundTripper
}

func (t *apiKeyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set(headerAPIKey, t.key)
	return t.base.RoundTrip(r2)
}

// apiError converts a Drive API (library) error into a typed error.
func apiError(what string, err error) error {
	if err == nil {
		return nil
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		reasons := make([]string, 0, len(ge.Errors))
		for _, e := range ge.Errors {
			reasons = append(reasons, e.Reason)
		}
		return reasonError(ge.Code, reasons, fmt.Sprintf("gdrive %s: %s", what, ge.Message), ge.Header)
	}
	return classifyTransport(fmt.Errorf("gdrive %s: %w", what, err))
}

func rkHeader(id, rk string) string { return id + "/" + rk }

func (h *Hoster) getFile(ctx context.Context, api *apiClient, id, rk string) (*drive.File, error) {
	if err := h.limiter.Wait(ctx); err != nil {
		return nil, hosters.NewFatal(err)
	}
	call := api.svc.Files.Get(id).SupportsAllDrives(true).Fields(fileFields).Context(ctx)
	if rk != "" {
		call.Header().Set(headerResourceKeys, rkHeader(id, rk))
	}
	f, err := call.Do()
	if err != nil {
		return nil, apiError("files.get", err)
	}
	if f.Trashed {
		return nil, hosters.NewNotFound(errors.New("gdrive: the file is in the owner's trash"))
	}
	return f, nil
}

// followShortcut returns the shortcut's target (or f itself).
func (h *Hoster) followShortcut(ctx context.Context, api *apiClient, f *drive.File) (*drive.File, error) {
	for i := 0; f.MimeType == mimeShortcut; i++ {
		if f.ShortcutDetails == nil || f.ShortcutDetails.TargetId == "" || i > 4 {
			return nil, hosters.NewNotFound(errors.New("gdrive: broken shortcut"))
		}
		t, err := h.getFile(ctx, api, f.ShortcutDetails.TargetId, f.ShortcutDetails.TargetResourceKey)
		if err != nil {
			return nil, err
		}
		f = t
	}
	return f, nil
}

func (h *Hoster) resolveAPI(ctx context.Context, s *hosters.Session, api *apiClient, t target, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	f, err := h.getFile(ctx, api, t.id, t.resourceKey)
	if err != nil {
		return nil, err
	}
	if f, err = h.followShortcut(ctx, api, f); err != nil {
		return nil, err
	}
	if f.MimeType == mimeFolder {
		return h.listAPI(ctx, s, api, f, emit)
	}
	fi, ok := h.apiFileInfo(f, "", false)
	if !ok {
		return nil, hosters.NewFatal(fmt.Errorf("gdrive: %q is a %s and cannot be downloaded", f.Name, f.MimeType))
	}
	emit(fi)
	return &hosters.ResolveResult{Files: []hosters.FileInfo{fi}}, nil
}

// apiFileInfo converts an API file; ok is false for non-downloadable Google
// types (forms, sites, maps...).
func (h *Hoster) apiFileInfo(f *drive.File, prefix string, inFolder bool) (hosters.FileInfo, bool) {
	t := target{kind: tFile, id: f.Id, resourceKey: f.ResourceKey}
	fi := hosters.FileInfo{ID: f.Id, Name: f.Name, Size: f.Size}
	if fi.Name == "" {
		fi.Name = f.Id
	}
	if strings.HasPrefix(f.MimeType, mimeAppsPfx) {
		dt := docTypeOfMime(f.MimeType)
		if dt == "" {
			return fi, false
		}
		t.kind, t.docType = tDoc, dt
		format := h.exports[dt]
		fi.Name = exportName(fi.Name, format)
		fi.Size = -1
		fi.Meta = map[string]string{metaDocType: dt, metaExport: format}
	} else if f.Md5Checksum != "" {
		fi.Checksum = &hosters.Checksum{Algo: hosters.AlgoMD5, Value: strings.ToLower(f.Md5Checksum)}
	}
	if f.ResourceKey != "" {
		if fi.Meta == nil {
			fi.Meta = map[string]string{}
		}
		fi.Meta[metaResourceKey] = f.ResourceKey
	}
	fi.Link = fileLink(t)
	if inFolder {
		fi.RelPath = path.Join(prefix, fsutil.SanitizeName(fi.Name))
	}
	return fi, true
}

func escapeQ(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

// listAPI lists a folder tree with files.list (paginated, recursive,
// shared drives included, shortcuts followed).
func (h *Hoster) listAPI(ctx context.Context, s *hosters.Session, api *apiClient, root *drive.File, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	res := &hosters.ResolveResult{Name: root.Name, IsFolder: true}
	if res.Name == "" {
		res.Name = root.Id
	}
	driveID := root.DriveId
	visited := map[string]bool{}
	var walk func(id, rk, prefix string, depth int) error
	walk = func(id, rk, prefix string, depth int) error {
		if visited[id] {
			return nil
		}
		visited[id] = true
		if depth > maxFolderDepth {
			return hosters.NewFatal(fmt.Errorf("gdrive: folder tree deeper than %d levels", maxFolderDepth))
		}
		type sub struct{ id, rk, name string }
		var subs []sub
		pageToken := ""
		for {
			if err := h.limiter.Wait(ctx); err != nil {
				return hosters.NewFatal(err)
			}
			call := api.svc.Files.List().
				Q(fmt.Sprintf("'%s' in parents and trashed = false", escapeQ(id))).
				Fields(listFields).PageSize(1000).OrderBy("folder,name").
				SupportsAllDrives(true).IncludeItemsFromAllDrives(true).Context(ctx)
			if driveID != "" {
				call = call.Corpora("drive").DriveId(driveID)
			}
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			if rk != "" {
				call.Header().Set(headerResourceKeys, rkHeader(id, rk))
			}
			page, err := call.Do()
			if err != nil {
				return apiError("files.list", err)
			}
			for _, f := range page.Files {
				name := fsutil.SanitizeName(f.Name)
				if f.MimeType == mimeShortcut {
					sd := f.ShortcutDetails
					if sd == nil || sd.TargetId == "" {
						continue
					}
					if sd.TargetMimeType == mimeFolder {
						subs = append(subs, sub{sd.TargetId, sd.TargetResourceKey, name})
						continue
					}
					tf, err := h.getFile(ctx, api, sd.TargetId, sd.TargetResourceKey)
					if err != nil {
						if hosters.IsNotFound(err) || hosters.IsNeedsAuth(err) {
							logOf(s).Warn("gdrive: skipping broken shortcut", "shortcut", f.Id)
							continue
						}
						return err
					}
					tf.Name = f.Name // keep the name shown in the folder
					f = tf
				}
				if f.MimeType == mimeFolder {
					subs = append(subs, sub{f.Id, f.ResourceKey, name})
					continue
				}
				fi, ok := h.apiFileInfo(f, prefix, true)
				if !ok {
					logOf(s).Info("gdrive: skipping non-downloadable item", "id", f.Id, "mime", f.MimeType)
					continue
				}
				res.Files = append(res.Files, fi)
				emit(fi)
			}
			pageToken = page.NextPageToken
			if pageToken == "" {
				break
			}
		}
		for _, sb := range subs {
			if err := walk(sb.id, sb.rk, path.Join(prefix, sb.name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root.Id, root.ResourceKey, "", 0); err != nil {
		return nil, err
	}
	return res, nil
}

// mediaURL is the alt=media URL of a file.
func (h *Hoster) mediaURL(id string, ack bool) string {
	v := url.Values{"alt": {"media"}, "supportsAllDrives": {"true"}}
	if ack {
		v.Set("acknowledgeAbuse", "true")
	}
	return h.apiBase + "/drive/v3/files/" + url.PathEscape(id) + "?" + v.Encode()
}

// probe performs a 1-byte ranged GET and returns nil if the download would
// work, or the typed error otherwise. The returned header is that of the
// successful response.
func (h *Hoster) probe(ctx context.Context, c *http.Client, rawURL string, hdr http.Header) (http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, hosters.NewFatal(err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		if err := h.Classify(resp, body, nil); err != nil {
			return nil, resp.StatusCode, err
		}
		return resp.Header, resp.StatusCode, nil
	}
	if err := h.Classify(resp, body, nil); err != nil {
		return nil, resp.StatusCode, err
	}
	return nil, resp.StatusCode, hosters.NewTemporary(0, fmt.Errorf("gdrive: unexpected HTTP %d", resp.StatusCode))
}

func (h *Hoster) prepareAPI(ctx context.Context, s *hosters.Session, api *apiClient, t target, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	meta, err := h.getFile(ctx, api, t.id, t.resourceKey)
	if err != nil {
		return nil, err
	}
	if meta, err = h.followShortcut(ctx, api, meta); err != nil {
		return nil, err
	}
	if meta.MimeType == mimeFolder {
		return nil, hosters.NewFatal(errors.New("gdrive: link points to a folder, not a file"))
	}
	if strings.HasPrefix(meta.MimeType, mimeAppsPfx) {
		return h.prepareAPIExport(s, api, meta, f)
	}
	hdr := http.Header{}
	if meta.ResourceKey != "" {
		hdr.Set(headerResourceKeys, rkHeader(meta.Id, meta.ResourceKey))
	}
	spec := &hosters.DownloadSpec{
		Client:    api.http,
		Header:    hdr,
		Size:      meta.Size,
		Resumable: true,
	}
	if meta.Md5Checksum != "" {
		spec.Checksum = &hosters.Checksum{Algo: hosters.AlgoMD5, Value: strings.ToLower(meta.Md5Checksum)}
	}
	if f.Name == "" {
		spec.Filename = meta.Name
	}
	spec.URL = h.mediaURL(meta.Id, false)
	_, _, err = h.probe(ctx, api.http, spec.URL, hdr)
	if err != nil && isAbuse(err) {
		spec.URL = h.mediaURL(meta.Id, true)
		_, _, err = h.probe(ctx, api.http, spec.URL, hdr)
		if err != nil && isAbuse(err) {
			return nil, hosters.NewFatal(fmt.Errorf("%w; it refuses to serve it even with acknowledgeAbuse", errAbusive))
		}
	}
	if err == nil {
		return spec, nil
	}
	if hosters.IsQuota(err) && quotaScope(err) == hosters.ScopeFile {
		if api.oauth && h.copyOnQuota {
			return h.prepareCopy(ctx, s, api, meta, spec)
		}
		return nil, fileQuotaError(api.oauth)
	}
	return nil, err
}

func (h *Hoster) prepareAPIExport(s *hosters.Session, api *apiClient, meta *drive.File, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	dt := docTypeOfMime(meta.MimeType)
	if dt == "" {
		return nil, hosters.NewFatal(fmt.Errorf("gdrive: %s files cannot be downloaded", meta.MimeType))
	}
	format := h.exports[dt]
	if v := f.Meta[metaExport]; v != "" {
		format = v
	}
	mime := exportMime[format]
	spec := &hosters.DownloadSpec{Size: -1, Resumable: false, MaxConns: 1}
	if f.Name == "" {
		spec.Filename = exportName(meta.Name, format)
	}
	if api.oauth {
		spec.Client = api.http
		if l := meta.ExportLinks[mime]; l != "" {
			spec.URL = l
		} else {
			spec.URL = h.apiBase + "/drive/v3/files/" + url.PathEscape(meta.Id) + "/export?" + url.Values{"mimeType": {mime}}.Encode()
		}
		return spec, nil
	}
	// API key: exportLinks need a user; public docs export anonymously.
	spec.URL = h.anonExportURL(dt, meta.Id, format)
	logOf(s).Debug("gdrive: exporting public document anonymously", "id", meta.Id, "format", format)
	return spec, nil
}

func isAbuse(err error) bool {
	he, ok := hosters.AsError(err)
	return ok && errors.Is(he.Err, errAbusive)
}

func quotaScope(err error) hosters.QuotaScope {
	if he, ok := hosters.AsError(err); ok {
		return he.Scope
	}
	return hosters.ScopeNone
}

// errAbusive marks "cannotDownloadAbusiveFile" (retry with
// acknowledgeAbuse=true).
var errAbusive = errors.New("gdrive: file flagged as abusive (malware/spam) by Google")
