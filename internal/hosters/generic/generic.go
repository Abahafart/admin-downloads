package generic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"admindescargas/internal/config"
	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
)

// Name is the plugin identifier (also the [hosters.generic] config key).
const Name = "generic"

// DefaultRetryAfter is the quota wait used when the config sets none.
const DefaultRetryAfter = 10 * time.Minute

// Meta keys stored in FileInfo.Meta.
const (
	// MetaResumable is "true" or "false": whether the probe saw Range
	// support. "true" also covers "unknown" (the downloader re-checks).
	MetaResumable = "resumable"
)

const (
	// probeDrainBytes is how much of a successful probe body is read before
	// closing it (enough to drain a 206 of one byte and allow keep-alive).
	probeDrainBytes = 512
	// classifyBodyBytes is how much of an error body is passed to Classify.
	classifyBodyBytes = 4 << 10
)

func init() { hosters.RegisterFactory(Name, New) }

// Generic is the fallback hoster for direct http(s) links. It is stateless
// and safe for concurrent use.
type Generic struct {
	caps hosters.Caps
}

var _ hosters.Hoster = (*Generic)(nil)

// New builds the plugin from its [hosters.generic] settings.
func New(cfg config.HosterConfig) (hosters.Hoster, error) {
	if cfg.MaxConnsPerFile < 0 {
		return nil, fmt.Errorf("generic: max_conns_per_file must be >= 0, got %d", cfg.MaxConnsPerFile)
	}
	if cfg.MaxParallel < 0 {
		return nil, fmt.Errorf("generic: max_parallel must be >= 0, got %d", cfg.MaxParallel)
	}
	retry := cfg.RetryAfter.Duration
	if retry <= 0 {
		retry = DefaultRetryAfter
	}
	return &Generic{caps: hosters.Caps{
		MaxConnsPerFile:   cfg.MaxConnsPerFile,
		MaxParallel:       cfg.MaxParallel,
		Resumable:         true,
		DefaultRetryAfter: retry,
		QuotaScope:        hosters.ScopeIP,
		Fallback:          true,
	}}, nil
}

// Name implements hosters.Hoster.
func (g *Generic) Name() string { return Name }

// Caps implements hosters.Hoster.
func (g *Generic) Caps() hosters.Caps { return g.caps }

// Match implements hosters.Hoster: any absolute http or https URL with a
// host. FTP is not supported.
func (g *Generic) Match(u *url.URL) bool {
	if u == nil || u.Hostname() == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	}
	return false
}

// Classify implements hosters.Hoster.
func (g *Generic) Classify(resp *http.Response, body []byte, err error) error {
	return hosters.DefaultClassify(resp, body, err)
}

// Prepare implements hosters.Hoster. The link is already the direct
// download, so no I/O is needed.
func (g *Generic) Prepare(_ context.Context, _ *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	if f.Link == "" {
		return nil, hosters.NewFatal(errors.New("generic: file has no link"))
	}
	return &hosters.DownloadSpec{
		URL:       f.Link,
		Size:      f.Size,
		Resumable: f.Meta[MetaResumable] != "false",
	}, nil
}

// Resolve implements hosters.Hoster. It probes link with HEAD and, when that
// fails or is incomplete, with a ranged GET of one byte.
func (g *Generic) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !g.Match(u) {
		return nil, hosters.NewFatal(errors.New("generic: not an absolute http(s) URL"))
	}
	if s == nil || s.Client == nil {
		return nil, hosters.NewFatal(errors.New("generic: session has no HTTP client"))
	}

	p, err := g.probe(ctx, s, link)
	if err != nil {
		return nil, err
	}

	name := p.name
	if name == "" {
		name = nameFromURL(u)
	}
	if name == "" && p.finalURL != nil {
		name = nameFromURL(p.finalURL)
	}
	if name == "" {
		name = cleanName(u.Hostname())
	}
	if name == "" {
		name = fsutil.SanitizeName("download")
	}

	resumable := "true"
	if p.ranges == rangesNo {
		resumable = "false"
	}
	return &hosters.ResolveResult{
		IsFolder: false,
		Files: []hosters.FileInfo{{
			Name: name,
			Size: p.size,
			Link: link,
			Meta: map[string]string{MetaResumable: resumable},
		}},
	}, nil
}

// rangeSupport is what a probe learned about Range requests.
type rangeSupport int

const (
	rangesUnknown rangeSupport = iota
	rangesYes
	rangesNo
)

