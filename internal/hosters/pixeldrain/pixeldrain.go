package pixeldrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Name is the plugin name.
const Name = "pixeldrain"

func init() { hosters.RegisterFactory(Name, New) }

// maxBody caps API responses read into memory.
const maxBody = 8 << 20

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)

// Hoster is the Pixeldrain plugin. It is stateless and safe for concurrent
// use.
type Hoster struct {
	cfg     config.HosterConfig
	apiBase string
	domains []string
}

var (
	_ hosters.Hoster         = (*Hoster)(nil)
	_ hosters.AccountChecker = (*Hoster)(nil)
)

// New builds the plugin from its [hosters.pixeldrain] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	h := &Hoster{cfg: cfg, apiBase: defaultAPIBase}
	if v := strings.TrimSpace(cfg.Options["api_base"]); v != "" {
		if _, err := url.ParseRequestURI(v); err != nil {
			return nil, fmt.Errorf("pixeldrain: invalid api_base: %w", err)
		}
		h.apiBase = strings.TrimRight(v, "/")
	}
	h.domains = append(h.domains, defaultDomains...)
	for _, d := range cfg.Domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			h.domains = append(h.domains, d)
		}
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
		AccountKinds:      []accounts.Kind{accounts.KindAPIKey},
		DefaultRetryAfter: time.Hour,
		QuotaScope:        hosters.ScopeIP,
	}
}

// linkKind is what a link points to.
type linkKind int

const (
	kindFile linkKind = iota
	kindList
)

type parsedLink struct {
	kind linkKind
	ids  []string // file ids (kindFile) or one list id
	item int      // list item index, -1 = all
}

func (h *Hoster) hostOK(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, d := range h.domains {
		if host == strings.TrimPrefix(d, "www.") {
			return true
		}
	}
	return false
}

func (h *Hoster) parse(u *url.URL) (parsedLink, bool) {
	if u == nil || !h.hostOK(u.Hostname()) {
		return parsedLink{}, false
	}
	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	var kind, raw string
	switch {
	case len(segs) >= 2 && (segs[0] == "u" || segs[0] == "l"):
		kind, raw = segs[0], segs[1]
	case len(segs) >= 3 && segs[0] == "api" && (segs[1] == "file" || segs[1] == "list"):
		kind, raw = map[string]string{"file": "u", "list": "l"}[segs[1]], segs[2]
	default:
		return parsedLink{}, false
	}
	raw, err := url.PathUnescape(raw)
	if err != nil {
		return parsedLink{}, false
	}
	p := parsedLink{item: -1}
	if kind == "l" {
		if !idRe.MatchString(raw) {
			return parsedLink{}, false
		}
		p.kind, p.ids = kindList, []string{raw}
		if v, err := url.ParseQuery(u.Fragment); err == nil {
			if n, err := strconv.Atoi(v.Get("item")); err == nil && n >= 0 {
				p.item = n
			}
		}
		return p, true
	}
	for _, id := range strings.Split(raw, ",") {
		if !idRe.MatchString(id) {
			return parsedLink{}, false
		}
		p.ids = append(p.ids, id)
	}
	p.kind = kindFile
	return p, len(p.ids) > 0
}

// Match implements hosters.Hoster.
func (h *Hoster) Match(u *url.URL) bool {
	_, ok := h.parse(u)
	return ok
}

// Resolve implements hosters.Hoster.
func (h *Hoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("pixeldrain: parse link: %w", err))
	}
	p, ok := h.parse(u)
	if !ok {
		return nil, hosters.NewFatal(fmt.Errorf("pixeldrain: unsupported link"))
	}
	if p.kind == kindList {
		return h.resolveList(ctx, s, p)
	}
	res := &hosters.ResolveResult{}
	for _, id := range p.ids {
		var fi fileInfo
		if err := h.getJSON(ctx, s, apiPath(h.apiBase, pathFileInfo, id), &fi); err != nil {
			return nil, fmt.Errorf("pixeldrain: file info %s: %w", id, err)
		}
		if fi.ID == "" {
			fi.ID = id
		}
		res.Files = append(res.Files, h.toFileInfo(fi, ""))
	}
	return res, nil
}

