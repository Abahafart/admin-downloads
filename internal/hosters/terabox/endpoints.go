package terabox

// Everything that depends on TeraBox's web front-end lives in this file:
// domains, paths, query parameters, HTML patterns and errno meanings. When
// the site changes, this is the file to update (then refresh the fixtures in
// testdata/terabox and run `adesc selftest terabox`).
//
// Status: authored from public descriptions of the web client
// (2024-2026); not yet verified against the live site from this code base.

import (
	"net/url"
	"regexp"
	"strconv"

	"admindescargas/internal/hosters"
)

// DefaultDomains are the TeraBox mirror domains matched by default
// ([hosters.terabox].domains extends the list; option replace_domains=true
// replaces it). Subdomains (www., d., data. ...) match too.
var DefaultDomains = []string{
	"terabox.com", "terabox.app", "terabox.fun", "teraboxapp.com",
	"1024terabox.com", "1024tera.com", "freeterabox.com",
	"4funbox.com", "4funbox.co", "mirrobox.com", "nephobox.com",
	"momerybox.com", "tibibox.com", "gibibox.com",
	"teraboxlink.com", "terasharelink.com", "terafileshare.com",
	"teraboxshare.com",
}

// Link forms (surl is the share id without the leading "1" of /s/1xxxx).
const (
	pathShortPrefix = "/s/"                 // /s/1<surl>
	pathSharingLink = "/sharing/link"       // ?surl=<surl>
	pathSharingEmbd = "/sharing/embed"      // ?surl=<surl>
	pathWapFileList = "/wap/share/filelist" // ?surl=<surl>
	pathWapHome     = "/wap/share/home"     // ?surl=<surl>
	querySURL       = "surl"
)

// API endpoints (relative to the share page host after redirects).
const (
	pathShortURLInfo = "/api/shorturlinfo"
	pathShareList    = "/share/list"
	pathCheckLogin   = "/api/check/login"
	pathMembership   = "/rest/2.0/membership/proxy/user"
)

// Fixed query parameters sent by the web client.
const (
	paramAppID      = "250528"
	paramWeb        = "1"
	paramChannel    = "dubox"
	paramClientType = "0"
)

// Cookies.
const (
	cookieLogin     = "ndus"      // session cookie of a logged-in account
	cookieBrowserID = "browserid" // set by the site; kept by the jar
)

// extraCookies are sent with every request (English error messages).
var extraCookies = map[string]string{"lang": "en"}

// UserAgent is sent on page, API and download requests; TeraBox rejects
// unusual agents on some endpoints.
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Paging.
const (
	defaultPageSize = 100
	maxPages        = 1000 // per directory
	maxDepth        = 64   // directory nesting
)

// jsTokenPatterns extract the anti-CSRF token from the share page. The page
// usually embeds it url-encoded, e.g.
// decodeURIComponent(`%28function%28%29%7Bwindow.jsToken%20%3D%20a%7D%3Bfn%28%22<TOKEN>%22%29`)
var jsTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`fn%28%22([0-9A-Za-z]+)%22%29`),
	regexp.MustCompile(`fn\(\s*["']([0-9A-Za-z]+)["']\s*\)`),
	regexp.MustCompile(`"jsToken"\s*:\s*"([0-9A-Za-z]+)"`),
	regexp.MustCompile(`jsToken\s*=\s*["']([0-9A-Za-z]+)["']`),
}

// logIDPatterns extract dp-logid (any URL in the page carries it).
var logIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`dp-logid=([0-9]+)`),
	regexp.MustCompile(`"logid"\s*:\s*"?([0-9]+)`),
}

// bdstokenPatterns extract bdstoken (only used by account endpoints).
var bdstokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"bdstoken"\s*:\s*"([0-9a-fA-F]+)"`),
	regexp.MustCompile(`bdstoken\s*=\s*["']([0-9a-fA-F]+)["']`),
}