// probeResult is the information gathered from HEAD / ranged GET.
type probeResult struct {
	name     string // from Content-Disposition, already cleaned ("" = none)
	size     int64  // -1 = unknown
	ranges   rangeSupport
	finalURL *url.URL // URL after redirects (nil if unknown)
}

// probe runs HEAD and, if needed, the GET fallback. Errors are classified.
func (g *Generic) probe(ctx context.Context, s *hosters.Session, link string) (probeResult, error) {
	p := probeResult{size: -1}

	var headErr error
	headOK, headStatus := false, 0
	resp, err := send(ctx, s, http.MethodHead, link, false)
	switch {
	case err != nil:
		headErr = g.Classify(nil, nil, err)
		if ctx.Err() != nil {
			return p, headErr
		}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		_ = resp.Body.Close()
		headOK = true
		p.absorb(resp)
	default:
		_ = resp.Body.Close()
		headStatus = resp.StatusCode
		headErr = g.Classify(resp, nil, nil)
	}

	// HEAD 200 with Content-Length 0 is often a lie (dynamic handlers), so
	// only a positive size counts as complete.
	if headOK && p.size > 0 && p.ranges != rangesUnknown {
		return p, nil
	}

	gp, gotResp, err := g.probeGET(ctx, s, link)
	if err != nil {
		if headOK {
			return p, nil // keep what HEAD told us; the downloader re-checks
		}
		if !gotResp && ctx.Err() == nil && definitiveStatus(headStatus) {
			// HEAD got a definitive status, GET only a transport error:
			// the status is more informative.
			return p, headErr
		}
		return p, err
	}
	if !headOK {
		return gp, nil
	}
	// Merge: HEAD wins for the name, GET fills the gaps.
	if p.name == "" {
		p.name = gp.name
	}
	if gp.size >= 0 && p.size <= 0 {
		p.size = gp.size
	}
	if gp.ranges != rangesUnknown {
		p.ranges = gp.ranges
	}
	if p.finalURL == nil {
		p.finalURL = gp.finalURL
	}
	return p, nil
}

// definitiveStatus reports whether a HEAD error status can be trusted even
// though GET could not confirm it. 403 and 405 are excluded: many servers
// (signed S3 URLs, simple handlers) reject HEAD but serve GET.
func definitiveStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusNotFound, http.StatusGone:
		return true
	}
	return false
}

// probeGET requests the first byte of link. gotResp reports whether an HTTP
// response was received (as opposed to a transport error).
func (g *Generic) probeGET(ctx context.Context, s *hosters.Session, link string) (p probeResult, gotResp bool, err error) {
	p.size = -1
	resp, err := send(ctx, s, http.MethodGet, link, true)
	if err != nil {
		return p, false, g.Classify(nil, nil, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		_, _ = io.CopyN(io.Discard, resp.Body, probeDrainBytes)
		p.absorb(resp)
		if resp.StatusCode == http.StatusOK {
			// The server ignored our Range header.
			p.ranges = rangesNo
		}
		return p, true, nil
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// "bytes */0": the file exists but is empty.
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			p.absorb(resp)
			p.size = total
			p.ranges = rangesYes
			return p, true, nil
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, classifyBodyBytes))
	cerr := g.Classify(resp, body, nil)
	if cerr == nil {
		// 3xx that the client did not follow (e.g. redirect loop limit).
		cerr = hosters.NewFatal(fmt.Errorf("generic: unexpected HTTP %d", resp.StatusCode))
	}
	return p, true, cerr
}

// absorb extracts name, size, range support and final URL from a 2xx (or
// 416) response.
func (p *probeResult) absorb(resp *http.Response) {
	if resp.Request != nil && resp.Request.URL != nil {
		p.finalURL = resp.Request.URL
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if n := cleanName(filenameFromContentDisposition(cd)); n != "" {
			p.name = n
		}
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		p.ranges = rangesYes
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			p.size = total
		}
	default:
		if resp.ContentLength >= 0 {
			p.size = resp.ContentLength
		} else if n, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Content-Length")), 10, 64); err == nil && n >= 0 {
			p.size = n
		}
	}
	if p.ranges == rangesUnknown {
		for _, v := range resp.Header.Values("Accept-Ranges") {
			for _, tok := range strings.Split(v, ",") {
				switch strings.ToLower(strings.TrimSpace(tok)) {
				case "bytes":
					p.ranges = rangesYes
				case "none":
					if p.ranges == rangesUnknown {
						p.ranges = rangesNo
					}
				}
			}
		}
	}
}

