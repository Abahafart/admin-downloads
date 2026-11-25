package linkgrabber

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// bareHosts are hoster domains recognized in text even without a scheme or
// "www." prefix ("mega.nz/file/..."). They must be followed by '/'. Keep the
// list short: anything else needs http(s):// or www.
var bareHosts = []string{
	"mega.nz",
	"mega.co.nz",
	"mediafire.com",
	"drive.google.com",
	"docs.google.com",
	"terabox.com",
	"1024terabox.com",
	"teraboxapp.com",
	"pixeldrain.com",
	"gofile.io",
	"1fichier.com",
}

// ExtractURLs returns every http(s) URL found in text (clipboard paragraphs,
// lists, Markdown, HTML snippets, BBCode), in order of first appearance and
// without duplicates. Bare "www." hosts and the hoster domains in bareHosts
// get an "https://" prefix. Fragments are kept (MEGA keys live there);
// trailing sentence punctuation and unbalanced closing brackets or quotes
// are removed. It returns nil if text has no URL.
//
// The text is processed in memory only and must never be logged.
func ExtractURLs(text string) []string {
	var (
		out  []string
		seen map[string]struct{}
	)
	for i := 0; i < len(text); {
		bodyStart, prefix, ok := matchStart(text, i)
		if !ok {
			_, w := utf8.DecodeRuneInString(text[i:])
			i += w
			continue
		}
		end := scanEnd(text, i, bodyStart, contextCloser(text, i))
		cand := text[i:end]
		if inHTML(text, i) {
			cand = strings.ReplaceAll(cand, "&amp;", "&")
		}
		cand = trimTrailing(cand)
		if prefix == "" {
			// Normalize the scheme's case only ("HTTPS://" -> "https://").
			cand = strings.ToLower(text[i:bodyStart]) + cand[bodyStart-i:]
		} else {
			cand = prefix + cand
		}
		if len(cand) > bodyStart-i && validURL(cand) {
			if seen == nil {
				seen = make(map[string]struct{})
			}
			if _, dup := seen[cand]; !dup {
				seen[cand] = struct{}{}
				out = append(out, cand)
			}
			i = end
			continue
		}
		// Not a URL: skip the matched prefix and keep scanning.
		if bodyStart > i {
			i = bodyStart
		} else {
			_, w := utf8.DecodeRuneInString(text[i:])
			i += w
		}
	}
	return out
}

// matchStart reports whether a URL starts at text[i]. bodyStart is the index
// right after "scheme://" (or i for bare hosts) and prefix is the scheme to
// prepend ("" if the text already has one).
func matchStart(text string, i int) (bodyStart int, prefix string, ok bool) {
	rest := text[i:]
	for _, s := range [...]string{"https://", "http://"} {
		if hasPrefixFold(rest, s) {
			return i + len(s), "", true
		}
	}
	if !wordBoundary(text, i) {
		return 0, "", false
	}
	if hasPrefixFold(rest, "www.") && len(rest) > 4 && isAlnum(rune(rest[4])) {
		return i, "https://", true
	}
	for _, h := range bareHosts {
		if hasPrefixFold(rest, h) && len(rest) > len(h) && rest[len(h)] == '/' {
			return i, "https://", true
		}
	}
	return 0, "", false
}

// wordBoundary reports whether a bare host may start at text[i]: the
// previous rune must not belong to a word, a host name, an e-mail address
// or a path.
func wordBoundary(text string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return false
	}
	return !strings.ContainsRune(`.-_@/\%+~#?`, r)
}

// contextCloser returns an extra terminator implied by the character before
// the URL: a URL opened by a single quote ends at the next single quote.
// (Double quotes and '>' always terminate; brackets are balanced.)
func contextCloser(text string, i int) rune {
	if i > 0 && text[i-1] == '\'' {
		return '\''
	}
	return -1
}

// inHTML reports whether the URL at text[i] is an HTML attribute value
// (href="...", src='...') or starts an element's text (<a ...>https://...),
// where "&amp;" stands for '&'.
func inHTML(text string, i int) bool {
	if i >= 1 && text[i-1] == '>' {
		return true
	}
	return i >= 2 && text[i-2] == '=' && (text[i-1] == '"' || text[i-1] == '\'')
}

// scanEnd returns the index where the URL starting at text[start] ends.
func scanEnd(text string, start, bodyStart int, closer rune) int {
	var (
		paren, brace int
		inHost       = true
		ipv6         = false
	)
	j := bodyStart
	for j < len(text) {
		r, w := utf8.DecodeRuneInString(text[j:])
		if (r == utf8.RuneError && w <= 1) || r == closer || isTerminator(r) {
			return j
		}
		switch r {
		case '/', '?', '#':
			inHost = false
		case '[':
			// Only an IPv6 literal right after "//" may use brackets;
			// elsewhere '[' starts BBCode or Markdown.
			if !inHost || j != bodyStart || bodyStart == start {
				return j
			}
			ipv6 = true
		case ']':
			if !inHost || !ipv6 {
				return j
			}
			ipv6 = false
		case '(':
			paren++
		case ')':
			if paren == 0 {
				return j
			}
			paren--
		case '{':
			brace++
		case '}':
			if brace == 0 {
				return j
			}
			brace--
		}
		// "url1,url2" / "url1;url2" / "url1|url2": split at the separator.
		if j > bodyStart && strings.IndexByte(",;|", text[j-1]) >= 0 {
			if _, _, ok := matchStart(text, j); ok {
				return j
			}
		}
		j += w
	}
	return j
}

// isTerminator reports whether r can never be part of a URL in free text.
func isTerminator(r rune) bool {
	if unicode.IsSpace(r) || unicode.IsControl(r) {
		return true
	}
	switch r {
	case '<', '>', '"', '`', '\\',
		'\u200b', '\u200c', '\u200d', '\u2060', '\ufeff', // zero-width chars
		'“', '”', '‘', '’', '«', '»', '「', '」', '『', '』', '【', '】',
		'〈', '〉', '《', '》', '（', '）', '、', '。', '，', '；', '：', '！', '？', '…':
		return true
	}
	return false
}

// trimTrailing removes sentence punctuation, quotes and unbalanced closing
// brackets from the end of a URL candidate.
func trimTrailing(s string) string {
	for s != "" {
		r, w := utf8.DecodeLastRuneInString(s)
		switch {
		case strings.ContainsRune(".,;:!?'\"*|", r):
		case r == ')' && strings.Count(s, "(") < strings.Count(s, ")"):
		case r == ']' && strings.Count(s, "[") < strings.Count(s, "]"):
		case r == '}' && strings.Count(s, "{") < strings.Count(s, "}"):
		default:
			return s
		}
		s = s[:len(s)-w]
	}
	return s
}

// validURL reports whether s parses as an http(s) URL with a plausible host.
func validURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.Contains(host, ":") { // IPv6 literal, validated by url.Parse
		return true
	}
	if host[0] == '.' || host[0] == '-' || strings.Contains(host, "..") {
		return false
	}
	for _, r := range host {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func isAlnum(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// hasPrefixFold is strings.HasPrefix ignoring ASCII case.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
