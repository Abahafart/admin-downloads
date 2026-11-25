package dropbox

// Endpoints and API values.
const (
	defaultAPIBase     = "https://api.dropboxapi.com"
	defaultContentBase = "https://content.dropboxapi.com"

	pathListFolder         = "/2/files/list_folder"
	pathListFolderContinue = "/2/files/list_folder/continue"
	pathSharedLinkFile     = "/2/sharing/get_shared_link_file"
	pathCurrentAccount     = "/2/users/get_current_account"
	pathSharedLinkMeta     = "/2/sharing/get_shared_link_metadata"
)

// hosts are the hostnames of shared links.
var hosts = map[string]bool{
	"dropbox.com":               true,
	"www.dropbox.com":           true,
	"dl.dropbox.com":            true,
	"dl.dropboxusercontent.com": true,
	"db.tt":                     true,
}

// Phrases of the web error pages (lower case).
var (
	trafficPhrases  = []string{"too much traffic", "temporarily disabled", "generating too much", "link has been disabled"}
	passwordPhrases = []string{"password protected", "password-protected", "enter the password"}
	tooLargePhrases = []string{"too large to download", "zip_too_large", "too many files"}
)

type listFolderArg struct {
	Path       string     `json:"path"`
	SharedLink sharedLink `json:"shared_link"`
	Limit      int        `json:"limit,omitempty"`
}

type sharedLink struct {
	URL string `json:"url"`
}

type listFolderResult struct {
	Entries []entry `json:"entries"`
	Cursor  string  `json:"cursor"`
	HasMore bool    `json:"has_more"`
}

type entry struct {
	Tag  string `json:".tag"` // file | folder | deleted
	Name string `json:"name"`
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

type apiError struct {
	ErrorSummary string `json:"error_summary"`
}

type currentAccount struct {
	Email       string `json:"email"`
	AccountType struct {
		Tag string `json:".tag"`
	} `json:"account_type"`
}
