package clipboard

import (
	"regexp"
	"strings"

	"admindescargas/internal/hosters"
)

// RegistryMatcher adapts a hoster registry to a Matcher. Catch-all plugins
// (Caps().Fallback, e.g. the generic HTTP plugin) are ignored so that only
// links of real hosters are detected; a new plugin becomes detectable
// without touching the monitor.
func RegistryMatcher(r *hosters.Registry) Matcher {
	return func(link string) (string, bool) {
		if r == nil {
			return "", false
		}
		h := r.MatchString(link)
		if h == nil || h.Caps().Fallback {
			return "", false
		}
		return h.Name(), true
	}
}

// displayNames are the user-facing names of the known plugins.
var displayNames = map[string]string{
	"mega":       "MEGA",
	"mediafire":  "MediaFire",
	"gdrive":     "Google Drive",
	"terabox":    "TeraBox",
	"pixeldrain": "Pixeldrain",
	"gofile":     "Gofile",
	"dropbox":    "Dropbox",
	"onedrive":   "OneDrive",
	"onefichier": "1fichier",
	"yandex":     "Yandex Disk",
	"generic":    "HTTP",
}

// DisplayName returns the user-facing name of a hoster plugin.
func DisplayName(hoster string) string {
	if n, ok := displayNames[hoster]; ok {
		return n
	}
	return hoster
}

// urlRe finds http(s) URLs in free text. It is deliberately permissive
// (MEGA's old "#!id!key" links contain '#' and '!'); trailing punctuation
// is trimmed afterwards.
var urlRe = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'\x60{}|\\^\[\]]+`)

// SimpleExtract is a basic Extractor for when linkgrabber.ExtractURLs is not
// wired: every http(s) URL, without trailing sentence punctuation or an
// unbalanced closing parenthesis.
func SimpleExtract(text string) []string {
	found := urlRe.FindAllString(text, -1)
	out := found[:0]
	for _, u := range found {
		u = strings.TrimRight(u, ".,;:!?'\"")
		for strings.HasSuffix(u, ")") && strings.Count(u, "(") < strings.Count(u, ")") {
			u = strings.TrimRight(strings.TrimSuffix(u, ")"), ".,;:!?")
		}
		if len(u) > len("https://") {
			out = append(out, u)
		}
	}
	return out
}