// send performs one request through the session client. Transport errors
// are returned without the URL (it may be signed).
func send(ctx context.Context, s *hosters.Session, method, link string, ranged bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, link, nil)
	if err != nil {
		return nil, fmt.Errorf("generic: build %s request: %w", method, redact(err))
	}
	if ranged {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generic: %s: %w", method, redact(err))
	}
	return resp, nil
}

// redact strips the URL from a *url.Error, keeping the cause chain.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// parseContentRangeTotal returns TOTAL from "bytes 0-0/TOTAL" or
// "bytes */TOTAL". ok is false if absent, malformed or "*".
func parseContentRangeTotal(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if len(v) < 6 || !strings.EqualFold(v[:6], "bytes ") {
		return 0, false
	}
	_, total, found := strings.Cut(v[6:], "/")
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// nameFromURL returns the last non-empty path segment of u, percent-decoded
// and cleaned ("" if none).
func nameFromURL(u *url.URL) string {
	segs := strings.Split(u.EscapedPath(), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		seg := segs[i]
		if seg == "" {
			continue
		}
		if dec, err := url.PathUnescape(seg); err == nil && utf8.ValidString(dec) {
			seg = dec
		}
		return cleanName(seg)
	}
	return ""
}

// cleanName keeps only the last path component of an untrusted name and
// sanitizes it. It returns "" if nothing usable remains.
func cleanName(raw string) string {
	if i := strings.LastIndexAny(raw, `/\`); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSpace(raw)
	if strings.Trim(raw, ". ") == "" {
		return ""
	}
	return fsutil.SanitizeName(raw)
}

// filenameFromContentDisposition returns the raw (uncleaned) file name of a
// Content-Disposition value per RFC 6266: filename* (RFC 5987) wins over
// filename. The parser is lenient: unquoted values with spaces and a
// missing disposition type are accepted.
func filenameFromContentDisposition(v string) string {
	params := parseDispositionParams(v)
	if ext, ok := params["filename*"]; ok {
		if n, ok := decodeExtValue(ext); ok && strings.TrimSpace(n) != "" {
			return n
		}
	}
	n := params["filename"]
	if strings.HasPrefix(n, "=?") && strings.HasSuffix(n, "?=") {
		// RFC 2047 encoded word, sent by some servers despite RFC 6266.
		if dec, err := new(mime.WordDecoder).DecodeHeader(n); err == nil {
			n = dec
		}
	}
	return n
}

// parseDispositionParams splits "type; k=v; k2=\"v 2\"" into lowercase keys
// and unquoted values. The first occurrence of a key wins.
func parseDispositionParams(s string) map[string]string {
	out := make(map[string]string)
	for {
		s = strings.TrimLeft(s, " \t;")
		if s == "" {
			return out
		}
		i := strings.IndexAny(s, "=;")
		if i < 0 {
			return out // trailing bare token
		}
		if s[i] == ';' {
			s = s[i+1:] // bare token such as the disposition type
			continue
		}
		key := strings.ToLower(strings.TrimSpace(s[:i]))
		s = strings.TrimLeft(s[i+1:], " \t")
		var val string
		if strings.HasPrefix(s, `"`) {
			val, s = readQuoted(s[1:])
		} else {
			j := strings.IndexByte(s, ';')
			if j < 0 {
				j = len(s)
			}
			val, s = strings.TrimSpace(s[:j]), s[j:]
		}
		if _, dup := out[key]; !dup && key != "" {
			out[key] = val
		}
	}
}

// readQuoted reads a quoted-string body (after the opening quote) and
// returns its unescaped value and the remainder after the closing quote.
// An unterminated string runs to the end of s.
func readQuoted(s string) (val, rest string) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:]
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), ""
}

// decodeExtValue decodes an RFC 5987 ext-value: charset'lang'pct-encoded.
// UTF-8, US-ASCII and ISO-8859-1 are supported.
func decodeExtValue(v string) (string, bool) {
	parts := strings.SplitN(v, "'", 3)
	if len(parts) != 3 {
		return "", false
	}
	raw, err := url.PathUnescape(parts[2])
	if err != nil {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "utf-8", "us-ascii":
		if !utf8.ValidString(raw) {
			return "", false
		}
		return raw, true
	case "iso-8859-1", "latin1":
		r := make([]rune, len(raw))
		for i := 0; i < len(raw); i++ {
			r[i] = rune(raw[i])
		}
		return string(r), true
	}
	return "", false
}