func (h *Hoster) resolveList(ctx context.Context, s *hosters.Session, p parsedLink) (*hosters.ResolveResult, error) {
	id := p.ids[0]
	var li listInfo
	if err := h.getJSON(ctx, s, apiPath(h.apiBase, pathList, id), &li); err != nil {
		return nil, fmt.Errorf("pixeldrain: list %s: %w", id, err)
	}
	name := strings.TrimSpace(li.Title)
	if name == "" {
		name = id
	}
	if p.item >= 0 {
		if p.item >= len(li.Files) {
			return nil, hosters.NewNotFound(fmt.Errorf("pixeldrain: list %s has no item %d", id, p.item))
		}
		return &hosters.ResolveResult{Files: []hosters.FileInfo{h.toFileInfo(li.Files[p.item], "")}}, nil
	}
	res := &hosters.ResolveResult{Name: name, IsFolder: true}
	used := map[string]int{}
	for _, f := range li.Files {
		res.Files = append(res.Files, h.toFileInfo(f, uniqueName(used, f.Name, f.ID)))
	}
	return res, nil
}

// uniqueName returns name, or "name (n).ext" if it was already used in a
// flat list (Pixeldrain lists may hold several files with the same name).
func uniqueName(used map[string]int, name, id string) string {
	if strings.TrimSpace(name) == "" {
		name = id
	}
	key := strings.ToLower(name)
	n := used[key]
	used[key] = n + 1
	if n == 0 {
		return name
	}
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		ext = name[i:]
	}
	cand := fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
	used[strings.ToLower(cand)]++
	return cand
}

func (h *Hoster) toFileInfo(f fileInfo, relPath string) hosters.FileInfo {
	name := f.Name
	if strings.TrimSpace(name) == "" {
		name = f.ID
	}
	fi := hosters.FileInfo{
		ID:      f.ID,
		Name:    name,
		RelPath: relPath,
		Size:    f.Size,
		Link:    "https://pixeldrain.com/u/" + f.ID,
	}
	if fi.Size <= 0 {
		fi.Size = -1
	}
	if f.HashSHA256 != "" {
		fi.Checksum = &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: strings.ToLower(f.HashSHA256)}
	}
	if !availabilityOK(f.Availability) {
		fi.Meta = map[string]string{"availability": f.Availability}
	}
	return fi
}

// Prepare implements hosters.Hoster. The API download URL is stable, so no
// request is made unless the file id is missing.
func (h *Hoster) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	id := f.ID
	if id == "" {
		u, err := url.Parse(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(fmt.Errorf("pixeldrain: parse link: %w", err))
		}
		p, ok := h.parse(u)
		if !ok || p.kind != kindFile || len(p.ids) != 1 {
			return nil, hosters.NewFatal(errors.New("pixeldrain: file link without id"))
		}
		id = p.ids[0]
	}
	hdr, err := h.authHeader(ctx, s)
	if err != nil {
		return nil, err
	}
	// Check availability first: rate limited files answer with a JSON error
	// the downloader would otherwise only see after starting.
	var fi fileInfo
	if err := h.getJSON(ctx, s, apiPath(h.apiBase, pathFileInfo, id), &fi); err != nil {
		return nil, fmt.Errorf("pixeldrain: file info %s: %w", id, err)
	}
	if !availabilityOK(fi.Availability) && s.Anonymous() {
		return nil, classifyValue(fi.Availability, "", 0)
	}
	spec := &hosters.DownloadSpec{
		URL:       apiPath(h.apiBase, pathFile, id) + "?download",
		Header:    hdr,
		Filename:  fi.Name,
		Size:      fi.Size,
		Resumable: true,
	}
	if spec.Size <= 0 {
		spec.Size = -1
	}
	if fi.HashSHA256 != "" {
		spec.Checksum = &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: strings.ToLower(fi.HashSHA256)}
	}
	return spec, nil
}

// authHeader returns the basic-auth header for API-key sessions.
func (h *Hoster) authHeader(ctx context.Context, s *hosters.Session) (http.Header, error) {
	hdr := http.Header{}
	if s.Anonymous() {
		return hdr, nil
	}
	sec, err := s.Secret(ctx)
	if err != nil {
		return nil, hosters.NewNeedsAuth(fmt.Errorf("pixeldrain: load api key: %w", err))
	}
	if sec.APIKey == "" {
		return nil, hosters.NewNeedsAuth(errors.New("pixeldrain: account has no api key"))
	}
	r := &http.Request{Header: hdr}
	r.SetBasicAuth("", sec.APIKey)
	return hdr, nil
}

