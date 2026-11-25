// Package core defines the Engine: the queue, scheduler and state machine
// that every front-end (REST API, CLI through the API, desktop app) drives.
//
// This file is the contract only. The implementation (engine agent) lives in
// other files of this package and is constructed by internal/app. The API
// maps the sentinel errors below to HTTP status codes:
//
//	ErrNotFound        -> 404
//	ErrInvalidArgument -> 400
//	ErrInvalidState    -> 409
package core

import (
	"context"
	"errors"
)

// Sentinel errors returned (wrapped) by Engine methods.
var (
	ErrNotFound        = errors.New("core: not found")
	ErrInvalidArgument = errors.New("core: invalid argument")
	ErrInvalidState    = errors.New("core: invalid state for this action")
	ErrStopped         = errors.New("core: engine stopped")
)

// Engine is the API of the download core. All methods are safe for
// concurrent use. Methods taking ids apply to all of them and fail with
// ErrNotFound (wrapped, naming the id) if any id is unknown, before changing
// anything.
type Engine interface {
	// Start loads state from the store, re-queues downloads that were
	// active when the process stopped (if downloads.resume_on_start) and
	// starts the scheduler. It returns once the engine is running.
	Start(ctx context.Context) error
	// Stop pauses transfers (persisting offsets after fsync) and returns
	// when everything is flushed or ctx expires. Downloads keep their state
	// so the next Start can resume them.
	Stop(ctx context.Context) error

	// --- LinkGrabber ---

	// AddLinks extracts URLs, deduplicates them against the LinkGrabber
	// (and, for clipboard sources, the queue and history) and starts
	// resolving them in the background. It returns the new entries
	// immediately (State pending/unsupported); progress arrives as
	// EventGrabber* events.
	AddLinks(ctx context.Context, req AddLinksRequest) ([]GrabberEntry, error)
	// ListLinkGrabber returns every entry, oldest first.
	ListLinkGrabber(ctx context.Context) ([]GrabberEntry, error)
	// ConfirmLinkGrabber creates packages/downloads from the selected files
	// and removes the confirmed entries from the LinkGrabber.
	ConfirmLinkGrabber(ctx context.Context, req ConfirmRequest) ([]Package, error)
	// RemoveLinkGrabber discards entries without downloading them.
	RemoveLinkGrabber(ctx context.Context, entryIDs []int64) error

	// --- Downloads ---

	ListDownloads(ctx context.Context, f DownloadFilter) ([]Download, error)
	GetDownload(ctx context.Context, id int64) (Download, error)
	// PauseDownloads stops transfers keeping their progress.
	PauseDownloads(ctx context.Context, ids []int64) error
	// ResumeDownloads re-queues paused (or waiting) downloads; they continue
	// from the saved offset.
	ResumeDownloads(ctx context.Context, ids []int64) error
	// RestartDownloads deletes the .part file and progress and downloads
	// again from zero.
	RestartDownloads(ctx context.Context, ids []int64) error
	// RetryDownloads re-queues failed downloads (resetting the attempt
	// counter), resuming from the offset when possible.
	RetryDownloads(ctx context.Context, ids []int64) error
	// DeleteDownloads removes downloads from the queue; with deleteFiles it
	// also removes the .part / completed files from disk.
	DeleteDownloads(ctx context.Context, ids []int64, deleteFiles bool) error
	// RetryAllFailed is RetryDownloads for every failed download.
	RetryAllFailed(ctx context.Context) error

	// --- Packages (apply to every download of the package) ---

	ListPackages(ctx context.Context) ([]Package, error)
	GetPackage(ctx context.Context, id int64) (Package, []Download, error)
	PausePackages(ctx context.Context, ids []int64) error
	ResumePackages(ctx context.Context, ids []int64) error
	RestartPackages(ctx context.Context, ids []int64) error
	RetryPackages(ctx context.Context, ids []int64) error
	DeletePackages(ctx context.Context, ids []int64, deleteFiles bool) error
	// RescanPackage lists the package's source folder again and adds the
	// files that are not in the package yet. It returns the new downloads.
	RescanPackage(ctx context.Context, id int64) ([]Download, error)

	// --- Global ---

	// PauseAll / ResumeAll implement the tray actions (and are used by the
	// rotation manager around a global rotation).
	PauseAll(ctx context.Context) error
	ResumeAll(ctx context.Context) error
	Stats(ctx context.Context) (Stats, error)

	// Subscribe streams events until ctx is done, then closes the channel.
	// Slow subscribers lose events (never block the engine): after a gap
	// (Event.Seq jumps) clients should refetch the lists.
	Subscribe(ctx context.Context) <-chan Event
}
