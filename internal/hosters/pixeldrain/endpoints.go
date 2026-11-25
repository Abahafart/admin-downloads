package pixeldrain

import (
	"net/url"
	"strings"
)

// Endpoints and API values (kept here because hosters change them).
const (
	defaultAPIBase = "https://pixeldrain.com/api"

	pathFileInfo = "/file/%s/info"
	pathFile     = "/file/%s"
	pathList     = "/list/%s"
	pathUser     = "/user"
)

// defaultDomains are the hostnames the plugin matches (www. is implied).
var defaultDomains = []string{"pixeldrain.com", "pixeldrain.net", "pixeldra.in"}

// apiError is the JSON body Pixeldrain returns on errors.
type apiError struct {
	Success *bool  `json:"success"`
	Value   string `json:"value"`
	Message string `json:"message"`
}

// fileInfo is the response of /api/file/<id>/info (and an entry of a list).
type fileInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	HashSHA256   string `json:"hash_sha256"`
	MimeType     string `json:"mime_type"`
	Availability string `json:"availability"`
	CanDownload  *bool  `json:"can_download"`
}

// listInfo is the response of /api/list/<id>.
type listInfo struct {
	Success bool       `json:"success"`
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Files   []fileInfo `json:"files"`
}

// userInfo is the response of /api/user (only the fields we use).
type userInfo struct {
	Username     string `json:"username"`
	Subscription struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		Type               string `json:"type"`
		MonthlyTransferCap int64  `json:"monthly_transfer_cap"`
	} `json:"subscription"`
	MonthlyTransferCap  int64 `json:"monthly_transfer_cap"`
	MonthlyTransferUsed int64 `json:"monthly_transfer_used"`
}

// availabilityOK reports whether an availability value allows downloading.
func availabilityOK(v string) bool { return v == "" || v == "ok" || v == "available" }

// apiPath joins base with a path that has already been escaped.
func apiPath(base, format, id string) string {
	return strings.TrimRight(base, "/") + strings.Replace(format, "%s", url.PathEscape(id), 1)
}
