// Package hosters defines the plugin contract implemented by every hoster
// (MediaFire, MEGA, Google Drive, TeraBox, generic HTTP/FTP, ...) and the
// Registry used to find the plugin for a link.
//
// Plugins live in sub-packages (internal/hosters/<name>) and register a
// Factory from an init function:
//
//	func init() { hosters.RegisterFactory("mega", New) }
//	func New(cfg config.HosterConfig) (hosters.Hoster, error) { ... }
//
// internal/hosters/all blank-imports every plugin package; the app builds a
// Registry with NewRegistryFromFactories. Keep HTML selectors / API
// parameters of fragile hosters in a dedicated endpoints.go file, record
// fixtures under testdata/, and put live tests behind //go:build live.
//
// All HTTP must use Session.Client (which routes through the session's
// egress). Never log secrets, cookies or full signed URLs.
package hosters

import (
	"context"
	"crypto/cipher"
	"net/http"
	"net/url"
	"time"

	"admindescargas/internal/accounts"
)

// Hoster is implemented by each plugin. Implementations must be safe for
// concurrent use; per-identity state belongs in the Session (Value/SetValue),
// not in the Hoster.
type Hoster interface {
	// Name is the stable lowercase identifier ("mega", "mediafire", "gdrive",
	// "terabox", "generic", ...). It is stored in SQLite and used as the
	// config key [hosters.<name>].
	Name() string
	// Match reports whether the plugin handles u. It must be cheap and must
	// not do I/O; it is used by the clipboard monitor on every URL.
	Match(u *url.URL) bool
	// Resolve expands a link into the files it points to. A file link yields
	// one FileInfo with empty RelPath; a folder link yields every file in
	// the tree, each with RelPath set, and Result.Name = folder name.
	Resolve(ctx context.Context, s *Session, link string) (*ResolveResult, error)
	// Prepare turns a FileInfo (as returned by Resolve, possibly reloaded
	// from SQLite) into a direct download. It is called again whenever the
	// link expires, the egress changes or the download resumes.
	Prepare(ctx context.Context, s *Session, f FileInfo) (*DownloadSpec, error)
	// Classify maps a response (or transport error) obtained from a
	// DownloadSpec or a hoster API call to an outcome: nil means OK,
	// otherwise a *Error (use the New* constructors). body holds at most the
	// first few KiB of the response (it may be nil). resp may be nil when
	// err != nil. Plugins usually fall back to DefaultClassify.
	Classify(resp *http.Response, body []byte, err error) error
	// Caps describes limits and features of the hoster.
	Caps() Caps
}

// ResolveResult is the output of Resolve.
type ResolveResult struct {
	// Name is the package name: the shared folder name for folders, empty
	// for single files (the engine then uses the file name).
	Name string
	// IsFolder is true when the link points to a folder (even an empty one).
	IsFolder bool
	// Files are the downloadable files. For folders RelPath is the path
	// inside the folder including the file name (e.g. "S1/ep01.mkv").
	Files []FileInfo
}

// FileInfo describes one remote file. It is persisted in the downloads and
// linkgrabber_files tables, so everything needed by Prepare must be in it
// (use Meta for plugin-private values such as keys or internal ids).
type FileInfo struct {
	// ID is the hoster's id for the file (MEGA handle, Drive file id, ...).
	// Together with the hoster name it identifies the file for dedupe and
	// rescans. May be empty for generic links.
	ID   string `json:"id"`
	Name string `json:"name"`
	// RelPath is the path inside the shared folder, slash-separated and
	// including Name (e.g. "Temporada 1/ep01.mkv"); empty for a loose file.
	// It is untrusted: the engine sanitizes it with fsutil.SafeJoin.
	RelPath string `json:"rel_path,omitempty"`
	// Size in bytes, -1 if unknown.
	Size     int64     `json:"size"`
	Checksum *Checksum `json:"checksum,omitempty"`
	// Link is the file's own URL, used to re-resolve it on resume.
	Link string `json:"link"`
	// Meta holds plugin-private, non-secret values needed by Prepare (stored
	// as JSON). Example: MEGA node key, TeraBox fs_id.
	Meta map[string]string `json:"meta,omitempty"`
}

