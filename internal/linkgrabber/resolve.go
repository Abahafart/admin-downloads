package linkgrabber

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"

	"admindescargas/internal/hosters"
)

// Resolve expands link with h. When h implements hosters.StreamingResolver
// files are reported through emit while the listing is in progress;
// otherwise emit is called for every file after Resolve returns. emit may be
// nil. The returned result always contains every file.
func Resolve(ctx context.Context, h hosters.Hoster, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	if h == nil {
		return nil, errors.New("linkgrabber: no hoster")
	}
	if emit == nil {
		emit = func(hosters.FileInfo) {}
	}
	if sr, ok := h.(hosters.StreamingResolver); ok {
		res, err := sr.ResolveStream(ctx, s, link, emit)
		if err != nil {
			return nil, err
		}
		if res == nil {
			return nil, errors.New("linkgrabber: hoster returned no result")
		}
		return res, nil
	}
	res, err := h.Resolve(ctx, s, link)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("linkgrabber: hoster returned no result")
	}
	for _, f := range res.Files {
		emit(f)
	}
	return res, nil
}

// PackageName proposes a package name for a resolved link: the folder name,
// else the single file's name, else the last path segment of the link.
func PackageName(res *hosters.ResolveResult, link string) string {
	if res != nil {
		if n := strings.TrimSpace(res.Name); n != "" {
			return n
		}
		if len(res.Files) > 0 {
			if n := strings.TrimSpace(res.Files[0].Name); n != "" {
				return n
			}
		}
	}
	if u, err := url.Parse(link); err == nil {
		if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
			if un, err := url.PathUnescape(b); err == nil {
				return un
			}
			return b
		}
		return u.Hostname()
	}
	return link
}

// NormalizeURL returns the canonical form used for deduplication: spaces
// trimmed, scheme and host lower-cased, default ports removed. Path, query
// and fragment (MEGA keys) are kept verbatim. Unparsable input is returned
// trimmed.
func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	}
	u.Host = host
	return u.String()
}
