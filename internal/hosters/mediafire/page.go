package mediafire

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"admindescargas/internal/hosters"
)

// directLink loads the file page and returns the direct download URL.
func (h *Hoster) directLink(ctx context.Context, s *hosters.Session, key string) (string, error) {
	if err := h.limiter.Wait(ctx); err != nil {
		return "", hosters.NewFatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.webBase+"/file/"+url.PathEscape(key)+"/", nil)
	if err != nil {
		return "", hosters.NewFatal(fmt.Errorf("mediafire: build request: %w", err))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	// Premium sessions are redirected straight to the download server:
	// stop there instead of downloading the file body.
	c := *s.Client
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if reDirectHost.MatchString(r.URL.Hostname()) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", h.Classify(nil, nil, fmt.Errorf("mediafire: file page: %w", redactURLError(err)))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc, err := resp.Location(); err == nil && reDirectHost.MatchString(loc.Hostname()) {
			return loc.String(), nil
		}
	}
	final := resp.Request.URL
	if m := reErrno.FindStringSubmatch(final.RawQuery); m != nil && strings.Contains(final.Path, "error.php") {
		return "", errnoError(m[1])
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if err := h.Classify(resp, body, nil); err != nil {
			return "", fmt.Errorf("mediafire: file page: %w", err)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", hosters.NewTemporary(0, fmt.Errorf("mediafire: read file page: %w", err))
	}
	return parseFilePage(body, final)
}

// parseFilePage extracts the direct link from a file page, or explains why
// there is none.
func parseFilePage(body []byte, base *url.URL) (string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", hosters.NewTemporary(0, fmt.Errorf("mediafire: parse file page: %w", err))
	}
	btn := doc.Find(selDownloadButton).First()
	if btn.Length() > 0 {
		if href, ok := btn.Attr("href"); ok {
			if u := absDirect(href, base); u != "" {
				return u, nil
			}
		}
		if enc, ok := btn.Attr(attrScrambledURL); ok {
			if u := decodeScrambled(enc); u != "" {
				return u, nil
			}
		}
	}
	// Any element carrying a scrambled URL (layout changes).
	var found string
	doc.Find("[" + attrScrambledURL + "]").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		enc, _ := sel.Attr(attrScrambledURL)
		found = decodeScrambled(enc)
		return found == ""
	})
	if found != "" {
		return found, nil
	}
	if m := reDirectLink.Find(body); m != nil {
		return html.UnescapeString(string(m)), nil
	}
	return "", explainPage(doc, body)
}

// absDirect returns href as an absolute http(s) URL if it looks like a real
// link (not "javascript:" or "#").
func absDirect(href string, base *url.URL) string {
	href = strings.TrimSpace(href)
	if href == "" || href == "#" || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	// A link back to a MediaFire page is not a download.
	if !reDirectHost.MatchString(u.Hostname()) && strings.Contains(u.Hostname(), "mediafire.com") &&
		(strings.HasPrefix(u.Path, "/file/") || u.Path == "" || u.Path == "/") {
		return ""
	}
	return u.String()
}

// decodeScrambled decodes the base64 data-scrambled-url attribute.
func decodeScrambled(enc string) string {
	enc = strings.TrimSpace(enc)
	if enc == "" {
		return ""
	}
	for _, e := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		b, err := e.DecodeString(enc)
		if err != nil {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(string(b)))
		if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return u.String()
		}
	}
	return ""
}

// explainPage turns a file page without a download link into a typed error.
func explainPage(doc *goquery.Document, body []byte) error {
	lower := strings.ToLower(string(body))
	switch {
	case isCaptcha(body):
		return captchaError()
	case doc.Find(selPasswordInput).Length() > 0 || strings.Contains(lower, "password protected"):
		return hosters.NewFatal(errors.New("mediafire: the file is password protected (not supported)"))
	case containsAny(lower, markersDangerous):
		return hosters.NewFatal(errors.New("mediafire: the file was blocked by MediaFire as dangerous and cannot be downloaded"))
	case containsAny(lower, markersRemoved):
		return hosters.NewNotFound(errors.New("mediafire: the file was removed or the link is invalid"))
	}
	return hosters.NewTemporary(0, errors.New("mediafire: no download link found on the file page (layout change?)"))
}

func errnoError(errno string) error {
	what := errnoMeaning[errno]
	if what == "" {
		what = "file unavailable"
	}
	err := fmt.Errorf("mediafire: %s (errno %s)", what, errno)
	switch errno {
	case "388", "386":
		return hosters.NewFatal(err)
	case "394":
		return hosters.NewNeedsAuth(err)
	}
	return hosters.NewNotFound(err)
}

func isCaptcha(body []byte) bool {
	return containsAny(strings.ToLower(string(body)), markersCaptcha)
}

func captchaError() error {
	return hosters.NewQuota(hosters.ScopeIP, captchaRetryAfter,
		errors.New("mediafire: captcha / too many requests from this IP"))
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
