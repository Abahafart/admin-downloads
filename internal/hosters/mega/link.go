package mega

import (
	"errors"
	"net/url"
	"strings"
)

// defaultDomains are the hostnames handled by the plugin (plus "www."
// variants). HosterConfig.Domains extends the list.
var defaultDomains = []string{"mega.nz", "mega.co.nz", "mega.io", "mega.app"}

// linkKind distinguishes public file links from folder links.
type linkKind int

const (
	linkFile linkKind = iota + 1
	linkFolder
)

// megaLink is a parsed MEGA URL.
type megaLink struct {
	Kind   linkKind
	Handle string // file handle, or folder root handle
	Key    string // base64url key from the fragment ("" if missing)
	// Folder links only: restrict to a subfolder, or to a single file.
	SubFolder string
	File      string
}

var (
	errNotMEGA       = errors.New("mega: not a MEGA link")
	errPasswordLink  = errors.New("mega: password-protected links (#P!) are not supported; open the link in a browser to get the decrypted link")
	errMissingKey    = errors.New("mega: the link has no decryption key (the part after '#')")
	errMalformedLink = errors.New("mega: malformed MEGA link")
)

// isB64 reports whether s is non-empty and only has base64url characters.
func isB64(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// cutKey trims anything after the key itself (query-like garbage that some
// sites append, e.g. "?foo" or "&bar").
func cutKey(s string) string {
	if i := strings.IndexAny(s, "?&# "); i >= 0 {
		return s[:i]
	}
	return s
}

// parseLink parses every supported MEGA link format:
//
//	/file/<h>#<k>                  /embed/<h>#<k>
//	/folder/<h>#<k>                /folder/<h>#<k>/file/<id>
//	/folder/<h>#<k>/folder/<sub>
//	#!<h>!<k>                      #F!<h>!<k>      #F!<h>!<k>!<sub>
//
// Hostname checks are done by the caller.
func parseLink(u *url.URL) (megaLink, error) {
	var l megaLink
	frag := u.Fragment
	if frag == "" && u.RawFragment != "" {
		frag = u.RawFragment
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) >= 2 && segs[0] != "" {
		switch segs[0] {
		case "file", "embed":
			l.Kind = linkFile
		case "folder":
			l.Kind = linkFolder
		default:
			return l, errNotMEGA
		}
		l.Handle = segs[1]
		// Some sites write the key as "/file/<h>!<k>".
		if i := strings.IndexByte(l.Handle, '!'); i >= 0 && frag == "" {
			l.Handle, frag = l.Handle[:i], l.Handle[i+1:]
		}
		if !isB64(l.Handle) {
			return l, errMalformedLink
		}
		parts := strings.Split(frag, "/")
		l.Key = cutKey(parts[0])
		if l.Kind == linkFolder && len(parts) >= 3 && isB64(cutKey(parts[2])) {
			switch parts[1] {
			case "file":
				l.File = cutKey(parts[2])
			case "folder":
				l.SubFolder = cutKey(parts[2])
			}
		}
		if l.Key != "" && !isB64(l.Key) {
			return l, errMalformedLink
		}
		return l, nil
	}
	if len(segs) == 1 && segs[0] != "" && segs[0] != "#" {
		return l, errNotMEGA
	}
	// Legacy fragment links.
	switch {
	case strings.HasPrefix(frag, "P!"):
		return l, errPasswordLink
	case strings.HasPrefix(frag, "!"):
		l.Kind = linkFile
		frag = frag[1:]
	case strings.HasPrefix(frag, "F!"):
		l.Kind = linkFolder
		frag = frag[2:]
	default:
		return l, errNotMEGA
	}
	parts := strings.Split(frag, "!")
	l.Handle = cutKey(parts[0])
	if !isB64(l.Handle) {
		return l, errMalformedLink
	}
	if len(parts) > 1 {
		l.Key = cutKey(parts[1])
		if l.Key != "" && !isB64(l.Key) {
			return l, errMalformedLink
		}
	}
	if l.Kind == linkFolder && len(parts) > 2 && isB64(cutKey(parts[2])) {
		l.SubFolder = cutKey(parts[2])
	}
	return l, nil
}

// fileLink is the canonical public file URL.
func fileLink(handle, key string) string {
	return "https://mega.nz/file/" + handle + "#" + key
}

// folderLink is the canonical folder URL.
func folderLink(root, key string) string {
	return "https://mega.nz/folder/" + root + "#" + key
}

// folderFileLink is the canonical URL of a file inside a shared folder.
func folderFileLink(root, key, file string) string {
	return folderLink(root, key) + "/file/" + file
}

// hostMatches reports whether host is one of domains or a subdomain of one.
func hostMatches(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
