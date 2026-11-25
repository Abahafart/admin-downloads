package gofile

import "regexp"

// Endpoints and scraping patterns (fragile: Gofile changes them).
const (
	defaultAPIBase  = "https://api.gofile.io"
	defaultSiteBase = "https://gofile.io"
	defaultJSPath   = "/dist/js/global.js"

	pathAccounts = "/accounts"       // POST: create guest account
	pathContents = "/contents/"      // GET /contents/<id>?wt=...
	pathGetID    = "/accounts/getid" // GET: id of the token's account
	pathAccount  = "/accounts/"      // GET /accounts/<id>

	cookieName = "accountToken"
)

// wtPatterns extract the website token from the site JS. The first
// capturing group is the token.
var wtPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bwt\s*[:=]\s*["']([A-Za-z0-9]{6,64})["']`),
	regexp.MustCompile(`websiteToken\s*[:=]\s*["']([A-Za-z0-9]{6,64})["']`),
	regexp.MustCompile(`["']X-Website-Token["']\s*[:,]\s*["']([A-Za-z0-9]{6,64})["']`),
}

// apiResponse is the envelope of every API answer.
type apiResponse[T any] struct {
	Status string `json:"status"` // "ok" or "error-..."
	Data   T      `json:"data"`
}

type accountData struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Tier  string `json:"tier"`
	Email string `json:"email"`
}

// content is a file or folder node of /contents/<id>.
type content struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "folder" | "file"
	Name     string             `json:"name"`
	Code     string             `json:"code"`
	Size     int64              `json:"size"`
	Link     string             `json:"link"`
	MD5      string             `json:"md5"`
	Password bool               `json:"password"`
	Children map[string]content `json:"children"`
	// PasswordStatus is set on protected folders
	// ("passwordRequired", "passwordWrong", "passwordOk").
	PasswordStatus string `json:"passwordStatus"`
}
