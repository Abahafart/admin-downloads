package gdrive

import (
	"regexp"
	"time"

	"golang.org/x/oauth2/google"
)

// Everything that depends on Google's HTML or API shape lives here.

const (
	defaultAPIBase   = "https://www.googleapis.com"            // + /drive/v3/
	defaultUCBase    = "https://drive.usercontent.google.com"  // anonymous downloads
	defaultDriveBase = "https://drive.google.com"              // view pages, folder views
	defaultDocsBase  = "https://docs.google.com"               // Docs/Sheets/Slides exports
	loginHost        = "accounts.google.com"                   // private files redirect here
	driveScope       = "https://www.googleapis.com/auth/drive" // needed by files.copy

	userAgent    = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
	maxPageBytes = 4 << 20

	defaultRPS        = 8.0
	defaultRetryAfter = time.Hour // per-file quota: usually lifted within 24 h
	maxFolderDepth    = 64
	maxFormHops       = 4

	// appPropCopyOf tags the temporary copies made by the quota workaround
	// (appProperties are private to our OAuth client), so they can be found
	// and deleted even after a restart.
	appPropCopyOf = "adescCopyOf"
	copyDesc      = "Temporary copy made by AdminDescargas to bypass the download quota; safe to delete."

	// Fields requested from the API.
	fileFields = "id,name,mimeType,size,md5Checksum,resourceKey,driveId,trashed,exportLinks,shortcutDetails(targetId,targetMimeType,targetResourceKey)"
	listFields = "nextPageToken,files(id,name,mimeType,size,md5Checksum,resourceKey,shortcutDetails(targetId,targetMimeType,targetResourceKey))"

	headerResourceKeys = "X-Goog-Drive-Resource-Keys"
	headerAPIKey       = "X-Goog-Api-Key" //nolint:gosec // G101: header name, not a credential
)

// defaultOAuthEndpoint is Google's OAuth endpoint (overridden by tests).
var defaultOAuthEndpoint = google.Endpoint

// Google mime types.
const (
	mimeFolder   = "application/vnd.google-apps.folder"
	mimeShortcut = "application/vnd.google-apps.shortcut"
	mimeAppsPfx  = "application/vnd.google-apps."
)

// docTypes maps the docs.google.com path segment to the Google mime type.
var docTypes = map[string]string{
	"document":     "application/vnd.google-apps.document",
	"spreadsheets": "application/vnd.google-apps.spreadsheet",
	"presentation": "application/vnd.google-apps.presentation",
	"drawings":     "application/vnd.google-apps.drawing",
}

// defaultExport is the export format per doc type.
var defaultExport = map[string]string{
	"document":     "docx",
	"spreadsheets": "xlsx",
	"presentation": "pptx",
	"drawings":     "pdf",
}

// exportFormats lists the valid formats per doc type.
var exportFormats = map[string][]string{
	"document":     {"docx", "pdf", "odt", "rtf", "txt", "epub", "md", "zip"},
	"spreadsheets": {"xlsx", "pdf", "ods", "csv", "tsv", "zip"},
	"presentation": {"pptx", "pdf", "odp", "txt"},
	"drawings":     {"pdf", "png", "jpg", "svg"},
}

// exportMime maps an export format to the mime type used by the API.
var exportMime = map[string]string{
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"pdf":  "application/pdf",
	"odt":  "application/vnd.oasis.opendocument.text",
	"ods":  "application/vnd.oasis.opendocument.spreadsheet",
	"odp":  "application/vnd.oasis.opendocument.presentation",
	"rtf":  "application/rtf",
	"txt":  "text/plain",
	"csv":  "text/csv",
	"tsv":  "text/tab-separated-values",
	"epub": "application/epub+zip",
	"md":   "text/markdown",
	"zip":  "application/zip",
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"svg":  "image/svg+xml",
}

var (
	reID        = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}$`)
	reSizeRange = regexp.MustCompile(`/(\d+)\s*$`)
)

// Lowercase markers searched in HTML pages.
var (
	markersQuota = []string{
		"too many users have viewed or downloaded this file recently",
		"download quota for this file has been exceeded",
		"quota exceeded",
		"cuota de descarga",
		"se ha superado la cuota",
	}
	markersNotFound = []string{
		"the file you have requested does not exist",
		"sorry, the file you have requested does not exist",
		"el archivo que has solicitado no existe",
		"file is in the owner's trash",
	}
	markersLogin = []string{
		"accounts.google.com/servicelogin",
		"accounts.google.com/v3/signin",
		"you need access",
		"necesitas acceso",
		"request access",
	}
	markersBlocked = []string{
		"our systems have detected unusual traffic",
		"automated queries",
	}
)
