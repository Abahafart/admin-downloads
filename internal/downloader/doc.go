// Package downloader is the segmented download engine (PLAN.md section 3 and
// 9.1): N Range segments written with WriteAt into a pre-allocated .part
// file, IDM-style dynamic splitting, fsync-before-persist of segment offsets
// (store segments table + a .part.adesc sidecar map), If-Range with
// ETag/Last-Modified checks, rate limiting (golang.org/x/time/rate),
// single-stream fallback without Range, and outcome handling through
// hosters.Hoster.Classify.
//
// A Download runs one attempt with one DownloadSpec. Transient errors of a
// single segment are retried in place with backoff; every other outcome
// (Quota, Expired, NeedsAuth, NotFound, Fatal, ErrRemoteChanged) stops all
// segments, flushes the progress and is returned to the caller (the engine),
// which rotates the egress, calls Prepare again or restarts from zero and
// then starts a new Download with the returned Progress as Resume.
//
// Invariant: the progress handed to Options.Persist (and written to the
// sidecar) never claims bytes that are not on disk: the segment map is
// snapshotted, the .part file is fsynced, and only then is the snapshot
// persisted.
package downloader
