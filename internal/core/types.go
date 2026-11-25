package core

import (
	"time"

	"admindescargas/internal/hosters"
)

// State is the lifecycle state of a download (PLAN.md section 3):
//
//	queued -> resolving -> waiting(countdown) -> downloading -> verifying
//	       -> postprocessing -> completed | failed | paused
type State string

// Download states. These strings are stored in downloads.state and sent to
// the UI unchanged.
const (
	StateQueued         State = "queued"
	StateResolving      State = "resolving"
	StateWaiting        State = "waiting"
	StateDownloading    State = "downloading"
	StateVerifying      State = "verifying"
	StatePostprocessing State = "postprocessing"
	StateCompleted      State = "completed"
	StateFailed         State = "failed"
	StatePaused         State = "paused"
)

// AllStates lists every state in lifecycle order.
var AllStates = []State{
	StateQueued, StateResolving, StateWaiting, StateDownloading, StateVerifying,
	StatePostprocessing, StateCompleted, StateFailed, StatePaused,
}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	for _, v := range AllStates {
		if v == s {
			return true
		}
	}
	return false
}

// Active reports whether the download is being worked on right now.
func (s State) Active() bool {
	switch s {
	case StateResolving, StateWaiting, StateDownloading, StateVerifying, StatePostprocessing:
		return true
	}
	return false
}

// Terminal reports whether the download needs a user action to run again.
func (s State) Terminal() bool { return s == StateCompleted || s == StateFailed }

// Download is the API view of one file transfer.
type Download struct {
	ID        int64  `json:"id"`
	PackageID int64  `json:"package_id"`
	Hoster    string `json:"hoster"`
	// URL is the file's own link (FileInfo.Link).
	URL    string `json:"url"`
	FileID string `json:"file_id,omitempty"`
	Name   string `json:"name"`
	// RelPath is the sanitized path inside the package (slash separated).
	RelPath string `json:"rel_path,omitempty"`
	// DestPath is the absolute final path on disk.
	DestPath string `json:"dest_path"`
	State    State  `json:"state"`
	// Size is the remote size in bytes, -1 if unknown.
	Size int64 `json:"size"`
	// Done is the number of bytes safely on disk.
	Done int64 `json:"done"`
	// Speed in bytes/second (moving average); 0 when not downloading.
	Speed int64 `json:"speed"`
	// ETA in seconds, -1 if unknown.
	ETA         int64 `json:"eta"`
	Connections int   `json:"connections"`
	// Resumable is false when the server ignores Range (UI shows "no
	// reanudable"); nil while unknown.
	Resumable *bool             `json:"resumable,omitempty"`
	Checksum  *hosters.Checksum `json:"checksum,omitempty"`
	// Error is a user-facing message for failed/waiting downloads and
	// ErrorKind its hosters.Kind string ("quota", "not_found", ...).
	Error     string `json:"error,omitempty"`
	ErrorKind string `json:"error_kind,omitempty"`
	// QuotaScope is "ip", "account" or "file" when the download waits on
	// (or failed with) a quota: with "file" rotating the IP does not help.
	QuotaScope string `json:"quota_scope,omitempty"`
	Attempts   int    `json:"attempts"`
	// EgressID / PublicIP identify the network exit in use (or last used).
	EgressID  string `json:"egress_id,omitempty"`
	PublicIP  string `json:"public_ip,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	// WaitingUntil is set in StateWaiting: the countdown target.
	WaitingUntil time.Time `json:"waiting_until,omitzero"`
	Priority     int       `json:"priority"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitzero"`
	CompletedAt  time.Time `json:"completed_at,omitzero"`
}

// Package groups downloads: one per shared folder or per batch of links.
type Package struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// DestDir is the absolute directory where the package's files go
	// (<download_dir>/<sanitized name> for folders).
	DestDir   string `json:"dest_dir"`
	SourceURL string `json:"source_url,omitempty"` // folder link, for rescans
	Hoster    string `json:"hoster,omitempty"`
	IsFolder  bool   `json:"is_folder"`
	// State is the aggregate state: downloading if any file is active,
	// waiting/paused/failed/completed if all files agree, else queued.
	State        State     `json:"state"`
	Size         int64     `json:"size"` // sum of known sizes
	Done         int64     `json:"done"`
	Speed        int64     `json:"speed"`
	ETA          int64     `json:"eta"` // seconds, -1 unknown
	FilesTotal   int       `json:"files_total"`
	FilesDone    int       `json:"files_done"`
	FilesFail    int       `json:"files_failed"`
	Priority     int       `json:"priority"`
	ExistsPolicy string    `json:"exists_policy"` // config.ExistsPolicy value
	CreatedAt    time.Time `json:"created_at"`
}

