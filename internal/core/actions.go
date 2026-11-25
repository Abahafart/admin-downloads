package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"admindescargas/internal/downloader"
	"admindescargas/internal/store"
)

// --- queries ---

// ListDownloads implements Engine.
func (s *Service) ListDownloads(ctx context.Context, f DownloadFilter) ([]Download, error) {
	q := store.DownloadQuery{PackageID: f.PackageID, Hoster: f.Hoster}
	for _, st := range f.States {
		if !st.Valid() {
			return nil, fmt.Errorf("%w: unknown state %q", ErrInvalidArgument, st)
		}
		q.States = append(q.States, string(st))
	}
	rows, err := s.st.ListDownloads(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]Download, len(rows))
	for i, r := range rows {
		out[i] = s.downloadView(r)
	}
	return out, nil
}

// GetDownload implements Engine.
func (s *Service) GetDownload(ctx context.Context, id int64) (Download, error) {
	r, err := s.st.GetDownload(ctx, id)
	if err != nil {
		return Download{}, wrapStore(err)
	}
	return s.downloadView(r), nil
}

// ListPackages implements Engine.
func (s *Service) ListPackages(ctx context.Context) ([]Package, error) {
	pkgs, err := s.st.ListPackages(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{})
	if err != nil {
		return nil, err
	}
	byPkg := map[int64][]Download{}
	for _, r := range rows {
		byPkg[r.PackageID] = append(byPkg[r.PackageID], s.downloadView(r))
	}
	out := make([]Package, len(pkgs))
	for i, p := range pkgs {
		out[i] = s.packageView(p, byPkg[p.ID])
	}
	return out, nil
}

// GetPackage implements Engine.
func (s *Service) GetPackage(ctx context.Context, id int64) (Package, []Download, error) {
	p, err := s.st.GetPackage(ctx, id)
	if err != nil {
		return Package{}, nil, wrapStore(err)
	}
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{PackageID: id})
	if err != nil {
		return Package{}, nil, err
	}
	dls := make([]Download, len(rows))
	for i, r := range rows {
		dls[i] = s.downloadView(r)
	}
	return s.packageView(p, dls), dls, nil
}

// --- control helpers ---

// loadRows returns the rows for ids, failing with ErrNotFound before
// changing anything if any id is unknown.
func (s *Service) loadRows(ctx context.Context, ids []int64) ([]store.DownloadRow, error) {
	ids = dedupe(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{IDs: ids})
	if err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		have := map[int64]bool{}
		for _, r := range rows {
			have[r.ID] = true
		}
		for _, id := range ids {
			if !have[id] {
				return nil, fmt.Errorf("%w: download %d", ErrNotFound, id)
			}
		}
	}
	return rows, nil
}

func dedupe(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// control stops the running jobs among ids with cause, then calls apply for
// each id while holding the engine lock (so the scheduler cannot start them
// in between). apply receives the fresh row.
func (s *Service) control(ctx context.Context, ids []int64, cause error, apply func(ctx context.Context, r store.DownloadRow) error) error {
	if err := s.checkRunning(); err != nil {
		return err
	}
	rows, err := s.loadRows(ctx, ids)
	if err != nil || len(rows) == 0 {
		return err
	}
	for {
		s.mu.Lock()
		var running []*job
		for _, r := range rows {
			if j := s.jobs[r.ID]; j != nil {
				running = append(running, j)
			}
		}
		if len(running) == 0 {
			var errs []error
			var touched []int64
			for _, r := range rows {
				fresh, err := s.st.GetDownload(ctx, r.ID)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					errs = append(errs, err)
					continue
				}
				if err := apply(ctx, fresh); err != nil {
					errs = append(errs, err)
				}
				touched = append(touched, r.ID)
			}
			s.mu.Unlock()
			for _, id := range touched {
				s.publishDownload(ctx, id)
			}
			s.kick()
			return errors.Join(errs...)
		}
		for _, j := range running {
			j.cancel(cause)
		}
		s.mu.Unlock()
		if err := s.waitJobs(ctx, running); err != nil {
			return err
		}
	}
}

func (s *Service) setState(ctx context.Context, r store.DownloadRow, from []string, to State, clearErr bool) error {
	_, err := s.st.UpdateDownloadsState(ctx, []int64{r.ID}, from, string(to), clearErr)
	return err
}

// --- downloads ---

// PauseDownloads implements Engine.
func (s *Service) PauseDownloads(ctx context.Context, ids []int64) error {
	from := []string{string(StateQueued), string(StateResolving), string(StateWaiting), string(StateDownloading),
		string(StateVerifying), string(StatePostprocessing)}
	return s.control(ctx, ids, errPaused, func(ctx context.Context, r store.DownloadRow) error {
		return s.setState(ctx, r, from, StatePaused, true)
	})
}

