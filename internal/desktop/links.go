package desktop

import (
	"net/url"
	"strings"
)

// Scheme is the custom URL scheme registered by the installers
// (adesc://add?url=<link>[&url=<link>...]).
const Scheme = "adesc"

// ParseSchemeURL returns the links of an adesc:// URL. Accepted forms:
// adesc://add?url=..., adesc:add?url=..., adesc:///add?url=... with one or
// more url parameters (and optionally text=, free text with links). Only
// http(s) links are returned: anything else (file:, javascript:...) is
// dropped. ok is false when raw is not an adesc URL.
func ParseSchemeURL(raw string) (links []string, text string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, Scheme) {
		return nil, "", false
	}
	action := u.Host
	if action == "" {
		action = strings.Trim(u.Path, "/")
		if action == "" {
			action = u.Opaque
		}
	}
	action = strings.ToLower(strings.Trim(action, "/"))
	if action != "add" {
		return nil, "", true
	}
	q := u.Query()
	for _, l := range q["url"] {
		if IsHTTPURL(l) {
			links = append(links, strings.TrimSpace(l))
		}
	}
	return links, q.Get("text"), true
}

// IsHTTPURL reports whether s is an absolute http(s) URL with a host.
func IsHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

// LinksFromArgs collects the links passed on the command line (second
// instance, OS URL handler, file manager "open with"): adesc:// URLs and
// bare http(s) links. Flags (starting with "-") and everything else are
// ignored. text is the concatenated text= parameters of adesc URLs.
func LinksFromArgs(args []string) (links []string, text string) {
	var texts []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if ls, t, ok := ParseSchemeURL(a); ok {
			links = append(links, ls...)
			if t != "" {
				texts = append(texts, t)
			}
			continue
		}
		if IsHTTPURL(a) {
			links = append(links, strings.TrimSpace(a))
		}
	}
	return links, strings.Join(texts, "\n")
}