// browserIDPatterns extract browserid when the page embeds it instead of
// setting the cookie.
var browserIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"browserid"\s*:\s*"([0-9A-Za-z_\-=]+)"`),
}

// sharePagePath returns the canonical share page path for surl.
func sharePagePath(surl string) string {
	return pathSharingLink + "?" + url.Values{querySURL: {surl}}.Encode()
}

// commonParams are the parameters shared by API calls.
func commonParams(t tokens) url.Values {
	v := url.Values{
		"app_id":     {paramAppID},
		"web":        {paramWeb},
		"channel":    {paramChannel},
		"clienttype": {paramClientType},
	}
	if t.JSToken != "" {
		v.Set("jsToken", t.JSToken)
	}
	if t.LogID != "" {
		v.Set("dp-logid", t.LogID)
	}
	return v
}

// shortURLInfoParams: /api/shorturlinfo uses the share id WITH the leading
// "1" (as in /s/1xxxx).
func shortURLInfoParams(t tokens, surl string) url.Values {
	v := commonParams(t)
	v.Set("shorturl", "1"+surl)
	v.Set("root", "1")
	return v
}

// shareListParams: /share/list uses the share id WITHOUT the leading "1".
// dir == "" lists the share root.
func shareListParams(t tokens, surl, dir string, page, num int) url.Values {
	v := commonParams(t)
	v.Set("page", strconv.Itoa(page))
	v.Set("num", strconv.Itoa(num))
	v.Set("by", "name")
	v.Set("order", "asc")
	v.Set("shorturl", surl)
	if dir == "" {
		v.Set("root", "1")
	} else {
		v.Set("dir", dir)
	}
	return v
}

// errnoRule tells what an errno means.
type errnoRule struct {
	kind  hosters.Kind
	scope hosters.QuotaScope // for KindQuota
	msg   string
	// refresh: the page tokens are probably stale; reload the share page
	// and retry once.
	refresh bool
}

// errnoRules maps errno values seen in TeraBox / Baidu-derived APIs.
// Unknown values fall back to errmsgRules and then to Fatal.
var errnoRules = map[int]errnoRule{
	-6:      {kind: hosters.KindNeedsAuth, msg: "login required or cookie expired (add a TeraBox account with the ndus cookie)"},
	9019:    {kind: hosters.KindNeedsAuth, msg: "login required (add a TeraBox account with the ndus cookie)"},
	9013:    {kind: hosters.KindNeedsAuth, msg: "login required"},
	4000023: {kind: hosters.KindNeedsAuth, msg: "login required"},
	2:       {kind: hosters.KindTemporary, msg: "invalid parameters (stale jsToken?)", refresh: true},
	4000020: {kind: hosters.KindTemporary, msg: "invalid jsToken", refresh: true},
	-9:      {kind: hosters.KindNotFound, msg: "file or share not found"},
	-7:      {kind: hosters.KindNotFound, msg: "share not accessible"},
	105:     {kind: hosters.KindNotFound, msg: "share link does not exist"},
	115:     {kind: hosters.KindNotFound, msg: "file blocked by TeraBox"},
	117:     {kind: hosters.KindNotFound, msg: "share expired"},
	145:     {kind: hosters.KindNotFound, msg: "share canceled by its owner"},
	31066:   {kind: hosters.KindNotFound, msg: "file does not exist"},
	-12:     {kind: hosters.KindFatal, msg: "password-protected shares are not supported yet"},
	-62:     {kind: hosters.KindQuota, scope: hosters.ScopeIP, msg: "verification required (too many requests from this IP)"},
	400141:  {kind: hosters.KindQuota, scope: hosters.ScopeIP, msg: "verification required (captcha)"},
	400210:  {kind: hosters.KindQuota, scope: hosters.ScopeIP, msg: "verification required"},
	31034:   {kind: hosters.KindQuota, scope: hosters.ScopeIP, msg: "requests too frequent"},
	31045:   {kind: hosters.KindQuota, scope: hosters.ScopeAccount, msg: "account download limit reached"},
	-1:      {kind: hosters.KindTemporary, msg: "server error"},
	31062:   {kind: hosters.KindTemporary, msg: "server busy"},
}

// errmsgRules classify unknown errnos by their message text.
var errmsgRules = []struct {
	re   *regexp.Regexp
	rule errnoRule
}{
	{regexp.MustCompile(`(?i)login|sign in|not logged`), errnoRule{kind: hosters.KindNeedsAuth, msg: "login required"}},
	{regexp.MustCompile(`(?i)verif|captcha`), errnoRule{kind: hosters.KindQuota, scope: hosters.ScopeIP, msg: "verification required"}},
	{regexp.MustCompile(`(?i)frequen|too many|limit`), errnoRule{kind: hosters.KindQuota, scope: hosters.ScopeIP, msg: "too many requests"}},
	{regexp.MustCompile(`(?i)not exist|not found|expired|deleted|cancel`), errnoRule{kind: hosters.KindNotFound, msg: "share not found"}},
	{regexp.MustCompile(`(?i)param`), errnoRule{kind: hosters.KindTemporary, msg: "invalid parameters", refresh: true}},
}