// CheckAccount implements hosters.AccountChecker.
func (h *Hoster) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	if s.Anonymous() {
		return nil, hosters.ErrNoAccount
	}
	var ui userInfo
	err := h.getJSON(ctx, s, strings.TrimRight(h.apiBase, "/")+pathUser, &ui)
	switch {
	case hosters.IsNeedsAuth(err):
		return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: "API key rechazada", QuotaTotal: -1, QuotaUsed: -1}, nil
	case err != nil:
		return nil, err
	}
	res := &accounts.CheckResult{Status: accounts.StatusOK, QuotaTotal: -1, QuotaUsed: -1}
	sub := strings.ToLower(ui.Subscription.ID + ui.Subscription.Type)
	res.Premium = sub != "" && !strings.Contains(sub, "free")
	if ui.Subscription.Name != "" {
		res.Message = ui.Subscription.Name
	}
	limit := ui.MonthlyTransferCap
	if limit <= 0 {
		limit = ui.Subscription.MonthlyTransferCap
	}
	if limit > 0 {
		res.QuotaTotal, res.QuotaUsed = limit, ui.MonthlyTransferUsed
		if ui.MonthlyTransferUsed >= limit {
			res.Status = accounts.StatusQuota
		}
	}
	return res, nil
}

// getJSON GETs u (with the account's auth) and decodes the JSON body.
func (h *Hoster) getJSON(ctx context.Context, s *hosters.Session, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return hosters.NewFatal(err)
	}
	hdr, err := h.authHeader(ctx, s)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return h.Classify(nil, nil, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return hosters.NewTemporary(0, fmt.Errorf("read body: %w", err))
	}
	if err := h.Classify(resp, body, nil); err != nil {
		return err
	}
	var ae apiError
	if json.Unmarshal(body, &ae) == nil && ae.Success != nil && !*ae.Success {
		return classifyValue(ae.Value, ae.Message, resp.StatusCode)
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
	var ae apiError
	isJSON := len(body) > 0 && json.Unmarshal(body, &ae) == nil && (ae.Value != "" || ae.Success != nil)
	if isJSON && ae.Success != nil && !*ae.Success {
		return classifyValue(ae.Value, ae.Message, resp.StatusCode)
	}
	if resp.StatusCode < 400 {
		return nil
	}
	return hosters.DefaultClassify(resp, body, nil)
}

// classifyValue maps a Pixeldrain error value ("not_found",
// "file_rate_limited_captcha_required", ...) to an outcome.
func classifyValue(value, msg string, code int) error {
	v := strings.ToLower(value)
	e := fmt.Errorf("pixeldrain: %s", value)
	if msg != "" {
		e = fmt.Errorf("pixeldrain: %s: %s", value, msg)
	}
	switch {
	case strings.Contains(v, "not_found"), code == http.StatusNotFound:
		return hosters.NewNotFound(e)
	case strings.HasPrefix(v, "ip_"), strings.Contains(v, "transfer_limit"), strings.Contains(v, "bandwidth_limit"),
		strings.Contains(v, "rate_limit") && !strings.HasPrefix(v, "file_"), code == http.StatusTooManyRequests:
		// Per-IP free limit: rotating the egress helps.
		return hosters.NewQuota(hosters.ScopeIP, 0, e)
	case strings.Contains(v, "captcha"), strings.Contains(v, "unauthorized"), strings.Contains(v, "authenticat"),
		code == http.StatusUnauthorized:
		// Captcha required for anonymous downloads (hot-linked or flagged
		// file): an API key account lifts it, rotating the IP does not.
		return hosters.NewNeedsAuth(e)
	case strings.Contains(v, "forbidden"), strings.Contains(v, "banned"), strings.Contains(v, "abuse"),
		strings.Contains(v, "deleted"), strings.Contains(v, "removed"):
		return hosters.NewNotFound(e)
	case code >= 500:
		return hosters.NewTemporary(0, e)
	default:
		return hosters.NewFatal(e)
	}
}