// GrabberState is the state of a LinkGrabber entry.
type GrabberState string

// LinkGrabber entry states.
const (
	GrabberPending     GrabberState = "pending"     // queued for Resolve
	GrabberResolving   GrabberState = "resolving"   // listing in progress (Files grows)
	GrabberReady       GrabberState = "ready"       // can be confirmed
	GrabberError       GrabberState = "error"       // Resolve failed (Error set)
	GrabberUnsupported GrabberState = "unsupported" // no hoster matched
)

// GrabberEntry is one link pasted into the LinkGrabber with its expansion.
type GrabberEntry struct {
	ID     int64        `json:"id"`
	URL    string       `json:"url"`
	Hoster string       `json:"hoster,omitempty"`
	State  GrabberState `json:"state"`
	Error  string       `json:"error,omitempty"`
	// PackageName is the proposed package name (folder name or file name).
	PackageName string        `json:"package_name"`
	IsFolder    bool          `json:"is_folder"`
	Files       []GrabberFile `json:"files"`
	TotalSize   int64         `json:"total_size"` // -1 if any size unknown
	// Source tells where the link came from (api, cli, clipboard, extension).
	Source  string    `json:"source,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// GrabberFile is one resolved file of an entry.
type GrabberFile struct {
	Index    int               `json:"index"` // stable within the entry
	Name     string            `json:"name"`
	RelPath  string            `json:"rel_path,omitempty"`
	Size     int64             `json:"size"`
	Checksum *hosters.Checksum `json:"checksum,omitempty"`
	// Known is true if the file is already queued or in history.
	Known bool `json:"known,omitempty"`
}

// AddLinksRequest adds links to the LinkGrabber.
type AddLinksRequest struct {
	// Text is free text; every URL in it is extracted (clipboard, stdin).
	Text string `json:"text,omitempty"`
	// URLs are explicit links (added in addition to those found in Text).
	URLs []string `json:"urls,omitempty"`
	// Source is informational: api | cli | clipboard | extension | desktop.
	Source string `json:"source,omitempty"`
	// AutoConfirm skips the LinkGrabber: entries are confirmed (all files,
	// default destination) as soon as they resolve.
	AutoConfirm bool `json:"auto_confirm,omitempty"`
	// DestDir and PackageName apply when AutoConfirm is set.
	DestDir     string `json:"dest_dir,omitempty"`
	PackageName string `json:"package_name,omitempty"`
}

// ConfirmRequest moves LinkGrabber entries to the download queue.
type ConfirmRequest struct {
	Items []ConfirmItem `json:"items"`
	// DestDir is the base directory ("" = config download_dir). Folder
	// entries are saved under DestDir/<package name>/<rel_path>.
	DestDir string `json:"dest_dir,omitempty"`
	// ExistsPolicy overrides config downloads.exists_policy ("" = config).
	ExistsPolicy string `json:"exists_policy,omitempty"`
	// Paused adds the downloads in StatePaused instead of StateQueued.
	Paused   bool `json:"paused,omitempty"`
	Priority int  `json:"priority,omitempty"`
}

// ConfirmItem selects what to take from one entry.
type ConfirmItem struct {
	EntryID int64 `json:"entry_id"`
	// PackageName overrides the proposed name ("" = keep).
	PackageName string `json:"package_name,omitempty"`
	// Include lists RelPaths of files or folder prefixes ("S1/") to take;
	// empty = everything. Exclude is applied after Include. Matching is on
	// the sanitized slash-separated RelPath; for single-file entries use
	// FileIndexes instead (or nothing).
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// FileIndexes, if non-empty, selects files by GrabberFile.Index and
	// takes precedence over Include/Exclude.
	FileIndexes []int `json:"file_indexes,omitempty"`
}

// DownloadFilter narrows ListDownloads. Zero value = everything.
type DownloadFilter struct {
	PackageID int64   `json:"package_id,omitempty"`
	States    []State `json:"states,omitempty"`
	Hoster    string  `json:"hoster,omitempty"`
}

// Stats is a global snapshot for the tray and the status bar.
type Stats struct {
	Speed       int64 `json:"speed"` // total bytes/second
	Active      int   `json:"active"`
	Queued      int   `json:"queued"`
	Waiting     int   `json:"waiting"`
	Failed      int   `json:"failed"`
	Completed   int   `json:"completed"`
	GrabberSize int   `json:"grabber_size"`
	// Paused is true after PauseAll until ResumeAll.
	Paused bool `json:"paused"`
	// Rotating is true while a global IP rotation (router / system VPN)
	// holds every transfer; they continue by themselves afterwards.
	Rotating bool `json:"rotating,omitempty"`
}