// ResumeDownloads implements Engine. Failed downloads are retried.
func (s *Service) ResumeDownloads(ctx context.Context, ids []int64) error {
	return s.control(ctx, ids, nil, func(ctx context.Context, r store.DownloadRow) error {
		switch State(r.State) {
		case StatePaused, StateWaiting:
			return s.setState(ctx, r, []string{string(StatePaused), string(StateWaiting)}, StateQueued, true)
		case StateFailed:
			return s.requeue(ctx, r.ID)
		}
		return nil
	})
}

func (s *Service) requeue(ctx context.Context, id int64) error {
	return s.st.UpdateDownload(ctx, id, store.DownloadPatch{State: ptr(string(StateQueued)), Attempts: ptr(0),
		Error: ptr(""), ErrorKind: ptr(""), WaitingUntil: ptr(time.Time{})})
}

// RetryDownloads implements Engine: failed (and waiting or paused)
// downloads go back to the queue with the attempt counter reset; they
// resume from the saved offset.
func (s *Service) RetryDownloads(ctx context.Context, ids []int64) error {
	return s.control(ctx, ids, nil, func(ctx context.Context, r store.DownloadRow) error {
		switch State(r.State) {
		case StateFailed, StateWaiting, StatePaused:
			return s.requeue(ctx, r.ID)
		}
		return nil
	})
}

// RetryAllFailed implements Engine.
func (s *Service) RetryAllFailed(ctx context.Context) error {
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{States: []string{string(StateFailed)}})
	if err != nil {
		return err
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return s.RetryDownloads(ctx, ids)
}

// RestartDownloads implements Engine: the .part file, the sidecar, the
// saved progress (and the finished file of a completed download) are
// deleted and the download is queued from zero.
func (s *Service) RestartDownloads(ctx context.Context, ids []int64) error {
	return s.control(ctx, ids, errRestart, func(ctx context.Context, r store.DownloadRow) error {
		if r.DestPath != "" {
			if err := downloader.RemoveFiles(r.DestPath + PartSuffix); err != nil {
				return fmt.Errorf("restart %d: %w", r.ID, err)
			}
			if r.State == string(StateCompleted) {
				if err := os.Remove(r.DestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("restart %d: %w", r.ID, err)
				}
			}
		}
		if err := s.st.ResetDownloadProgress(ctx, r.ID); err != nil {
			return err
		}
		return s.st.UpdateDownload(ctx, r.ID, store.DownloadPatch{State: ptr(string(StateQueued)), Attempts: ptr(0),
			Error: ptr(""), ErrorKind: ptr(""), WaitingUntil: ptr(time.Time{}), StartedAt: ptr(time.Time{}),
			CompletedAt: ptr(time.Time{})})
	})
}

