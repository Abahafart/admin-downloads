package core

import (
	"context"
	"errors"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/hosters"
)

// AccountPicker chooses the account a download uses. It is implemented by
// the accounts manager (wired in internal/app); nil means every download is
// anonymous.
type AccountPicker interface {
	// Pick returns the enabled account with the most quota for hoster, or
	// (nil, nil) to download anonymously.
	Pick(ctx context.Context, hoster string) (*accounts.Account, error)
	// ReportQuota records that the account ran out of quota; it should not
	// be picked again before retryAfter (0 = hoster default).
	ReportQuota(ctx context.Context, accountID string, retryAfter time.Duration)
}

// AccountLookup is optionally implemented by the AccountPicker: the engine
// uses it to rebuild the session of a finished download's account (for
// hosters.Cleaner).
type AccountLookup interface {
	Get(ctx context.Context, id string) (*accounts.Account, error)
}

// PostProcessInfo describes a finished download for the PostProcessor.
type PostProcessInfo struct {
	Download Download
	Package  Package
	// PackageComplete is true when every other file of the package is
	// completed too (multi-part archives can be extracted).
	PackageComplete bool
	// Checksum is the checksum to verify: DownloadSpec.Checksum from the
	// last Prepare, else the one stored with the download (nil = none).
	Checksum *hosters.Checksum
}

// ErrChecksumMismatch is wrapped by PostProcessor errors that mean the
// downloaded bytes are wrong: the engine then deletes the file and resets
// the progress so that a retry downloads it again.
var ErrChecksumMismatch = errors.New("core: checksum mismatch")

// PostProcessor verifies / extracts finished files (internal/postprocess,
// wired in internal/app). It is optional: without it the engine verifies
// sha256 / sha1 / md5 / crc32 checksums itself.
type PostProcessor interface {
	// Process runs after the file was renamed to path. A non-nil error
	// marks the download as failed with that message (wrap
	// ErrChecksumMismatch to also discard the file). If the engine stops
	// (or the download is paused) while Process runs, it is called again
	// for the same file when the download resumes, so it must be
	// idempotent.
	Process(ctx context.Context, info PostProcessInfo, path string) error
}
