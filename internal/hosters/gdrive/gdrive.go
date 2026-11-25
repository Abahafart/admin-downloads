package gdrive

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin identifier.
const Name = "gdrive"

func init() { hosters.RegisterFactory(Name, New) }

// Hoster is the Google Drive plugin. It is safe for concurrent use.
type Hoster struct {
	cfg     config.HosterConfig
	limiter *rate.Limiter

	copyOnQuota bool
	deleteCopy  bool
	exports     map[string]string // doc type -> format

	// Base URLs (overridden by tests).
	apiBase       string
	ucBase        string
	driveBase     string
	docsBase      string
	oauthEndpoint oauth2.Endpoint
}

var (
	_ hosters.Hoster            = (*Hoster)(nil)
	_ hosters.StreamingResolver = (*Hoster)(nil)
	_ hosters.AccountChecker    = (*Hoster)(nil)
	_ hosters.SelfTester        = (*Hoster)(nil)
	_ Cleaner                   = (*Hoster)(nil)
	_ hosters.FileQuotaBypasser = (*Hoster)(nil)
)

// Cleaner is implemented by the plugin: the engine calls Cleanup when a
// download is finished for good so the temporary copy made by the quota
// workaround (if any) is deleted. See the package documentation.
// It is the generic hosters.Cleaner interface.
type Cleaner = hosters.Cleaner

// New builds the plugin from its [hosters.gdrive] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = defaultRPS
	}
	h := &Hoster{
		cfg:           cfg,
		limiter:       rate.NewLimiter(rate.Limit(rps), 4),
		copyOnQuota:   true,
		deleteCopy:    true,
		exports:       map[string]string{},
		apiBase:       defaultAPIBase,
		ucBase:        defaultUCBase,
		driveBase:     defaultDriveBase,
		docsBase:      defaultDocsBase,
		oauthEndpoint: defaultOAuthEndpoint,
	}
	var err error
	if h.copyOnQuota, err = boolOpt(cfg.Options, "copy_on_quota", true); err != nil {
		return nil, err
	}
	if h.deleteCopy, err = boolOpt(cfg.Options, "delete_copy", true); err != nil {
		return nil, err
	}
	optKey := map[string]string{
		"document": "export_document", "spreadsheets": "export_spreadsheet",
		"presentation": "export_presentation", "drawings": "export_drawing",
	}
	global := strings.ToLower(strings.TrimSpace(cfg.Options["export_format"]))
	for typ, def := range defaultExport {
		f := def
		if global != "" && slices.Contains(exportFormats[typ], global) {
			f = global
		}
		if v := strings.ToLower(strings.TrimSpace(cfg.Options[optKey[typ]])); v != "" {
			if !slices.Contains(exportFormats[typ], v) {
				return nil, fmt.Errorf("gdrive: option %s: unsupported format %q (valid: %s)", optKey[typ], v, strings.Join(exportFormats[typ], ", "))
			}
			f = v
		}
		h.exports[typ] = f
	}
	if global != "" && !slices.ContainsFunc([]string{"document", "spreadsheets", "presentation", "drawings"}, func(t string) bool {
		return slices.Contains(exportFormats[t], global)
	}) {
		return nil, fmt.Errorf("gdrive: option export_format: unsupported format %q", global)
	}
	return h, nil
}

func boolOpt(opts map[string]string, key string, def bool) (bool, error) {
	v := strings.TrimSpace(opts[key])
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("gdrive: option %s: %q is not a boolean", key, v)
	}
	return b, nil
}

// Name implements hosters.Hoster.
func (h *Hoster) Name() string { return Name }