// DeleteDownloads implements Engine.
func (s *Service) DeleteDownloads(ctx context.Context, ids []int64, deleteFiles bool) error {
	pkgs := map[int64]bool{}
	err := s.control(ctx, ids, errDeleted, func(ctx context.Context, r store.DownloadRow) error {
		if deleteFiles && r.DestPath != "" {
			if err := downloader.RemoveFiles(r.DestPath + PartSuffix); err != nil {
				return err
			}
			if r.State == string(StateCompleted) {
				if err := os.Remove(r.DestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		if r.State != string(StateCompleted) {
			s.addHistory(ctx, r, store.OutcomeDeleted, "")
		}
		if err := s.st.DeleteDownload(ctx, r.ID); err != nil {
			return err
		}
		pkgs[r.PackageID] = true
		s.bus.Publish(Event{Type: EventDownloadRemoved, ID: r.ID})
		return nil
	})
	// Drop packages left empty.
	for id := range pkgs {
		if n, cerr := s.st.CountDownloads(ctx, id); cerr == nil && n == 0 {
			if derr := s.st.DeletePackage(ctx, id); derr == nil {
				s.bus.Publish(Event{Type: EventPackageRemoved, ID: id})
			}
		} else {
			s.markPackageDirty(id)
		}
	}
	return err
}

// --- packages ---

func (s *Service) packageDownloadIDs(ctx context.Context, pkgIDs []int64) ([]int64, error) {
	var ids []int64
	for _, pid := range dedupe(pkgIDs) {
		if _, err := s.st.GetPackage(ctx, pid); err != nil {
			return nil, wrapStore(err)
		}
	}
	for _, pid := range dedupe(pkgIDs) {
		rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{PackageID: pid})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

func (s *Service) forPackages(ctx context.Context, pkgIDs []int64, fn func(context.Context, []int64) error) error {
	ids, err := s.packageDownloadIDs(ctx, pkgIDs)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return fn(ctx, ids)
}

// PausePackages implements Engine.
func (s *Service) PausePackages(ctx context.Context, ids []int64) error {
	return s.forPackages(ctx, ids, s.PauseDownloads)
}

// ResumePackages implements Engine.
func (s *Service) ResumePackages(ctx context.Context, ids []int64) error {
	return s.forPackages(ctx, ids, s.ResumeDownloads)
}

// RestartPackages implements Engine.
func (s *Service) RestartPackages(ctx context.Context, ids []int64) error {
	return s.forPackages(ctx, ids, s.RestartDownloads)
}

// RetryPackages implements Engine.
func (s *Service) RetryPackages(ctx context.Context, ids []int64) error {
	return s.forPackages(ctx, ids, s.RetryDownloads)
}

// DeletePackages implements Engine.
func (s *Service) DeletePackages(ctx context.Context, ids []int64, deleteFiles bool) error {
	dlIDs, err := s.packageDownloadIDs(ctx, ids)
	if err != nil {
		return err
	}
	if len(dlIDs) > 0 {
		if err := s.DeleteDownloads(ctx, dlIDs, deleteFiles); err != nil {
			return err
		}
	}
	for _, id := range dedupe(ids) {
		if _, err := s.st.GetPackage(ctx, id); err != nil {
			continue // removed with its last download
		}
		if deleteFiles {
			if p, err := s.st.GetPackage(ctx, id); err == nil && p.IsFolder && p.DestDir != "" {
				_ = os.Remove(p.DestDir) // only if empty
			}
		}
		if err := s.st.DeletePackage(ctx, id); err != nil {
			return err
		}
		s.bus.Publish(Event{Type: EventPackageRemoved, ID: id})
	}
	return nil
}

// --- global ---

// PauseAll implements Engine: nothing new starts and running transfers
// stop (they go back to queued, keeping their progress) until ResumeAll.
func (s *Service) PauseAll(ctx context.Context) error {
	if err := s.checkRunning(); err != nil {
		return err
	}
	s.mu.Lock()
	s.pausedAll = true
	ids := make([]int64, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	active := []string{string(StateResolving), string(StateDownloading), string(StateVerifying), string(StatePostprocessing)}
	err := s.control(ctx, ids, errPauseAll, func(ctx context.Context, r store.DownloadRow) error {
		return s.setState(ctx, r, active, StateQueued, false)
	})
	s.publishStats(ctx)
	return err
}

// ResumeAll implements Engine.
func (s *Service) ResumeAll(ctx context.Context) error {
	if err := s.checkRunning(); err != nil {
		return err
	}
	s.mu.Lock()
	s.pausedAll = false
	s.mu.Unlock()
	s.kick()
	s.publishStats(ctx)
	return nil
}

// SuspendForRotation is the rotation manager's PauseAll hook: like PauseAll
// it stops every transfer (back to queued, keeping progress) and holds the
// scheduler, but it is independent of the user's PauseAll, so
// ResumeFromRotation never resumes downloads the user paused. The job
// that triggered the rotation (blocked inside rotation.Manager.Acquire) is
// canceled too and goes back to the queue: its row is still queued or
// waiting at that point.
func (s *Service) SuspendForRotation(ctx context.Context) error {
	if err := s.checkRunning(); err != nil {
		return err
	}
	s.mu.Lock()
	s.rotationPaused++
	ids := make([]int64, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	active := []string{string(StateResolving), string(StateDownloading), string(StateVerifying), string(StatePostprocessing)}
	err := s.control(ctx, ids, errPauseAll, func(ctx context.Context, r store.DownloadRow) error {
		return s.setState(ctx, r, active, StateQueued, false)
	})
	s.bus.Publish(Event{Type: EventRotation, Message: "started"})
	s.publishStats(ctx)
	return err
}

// ResumeFromRotation is the rotation manager's ResumeAll hook.
func (s *Service) ResumeFromRotation(ctx context.Context) error {
	s.mu.Lock()
	if s.rotationPaused > 0 {
		s.rotationPaused--
	}
	running := s.running && !s.stopping
	s.mu.Unlock()
	if !running {
		return nil
	}
	s.bus.Publish(Event{Type: EventRotation, Message: "finished"})
	s.kick()
	s.publishStats(ctx)
	return nil
}

func (s *Service) publishStats(ctx context.Context) {
	if st, err := s.Stats(ctx); err == nil {
		s.bus.Publish(Event{Type: EventStats, Stats: &st})
	}
}