// Checksum algorithms.
const (
	AlgoSHA256  = "sha256"
	AlgoSHA1    = "sha1"
	AlgoMD5     = "md5"
	AlgoCRC32   = "crc32"
	AlgoMEGAMAC = "mega-mac" // MEGA condensed chunk MAC (needs the node key)
)

// Checksum is a hash advertised by the hoster.
type Checksum struct {
	Algo  string `json:"algo"`
	Value string `json:"value"` // lowercase hex (base64 for mega-mac)
}

// DownloadSpec is a ready-to-fetch direct download produced by Prepare.
type DownloadSpec struct {
	URL string
	// Method defaults to GET.
	Method string
	// Header is added to every request (cookies go through Session.Jar).
	Header http.Header
	// Client overrides Session.Client (e.g. an oauth2 transport for Drive).
	// It must still route through the session egress.
	Client *http.Client
	// Filename, if set, overrides FileInfo.Name (e.g. from the hoster API).
	Filename string
	// Size in bytes, -1 if unknown (the downloader then uses Content-Length).
	Size int64
	// Resumable tells whether Range requests are supported.
	Resumable bool
	// MaxConns caps the number of parallel segments (0 = Caps default).
	MaxConns int
	// Expires is when URL stops working (zero = unknown). On 403/410 or after
	// Expires the downloader calls Prepare again and continues at the offset.
	Expires time.Time
	// Decryptor, if non-nil, returns a keystream positioned at the absolute
	// file offset (MEGA AES-128-CTR). The downloader XORs the body with it.
	Decryptor func(offset int64) (cipher.Stream, error)
	// Checksum to verify after download (overrides FileInfo.Checksum).
	Checksum *Checksum
}

// Caps describes the limits and features of a hoster.
type Caps struct {
	// MaxConnsPerFile caps segments per file (0 = downloader default).
	MaxConnsPerFile int
	// MaxParallel caps simultaneous downloads per egress (0 = config).
	MaxParallel int
	// Resumable is true if downloads can usually resume (Range supported).
	Resumable bool
	// Folders is true if Resolve can expand folder links.
	Folders bool
	// AccountKinds lists the supported account kinds (empty = none).
	AccountKinds []accounts.Kind
	// AccountRequired is true if anonymous downloads are not possible.
	AccountRequired bool
	// DefaultRetryAfter is used for quotas without explicit duration when
	// config has no [hosters.<name>].retry_after.
	DefaultRetryAfter time.Duration
	// QuotaScope is the usual scope of this hoster's quota (IP for MEGA,
	// File for Drive): the rotation manager uses it to know whether
	// rotating the IP can help before even trying.
	QuotaScope QuotaScope
	// Fallback marks catch-all plugins (generic HTTP/FTP): the registry only
	// tries them after every specific plugin.
	Fallback bool
}

// StreamingResolver is optionally implemented by hosters whose folder
// listings are slow: the linkgrabber uses it to show the tree while it is
// being listed. emit is called once per file (from the calling goroutine);
// the returned result must also contain every emitted file.
type StreamingResolver interface {
	ResolveStream(ctx context.Context, s *Session, link string, emit func(FileInfo)) (*ResolveResult, error)
}

// AccountChecker is optionally implemented by hosters supporting accounts:
// it logs in with s.Account / s.Secret and reports status and quota.
type AccountChecker interface {
	CheckAccount(ctx context.Context, s *Session) (*accounts.CheckResult, error)
}

// Cleaner is optionally implemented by hosters that create remote
// resources while preparing a download (Google Drive copies made to work
// around a per-file quota). The engine calls Cleanup, in the background and
// with a session of the same account, once the download ended for good:
// completed, failed (needs a user action) or deleted. Errors are only
// logged.
type Cleaner interface {
	Cleanup(ctx context.Context, s *Session, f FileInfo) error
}

// FileQuotaBypasser is optionally implemented by hosters that can work
// around a Quota{ScopeFile} answer with this session (Google Drive with an
// OAuth account and copy_on_quota). When it returns true the engine calls
// Prepare again immediately instead of waiting.
type FileQuotaBypasser interface {
	CanBypassFileQuota(s *Session) bool
}

// SelfTester is optionally implemented by fragile plugins; `adesc selftest
// <hoster>` runs it against known public links to detect breakage early.
type SelfTester interface {
	SelfTest(ctx context.Context, s *Session) error
}