// Caps implements hosters.Hoster.
func (h *Hoster) Caps() hosters.Caps {
	c := hosters.Caps{
		MaxConnsPerFile:   4,
		Resumable:         true,
		Folders:           true,
		AccountKinds:      []accounts.Kind{accounts.KindOAuth, accounts.KindAPIKey},
		DefaultRetryAfter: defaultRetryAfter,
		QuotaScope:        hosters.ScopeFile,
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

// CanBypassFileQuota reports whether Prepare can work around a per-file
// quota for this session (OAuth account and copy_on_quota enabled).
func (h *Hoster) CanBypassFileQuota(s *hosters.Session) bool {
	return h.copyOnQuota && s != nil && s.Account != nil && s.Account.Kind == accounts.KindOAuth
}

// targetKind is what a Drive link points to.
type targetKind int

const (
	tNone    targetKind = iota
	tFile               // binary file
	tFolder             // folder
	tDoc                // Google Docs editor file (exported)
	tUnknown            // open?id=: file or folder
)

type target struct {
	kind        targetKind
	id          string
	docType     string // tDoc: document | spreadsheets | presentation | drawings
	resourceKey string
}

// Match implements hosters.Hoster (no I/O).
func (h *Hoster) Match(u *url.URL) bool { return parseLink(u).kind != tNone }

func parseLink(u *url.URL) target {
	if u == nil {
		return target{}
	}
	host := strings.ToLower(u.Hostname())
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	q := u.Query()
	rk := q.Get("resourcekey")
	t := func(k targetKind, id string) target {
		if !reID.MatchString(id) {
			return target{}
		}
		return target{kind: k, id: id, resourceKey: rk}
	}
	// Strip "/u/<n>" account selectors anywhere in the path.
	segs = stripUserSegs(segs)
	switch host {
	case "drive.google.com", "docs.google.com":
		if len(segs) >= 3 && segs[0] == "file" && segs[1] == "d" {
			return t(tFile, segs[2])
		}
		if host == "docs.google.com" && len(segs) >= 3 && segs[1] == "d" {
			if _, ok := docTypes[segs[0]]; ok {
				r := t(tDoc, segs[2])
				r.docType = segs[0]
				return r
			}
		}
		if len(segs) >= 1 {
			switch segs[0] {
			case "open":
				return t(tUnknown, q.Get("id"))
			case "uc":
				return t(tFile, q.Get("id"))
			case "folderview", "embeddedfolderview":
				return t(tFolder, q.Get("id"))
			}
		}
		// /drive/folders/<id>, /drive/mobile/folders/<id>, /drive/u/0/folders/<id>
		if len(segs) >= 3 && segs[0] == "drive" {
			if i := slices.Index(segs, "folders"); i > 0 && i+1 < len(segs) {
				return t(tFolder, segs[i+1])
			}
		}
	case "drive.usercontent.google.com":
		if len(segs) >= 1 && (segs[0] == "download" || segs[0] == "uc") {
			return t(tFile, q.Get("id"))
		}
	}
	return target{}
}

func stripUserSegs(segs []string) []string {
	out := segs[:0:0]
	for i := 0; i < len(segs); i++ {
		if segs[i] == "u" && i+1 < len(segs) {
			if _, err := strconv.Atoi(segs[i+1]); err == nil {
				i++
				continue
			}
		}
		out = append(out, segs[i])
	}
	return out
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, nil)
}

// ResolveStream implements hosters.StreamingResolver.
func (h *Hoster) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("gdrive: parse link: %w", err))
	}
	t := parseLink(u)
	if t.kind == tNone {
		return nil, hosters.NewFatal(errors.New("gdrive: unsupported link"))
	}
	if emit == nil {
		emit = func(hosters.FileInfo) {}
	}
	api, err := h.api(ctx, s)
	if err != nil {
		return nil, err
	}
	if api != nil {
		return h.resolveAPI(ctx, s, api, t, emit)
	}
	return h.resolveAnon(ctx, s, t, emit)
}

// Prepare implements hosters.Hoster.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	t := targetOf(f)
	if t.kind == tNone {
		return nil, hosters.NewFatal(errors.New("gdrive: file has no Drive id"))
	}
	api, err := h.api(ctx, s)
	if err != nil {
		return nil, err
	}
	if api != nil {
		return h.prepareAPI(ctx, s, api, t, f)
	}
	if t.kind == tDoc {
		return h.prepareAnonExport(t, f), nil
	}
	return h.prepareAnonFile(ctx, s, t, f)
}

// targetOf rebuilds the target from a (possibly persisted) FileInfo.
func targetOf(f hosters.FileInfo) target {
	var t target
	if u, err := url.Parse(f.Link); err == nil {
		t = parseLink(u)
	}
	if f.ID != "" && reID.MatchString(f.ID) {
		t.id = f.ID
		if t.kind == tNone || t.kind == tUnknown || t.kind == tFolder {
			t.kind = tFile
		}
	}
	if dt := f.Meta[metaDocType]; dt != "" {
		if _, ok := docTypes[dt]; ok {
			t.kind, t.docType = tDoc, dt
		}
	}
	if rk := f.Meta[metaResourceKey]; rk != "" {
		t.resourceKey = rk
	}
	return t
}

// FileInfo.Meta keys.
const (
	metaDocType     = "doc_type"      // document | spreadsheets | presentation | drawings
	metaExport      = "export_format" // docx, xlsx, ...
	metaResourceKey = "resource_key"  // not secret: part of the shared link
	metaListing     = "listing"       // "embedded" for anonymous folder listings
)

func fileLink(t target) string {
	var l string
	if t.kind == tDoc {
		l = defaultDocsBase + "/" + t.docType + "/d/" + t.id + "/edit"
	} else {
		l = defaultDriveBase + "/file/d/" + t.id + "/view"
	}
	if t.resourceKey != "" {
		l += "?resourcekey=" + url.QueryEscape(t.resourceKey)
	}
	return l
}

// exportName appends the export extension unless name already has it.
func exportName(name, format string) string {
	if strings.HasSuffix(strings.ToLower(name), "."+format) {
		return name
	}
	return name + "." + format
}

func docTypeOfMime(mime string) string {
	for typ, m := range docTypes {
		if m == mime {
			return typ
		}
	}
	return ""
}
