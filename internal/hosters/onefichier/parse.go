package onefichier

import (
	"bytes"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Endpoints and page patterns (fragile: 1fichier changes its pages).
const (
	defaultWebBase = "https://1fichier.com"
	defaultAPIBase = "https://api.1fichier.com/v1"

	pathCheckLinks = "/check_links.pl"
	pathDir        = "/dir/"

	apiFileInfo = "/file/info.cgi"
	apiGetToken = "/download/get_token.cgi"
	apiUserInfo = "/user/info.cgi"
)

// mainDomain is the canonical domain; aliasDomains serve the same files.
const mainDomain = "1fichier.com"

var aliasDomains = []string{
	"alterupload.com", "cjoint.net", "desfichiers.com", "dfichiers.com", "megadl.fr",
	"mesfichiers.org", "piecejointe.net", "pjointe.com", "tenvoi.com", "dl4free.com",
}

var (
	waitRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)you must wait\s+(?:at least\s+|for\s+)?(\d+)\s*(minutes?|mins?|seconds?|secs?|s)\b`),
		regexp.MustCompile(`(?i)vous devez attendre\s+(?:encore\s+|au moins\s+)?(\d+)\s*(minutes?|mins?|secondes?|secs?|s)\b`),
	}
	countdownRe = regexp.MustCompile(`(?i)\bvar\s+ct\s*=\s*(\d+)\s*(\*\s*60)?`)
	oneAtATime  = regexp.MustCompile(`(?i)only download one file at a time|un seul fichier à la fois|one file at a time`)
	notFoundRe  = regexp.MustCompile(`(?i)the requested file (?:could not be found|has been deleted|does not exist)|file not found|le fichier demandé (?:n'existe pas|a été supprimé)|fichier introuvable|has been deleted following a notification`)
	linkTextRe  = regexp.MustCompile(`(?i)click here to download|cliquer ici pour télécharger|télécharger le fichier`)
)

// freePage is what Prepare learns from a 1fichier page.
type freePage struct {
	notFound bool
	password bool
	// wait is the advertised wait (0 = none).
	wait time.Duration
	// busy is set by the "one file at a time" warning (wait unknown).
	busy bool
	// link is the direct download link, if present.
	link string
	// form is the download form to post (nil if none).
	form *pageForm
}

type pageForm struct {
	action string
	values url.Values
}

// parseFreePage parses a file page. base resolves relative URLs.
func parseFreePage(html []byte, base *url.URL) (freePage, error) {
	var p freePage
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return p, err
	}
	text := doc.Text()
	if notFoundRe.MatchString(text) {
		p.notFound = true
		return p, nil
	}
	if doc.Find(`input[type=password][name=pass], input[type=password]`).Length() > 0 {
		p.password = true
	}
	for _, re := range waitRes {
		if m := re.FindStringSubmatch(text); m != nil {
			p.wait = unitDuration(m[1], m[2])
			break
		}
	}
	if p.wait == 0 {
		if m := countdownRe.FindSubmatch(html); m != nil {
			n, _ := strconv.Atoi(string(m[1]))
			d := time.Duration(n) * time.Second
			if len(m[2]) > 0 {
				d *= 60
			}
			p.wait = d
		}
	}
	if p.wait == 0 && oneAtATime.MatchString(text) {
		p.busy = true
	}

	// The download link: an anchor saying "click here to download" (or, as
	// a fallback, styled as the orange "ok" button) that points to a
	// download server, never to a site page.
	for pass := 0; pass < 2 && p.link == ""; pass++ {
		doc.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
			href, _ := a.Attr("href")
			cls, _ := a.Attr("class")
			if pass == 0 && !linkTextRe.MatchString(a.Text()) {
				return true
			}
			if pass == 1 && !strings.Contains(cls, "btn-orange") && !strings.Contains(cls, "ok") {
				return true
			}
			u, err := base.Parse(strings.TrimSpace(href))
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return true
			}
			host := strings.ToLower(u.Hostname())
			if strings.EqualFold(u.Host, base.Host) || host == mainDomain || host == "www."+mainDomain {
				return true
			}
			p.link = u.String()
			return false
		})
	}

	doc.Find("form").EachWithBreak(func(_ int, f *goquery.Selection) bool {
		method, _ := f.Attr("method")
		if !strings.EqualFold(method, "post") {
			return true
		}
		vals := url.Values{}
		f.Find("input[name]").Each(func(_ int, in *goquery.Selection) {
			name, _ := in.Attr("name")
			typ, _ := in.Attr("type")
			val, _ := in.Attr("value")
			switch strings.ToLower(typ) {
			case "checkbox", "radio":
				if _, checked := in.Attr("checked"); checked {
					vals.Add(name, val)
				}
			case "submit", "button", "image", "password", "file":
			default:
				vals.Add(name, val)
			}
		})
		if len(vals) == 0 {
			return true
		}
		action, _ := f.Attr("action")
		au, err := base.Parse(strings.TrimSpace(action))
		if err != nil {
			return true
		}
		p.form = &pageForm{action: au.String(), values: vals}
		return false
	})
	return p, nil
}

func unitDuration(n, unit string) time.Duration {
	v, _ := strconv.Atoi(n)
	if strings.HasPrefix(strings.ToLower(unit), "m") {
		return time.Duration(v) * time.Minute
	}
	return time.Duration(v) * time.Second
}

// checkLine is one line of check_links.pl: "url;name;size".
type checkLine struct {
	link     string
	name     string
	size     int64
	notFound bool
}

func parseCheckLinks(body []byte) []checkLine {
	var out []checkLine
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ";")
		if len(parts) < 3 {
			continue
		}
		// File names may contain ';': size is the last field, url the first.
		cl := checkLine{link: parts[0], name: strings.Join(parts[1:len(parts)-1], ";")}
		last := strings.TrimSpace(parts[len(parts)-1])
		if n, err := strconv.ParseInt(last, 10, 64); err == nil {
			cl.size = n
		} else {
			cl.notFound = true
		}
		out = append(out, cl)
	}
	return out
}

// dirEntry is an element of /dir/<id>?json=1.
type dirEntry struct {
	Link     string `json:"link"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// apiStatus is the common part of API answers.
type apiStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type apiFile struct {
	apiStatus
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Pass     int    `json:"pass"`
}

type apiToken struct {
	apiStatus
	URL string `json:"url"`
}
