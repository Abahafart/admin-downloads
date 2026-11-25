package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/downloader"
	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
	"admindescargas/internal/store"
)

// attemptState is carried across the attempts of one job.
type attemptState struct {
	quotaEgress  string    // egress that just hit an IP quota
	quotaUntil   time.Time // ... and until when
	quotaAccount string    // account that just hit its quota
	expired      int       // consecutive expired links without progress
	changed      int       // remote-changed restarts
	forceSingle  bool      // fall back to one connection
	fileBypass   bool      // FileQuotaBypasser already tried
	sess         *hosters.Session
}

func ptr[T any](v T) *T { return &v }

// PartSuffix is appended to the destination path while downloading.
const PartSuffix = ".part"

func fileInfo(r store.DownloadRow) hosters.FileInfo {
	fi := hosters.FileInfo{ID: r.FileID, Name: r.Name, RelPath: r.RelPath, Size: r.Size, Link: r.URL, Meta: r.Meta}
	if r.ChecksumAlgo != "" {
		fi.Checksum = &hosters.Checksum{Algo: r.ChecksumAlgo, Value: r.ChecksumValue}
	}
	return fi
}

// execute runs one download until it completes, fails, waits or is
// canceled. It never touches the state after its context is canceled:
// whoever canceled it owns the state.
func (s *Service) execute(j *job) {
	ctx := j.ctx
	row, err := s.st.GetDownload(ctx, j.id)
	if err != nil {
		return
	}
	if row.State != string(StateQueued) && row.State != string(StateWaiting) {
		return // paused/deleted between scheduling and start
	}
	pkg, err := s.st.GetPackage(ctx, row.PackageID)
	if err != nil {
		return
	}
	h := s.reg.Get(row.Hoster)
	if h == nil {
		s.fail(ctx, j, &row, hosters.NewFatal(fmt.Errorf("el plugin %q no está disponible", row.Hoster)))
		return
	}
	caps := h.Caps()
	if s.finishedOnDisk(ctx, row) {
		// The file was already downloaded and renamed; only the
		// post-processing (interrupted by a stop or a pause) is missing.
		s.postOnly(ctx, j, &row, pkg)
		return
	}
	var st attemptState
	for ctx.Err() == nil {
		if !s.attempt(ctx, j, h, caps, &row, pkg, &st) {
			return
		}
	}
}

// attempt performs one acquire/prepare/download cycle. It returns true to
// try again immediately.
func (s *Service) attempt(ctx context.Context, j *job, h hosters.Hoster, caps hosters.Caps, row *store.DownloadRow, pkg store.PackageRow, st *attemptState) bool {
	// The previous attempt may have learnt the remote identity (size, ETag,
	// resumable) and progress: always start from the stored row.
	fresh, err := s.st.GetDownload(ctx, row.ID)
	if err != nil {
		return false
	}
	*row = fresh

	// 1. Account.
	var acct *accounts.Account
	if s.accts != nil && (len(caps.AccountKinds) > 0 || caps.AccountRequired) {
		a, err := s.accts.Pick(ctx, row.Hoster)
		var nae *accounts.NoAccountError
		switch {
		case err == nil, ctx.Err() != nil:
		case errors.As(err, &nae):
			// Every account is over quota: wait for the first one if the
			// hoster cannot be used anonymously, else go anonymous.
			if caps.AccountRequired && !nae.Until.IsZero() {
				s.setWaiting(ctx, j, row, nae.Until, "Cuota de la cuenta agotada; no hay otra cuenta disponible",
					hosters.KindQuota.String(), hosters.ScopeAccount)
				return false
			}
		default:
			s.log.Warn("account pick failed", "hoster", row.Hoster, "err", err)
		}
		if a != nil && a.ID == st.quotaAccount {
			s.setWaiting(ctx, j, row, s.now().Add(s.retryAfter(row.Hoster, caps)),
				"Cuota de la cuenta agotada; no hay otra cuenta disponible", hosters.KindQuota.String(), hosters.ScopeAccount)
			return false
		}
		acct = a
	}
	if ctx.Err() != nil {
		return false
	}
	if caps.AccountRequired && acct == nil {
		s.fail(ctx, j, row, hosters.NewNeedsAuth(errors.New("este hoster necesita una cuenta")))
		return false
	}

	// 2. Egress.
	req := rotation.Request{
		Hoster:        row.Hoster,
		AllowRotation: s.cfg.Rotation.Enabled && (acct == nil || acct.AllowRotation),
		PreferEgress:  row.EgressID,
		QuotaScope:    caps.QuotaScope,
	}
	if acct != nil {
		req.AccountID = acct.ID
	}
	eg, err := s.rot.Acquire(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if we, ok := rotation.AsWait(err); ok {
			s.setWaiting(ctx, j, row, we.Until, "Cuota por IP: no hay otra salida disponible", hosters.KindQuota.String(), hosters.ScopeIP)
			return false
		}
		return s.handle(ctx, j, h, caps, row, st, hosters.NewTemporary(0, err), "", acct)
	}
	eid := eg.ID()
	if eid == st.quotaEgress && s.now().Before(st.quotaUntil) {
		// The manager could not offer anything else (e.g. rotation.Static).
		s.rot.Release(row.Hoster, eid)
		s.setWaiting(ctx, j, row, st.quotaUntil, "Cuota por IP: no hay otra salida disponible", hosters.KindQuota.String(), hosters.ScopeIP)
		return false
	}
	if !s.reserveEgress(j, eid, row.Hoster, caps) {
		s.rot.Release(row.Hoster, eid)
		s.deferJob(j.id)
		return false
	}
	released := false
	release := func() {
		if !released {
			released = true
			s.releaseEgress(j, eid, row.Hoster)
		}
	}
	defer release()

	// 3. Prepare.
	ip := s.publicIP(eid)
	j.set(func(j *job) {
		j.egressID, j.publicIP, j.state, j.waitingUntil, j.quotaScope = eid, ip, StateResolving, time.Time{}, ""
	})
	patch := store.DownloadPatch{State: ptr(string(StateResolving)), EgressID: &eid, PublicIP: &ip,
		WaitingUntil: ptr(time.Time{}), Error: ptr(""), ErrorKind: ptr(""), QuotaScope: ptr("")}
	if !s.update(ctx, j, row.ID, patch) {
		if ctx.Err() == nil {
			s.deferJob(j.id) // database trouble: try again later instead of spinning
		}
		return false
	}
	if acct != nil {
		// Best effort: the account row may be missing (FK) in odd setups.
		if err := s.st.UpdateDownload(ctx, row.ID, store.DownloadPatch{AccountID: &acct.ID}); err != nil && ctx.Err() == nil {
			s.log.Warn("record account failed", "download", row.ID, "err", err)
		}
	}
	row.EgressID = eid
	sess := s.session(eg, row.Hoster, acct)
	st.sess = sess
	spec, err := h.Prepare(ctx, sess, fileInfo(*row))
	if err == nil && spec == nil {
		err = hosters.NewFatal(errors.New("el plugin no devolvió un enlace de descarga"))
	}
	if err != nil {
		release()
		return s.handle(ctx, j, h, caps, row, st, err, eid, acct)
	}
	if spec.Checksum == nil && row.ChecksumAlgo != "" {
		spec.Checksum = &hosters.Checksum{Algo: row.ChecksumAlgo, Value: row.ChecksumValue}
	}

	// 4. Destination, exists policy and saved progress.
	partPath := row.DestPath + PartSuffix
	resume := s.loadResume(ctx, *row, partPath)
	if resume == nil && row.DoneBytes == 0 {
		if spec.Filename != "" && row.RelPath == "" && fsutil.SanitizeName(spec.Filename) != filepath.Base(row.DestPath) {
			if p, err := fsutil.SafeJoin(filepath.Dir(row.DestPath), spec.Filename); err == nil {
				name := fsutil.SanitizeName(spec.Filename)
				if s.update(ctx, j, row.ID, store.DownloadPatch{Name: &name, DestPath: &p}) {
					row.Name, row.DestPath = name, p
				}
			}
		}
		skipped, err := s.applyExistsPolicy(ctx, j, row, pkg, spec)
		if err != nil || skipped {
			return false
		}
		partPath = row.DestPath + PartSuffix
	}
	if err := os.MkdirAll(filepath.Dir(row.DestPath), 0o755); err != nil { //nolint:gosec // G301: download folders are user folders
		s.fail(ctx, j, row, hosters.NewFatal(fmt.Errorf("crear carpeta: %w", err)))
		return false
	}

	// 5. Download.
	if spec.Size >= 0 && spec.Size != row.Size {
		if s.update(ctx, j, row.ID, store.DownloadPatch{Size: &spec.Size}) {
			row.Size = spec.Size
		}
	}
	patch = store.DownloadPatch{State: ptr(string(StateDownloading))}
	if row.StartedAt.IsZero() {
		row.StartedAt = s.now()
		patch.StartedAt = &row.StartedAt
	}
	if !s.update(ctx, j, row.ID, patch) {
		return false
	}
	j.set(func(j *job) { j.state = StateDownloading })
	s.publishDownload(ctx, row.ID)

	var startDone int64
	if resume != nil {
		startDone = resume.Done()
	}
	opts := s.downloadOptions(row, caps, spec, h, sess, partPath, resume, st)
	dl, err := downloader.New(opts)
	if err != nil {
		s.fail(ctx, j, row, hosters.NewFatal(err))
		return false
	}
	j.set(func(j *job) { j.dl = dl })
	p, runErr := dl.Run(ctx)
	j.set(func(j *job) { j.dl, j.base = nil, p.Done() })
	release()
	if why := dl.Discarded(); why != "" && ctx.Err() == nil {
		s.notice(NoticeWarning, fmt.Sprintf("%s: el progreso guardado no sirve (%s); se descarga desde cero", row.Name, why))
	}
	if runErr == nil {
		s.finalize(ctx, j, row, pkg, spec, p)
		return false
	}
	if p.Done() > startDone {
		st.expired = 0
		if row.Attempts > 0 {
			row.Attempts = 0
			s.update(ctx, j, row.ID, store.DownloadPatch{Attempts: ptr(0)})
		}
	}
	return s.handle(ctx, j, h, caps, row, st, runErr, eid, acct)
}

func (s *Service) downloadOptions(row *store.DownloadRow, caps hosters.Caps, spec *hosters.DownloadSpec, h hosters.Hoster,
	sess *hosters.Session, partPath string, resume *downloader.Progress, st *attemptState) downloader.Options {
	conns := s.cfg.Downloads.ConnsPerFile
	if conns <= 0 {
		conns = downloader.DefaultMaxConns
	}
	hc := s.cfg.Hoster(row.Hoster)
	capConns := caps.MaxConnsPerFile
	if hc.MaxConnsPerFile > 0 {
		capConns = hc.MaxConnsPerFile
	}
	if capConns > 0 {
		conns = min(conns, capConns)
	}
	if spec.MaxConns > 0 {
		conns = min(conns, spec.MaxConns)
	}
	if st.forceSingle {
		conns = 1
	}
	limiters := []*rate.Limiter{s.globalRL}
	if l := s.cfg.Downloads.PerDownloadSpeedLimit; l > 0 {
		limiters = append(limiters, newLimiter(l))
	}
	id := row.ID
	lastRemote := downloader.Remote{Size: row.Size, ETag: row.ETag, LastModified: row.LastModified}
	lastResumable := row.Resumable
	persist := func(ctx context.Context, p downloader.Progress) error {
		segs := make([]store.SegmentRow, len(p.Segments))
		for i, sg := range p.Segments {
			segs[i] = store.SegmentRow{Idx: i, Start: sg.Start, End: sg.End, Done: sg.Done}
		}
		if err := s.st.SaveProgress(ctx, id, p.Done(), segs); err != nil {
			return err
		}
		if len(p.Segments) == 0 {
			return nil
		}
		if p.Remote.Size != lastRemote.Size || p.Remote.ETag != lastRemote.ETag ||
			p.Remote.LastModified != lastRemote.LastModified || lastResumable == nil || *lastResumable != p.Resumable {
			patch := store.DownloadPatch{Size: &p.Remote.Size, ETag: &p.Remote.ETag,
				LastModified: &p.Remote.LastModified, Resumable: &p.Resumable}
			if err := s.st.UpdateDownload(ctx, id, patch); err != nil {
				return err
			}
			lastRemote, lastResumable = p.Remote, &p.Resumable
			s.publishDownload(ctx, id)
		}
		return nil
	}
	return downloader.Options{
		Hoster:         h,
		Spec:           spec,
		Client:         sess.Client,
		PartPath:       partPath,
		Resume:         resume,
		MaxConns:       conns,
		MinSegmentSize: s.cfg.Downloads.MinSegmentSize,
		Limiters:       limiters,
		FlushInterval:  s.cfg.Downloads.FlushInterval.Duration,
		Persist:        persist,
		Sidecar:        &downloader.SidecarInfo{Hoster: row.Hoster, FileID: row.FileID, Name: row.Name},
		Backoff:        s.cfg.Downloads.RetryBackoff.Duration,
		MaxBackoff:     s.cfg.Downloads.MaxRetryBackoff.Duration,
		Log:            s.log.With("download", row.ID, "hoster", row.Hoster),
	}
}

// handle reacts to a failed attempt. It returns true to retry immediately.
func (s *Service) handle(ctx context.Context, j *job, h hosters.Hoster, caps hosters.Caps, row *store.DownloadRow,
	st *attemptState, err error, eid string, acct *accounts.Account) bool {
	if ctx.Err() != nil {
		return false
	}
	s.log.Debug("attempt failed", "download", row.ID, "egress", eid, "err", err)
	if errors.Is(err, downloader.ErrRemoteChanged) {
		st.changed++
		if st.changed >= 2 {
			st.forceSingle = true
		}
		_ = s.st.ResetDownloadProgress(ctx, row.ID)
		_ = downloader.RemoveFiles(row.DestPath + PartSuffix)
		row.DoneBytes, row.ETag, row.LastModified, row.Resumable = 0, "", "", nil
		j.set(func(j *job) { j.base = 0 })
		if st.changed > 4 {
			s.fail(ctx, j, row, hosters.NewFatal(errors.New("el archivo remoto cambia en cada intento")))
			return false
		}
		s.notice(NoticeWarning, fmt.Sprintf("%s: el archivo remoto cambió; se descarga desde cero", row.Name))
		return true
	}
	he, _ := hosters.AsError(err)
	switch hosters.KindOf(err) {
	case hosters.KindQuota:
		ra := he.RetryAfter
		if ra <= 0 {
			ra = s.retryAfter(row.Hoster, caps)
		}
		switch he.Scope {
		case hosters.ScopeAccount:
			if acct != nil && s.accts != nil {
				s.accts.ReportQuota(ctx, acct.ID, ra)
				st.quotaAccount = acct.ID
				return true
			}
			s.setWaiting(ctx, j, row, s.now().Add(ra), "Cuota de la cuenta agotada", hosters.KindQuota.String(), hosters.ScopeAccount)
			return false
		case hosters.ScopeFile:
			if fb, ok := h.(hosters.FileQuotaBypasser); ok && !st.fileBypass && st.sess != nil && fb.CanBypassFileQuota(st.sess) {
				st.fileBypass = true
				s.log.Info("file quota, preparing again with the hoster workaround", "download", row.ID, "hoster", row.Hoster)
				return true
			}
			s.setWaiting(ctx, j, row, s.now().Add(ra),
				"Cuota del archivo agotada: rotar la IP no ayuda (conecta una cuenta para copiarlo)", hosters.KindQuota.String(), hosters.ScopeFile)
			return false
		default:
			if eid != "" {
				s.rot.ReportQuota(ctx, row.Hoster, eid, hosters.ScopeIP, ra)
				st.quotaEgress, st.quotaUntil = eid, s.now().Add(ra)
				s.log.Info("ip quota, rotating egress", "download", row.ID, "hoster", row.Hoster, "egress", eid)
				return true
			}
			s.setWaiting(ctx, j, row, s.now().Add(ra), "Cuota por IP", hosters.KindQuota.String(), hosters.ScopeIP)
			return false
		}
	case hosters.KindExpired:
		st.expired++
		if st.expired <= 3 {
			s.log.Debug("direct link expired, preparing again", "download", row.ID)
			return true
		}
	case hosters.KindNeedsAuth, hosters.KindNotFound, hosters.KindFatal:
		s.fail(ctx, j, row, err)
		return false
	}
	// Temporary (or too many expirations): backoff, then the scheduler
	// picks it up again.
	var ne net.Error
	if eid != "" && errors.As(err, &ne) {
		s.rot.ReportFailure(eid, err)
	}
	row.Attempts++
	if row.Attempts > s.cfg.Downloads.MaxRetries {
		s.update(ctx, j, row.ID, store.DownloadPatch{Attempts: &row.Attempts})
		s.fail(ctx, j, row, err)
		return false
	}
	wait := downloader.Backoff(row.Attempts, s.cfg.Downloads.RetryBackoff.Duration, s.cfg.Downloads.MaxRetryBackoff.Duration)
	if he != nil && he.RetryAfter > wait {
		wait = he.RetryAfter
	}
	s.update(ctx, j, row.ID, store.DownloadPatch{Attempts: &row.Attempts})
	s.setWaiting(ctx, j, row, s.now().Add(wait),
		fmt.Sprintf("Reintento %d/%d: %s", row.Attempts, s.cfg.Downloads.MaxRetries, userError(err)), hosters.KindOf(err).String(), hosters.ScopeNone)
	return false
}

func (s *Service) retryAfter(hoster string, caps hosters.Caps) time.Duration {
	if d := s.cfg.Hoster(hoster).RetryAfter.Duration; d > 0 {
		return d
	}
	if caps.DefaultRetryAfter > 0 {
		return caps.DefaultRetryAfter
	}
	if d := s.cfg.Rotation.DefaultRetryAfter.Duration; d > 0 {
		return d
	}
	return time.Hour
}

// update writes a patch unless the job was canceled; it reports success.
func (s *Service) update(ctx context.Context, j *job, id int64, p store.DownloadPatch) bool {
	if ctx.Err() != nil {
		return false
	}
	if err := s.st.UpdateDownload(ctx, id, p); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("update download failed", "download", id, "err", err)
		}
		return false
	}
	return true
}

func (s *Service) setWaiting(ctx context.Context, j *job, row *store.DownloadRow, until time.Time, msg, kind string, scope hosters.QuotaScope) {
	qs := scope.String()
	if !s.update(ctx, j, row.ID, store.DownloadPatch{State: ptr(string(StateWaiting)), WaitingUntil: &until,
		Error: &msg, ErrorKind: &kind, QuotaScope: &qs}) {
		return
	}
	j.set(func(j *job) { j.state, j.waitingUntil, j.quotaScope = StateWaiting, until, qs })
	s.publishDownload(ctx, row.ID)
	if kind == hosters.KindQuota.String() {
		s.downloadNotice(row.ID, NoticeWarning, fmt.Sprintf("%s: %s · reintento %s", row.Name, msg, until.Local().Format("15:04:05")))
	}
}

func (s *Service) fail(ctx context.Context, j *job, row *store.DownloadRow, err error) {
	msg := userError(err)
	kind := hosters.KindOf(err).String()
	qs := ""
	if he, ok := hosters.AsError(err); ok && he.Kind == hosters.KindQuota {
		qs = he.Scope.String()
	}
	if !s.update(ctx, j, row.ID, store.DownloadPatch{State: ptr(string(StateFailed)), Error: &msg, ErrorKind: &kind,
		QuotaScope: &qs, WaitingUntil: ptr(time.Time{})}) {
		return
	}
	j.set(func(j *job) { j.state = StateFailed })
	s.addHistory(ctx, *row, store.OutcomeFailed, msg)
	s.cleanupRemote(*row)
	s.publishDownload(ctx, row.ID)
	s.downloadNotice(row.ID, NoticeError, fmt.Sprintf("Falló %s: %s", row.Name, msg))
}

func (s *Service) addHistory(ctx context.Context, r store.DownloadRow, outcome, msg string) {
	pkgName := ""
	if p, err := s.st.GetPackage(context.WithoutCancel(ctx), r.PackageID); err == nil {
		pkgName = p.Name
	}
	h := &store.HistoryRow{URL: r.URL, Hoster: r.Hoster, FileID: r.FileID, Name: r.Name, PackageName: pkgName,
		DestPath: r.DestPath, Size: r.Size, ChecksumAlgo: r.ChecksumAlgo, ChecksumValue: r.ChecksumValue,
		Outcome: outcome, Error: msg}
	if err := s.st.AddHistory(context.WithoutCancel(ctx), h); err != nil {
		s.log.Warn("add history failed", "err", err)
	}
}

// loadResume returns the saved progress from the database or, failing
// that, from the .part.adesc sidecar.
func (s *Service) loadResume(ctx context.Context, row store.DownloadRow, partPath string) *downloader.Progress {
	segs, err := s.st.ListSegments(ctx, row.ID)
	if err == nil && len(segs) > 0 {
		p := &downloader.Progress{
			Remote:    downloader.Remote{Size: row.Size, ETag: row.ETag, LastModified: row.LastModified},
			Resumable: row.Resumable != nil && *row.Resumable,
		}
		if row.ChecksumAlgo != "" {
			p.Remote.Checksum = &hosters.Checksum{Algo: row.ChecksumAlgo, Value: row.ChecksumValue}
		}
		for _, sg := range segs {
			p.Segments = append(p.Segments, downloader.Segment{Start: sg.Start, End: sg.End, Done: sg.Done})
		}
		return p
	}
	info, p, err := downloader.ReadSidecar(partPath)
	if err == nil && info.Hoster == row.Hoster && info.FileID == row.FileID && info.Name == row.Name && p.Done() > 0 {
		s.log.Info("resuming from sidecar", "download", row.ID)
		return &p
	}
	return nil
}

// applyExistsPolicy handles an existing destination file before a fresh
// download. It returns true when the download was completed by skipping.
func (s *Service) applyExistsPolicy(ctx context.Context, j *job, row *store.DownloadRow, pkg store.PackageRow, spec *hosters.DownloadSpec) (bool, error) {
	fi, err := os.Stat(row.DestPath)
	if err != nil {
		return false, nil //nolint:nilerr // no existing destination file, so no exists policy applies
	}
	policy := config.ExistsPolicy(pkg.ExistsPolicy)
	if policy == "" {
		policy = s.cfg.Downloads.ExistsPolicy
	}
	switch policy {
	case config.ExistsOverwrite:
		return false, nil
	case config.ExistsSkip:
		size := spec.Size
		if size < 0 {
			size = row.Size
		}
		if fi.Mode().IsRegular() && size >= 0 && fi.Size() == size {
			same := true
			if ck := spec.Checksum; ck != nil && checksumSupported(ck.Algo) {
				ok, err := verifyChecksum(ctx, row.DestPath, ck)
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				same = err == nil && ok
			}
			if same {
				s.complete(ctx, j, row, size, "ya existe en el destino, se omite")
				return true, nil
			}
		}
	}
	// rename (and skip with a different file): keep both.
	np := fsutil.UniquePath(row.DestPath)
	if !s.update(ctx, j, row.ID, store.DownloadPatch{DestPath: &np}) {
		return false, ctx.Err()
	}
	row.DestPath = np
	return false, nil
}

// finalize verifies, renames, post-processes and completes a download.
func (s *Service) finalize(ctx context.Context, j *job, row *store.DownloadRow, pkg store.PackageRow, spec *hosters.DownloadSpec, p downloader.Progress) {
	partPath := row.DestPath + PartSuffix
	size := p.Remote.Size
	if !s.update(ctx, j, row.ID, store.DownloadPatch{State: ptr(string(StateVerifying)), Size: &size, DoneBytes: &size}) {
		return
	}
	row.Size = size
	j.set(func(j *job) { j.state = StateVerifying })
	s.publishDownload(ctx, row.ID)

	ck := spec.Checksum
	if s.post == nil && s.cfg.PostProcess.VerifyChecksums && ck != nil && checksumSupported(ck.Algo) {
		ok, err := verifyChecksum(ctx, partPath, ck)
		if ctx.Err() != nil {
			return
		}
		if err != nil || !ok {
			_ = downloader.RemoveFiles(partPath)
			_ = s.st.ResetDownloadProgress(ctx, row.ID)
			msg := "la suma de verificación no coincide; reintenta para descargarlo de nuevo"
			if err != nil {
				msg = "no se pudo verificar: " + err.Error()
			}
			s.fail(ctx, j, row, hosters.NewFatal(errors.New(msg)))
			return
		}
	}

	dest := row.DestPath
	policy := config.ExistsPolicy(pkg.ExistsPolicy)
	if policy != config.ExistsOverwrite {
		if _, err := os.Lstat(dest); err == nil {
			dest = fsutil.UniquePath(dest)
		}
	}
	if err := os.Rename(partPath, dest); err != nil {
		s.fail(ctx, j, row, hosters.NewFatal(fmt.Errorf("renombrar: %w", err)))
		return
	}
	_ = os.Remove(downloader.SidecarPath(partPath))
	_ = s.st.DeleteSegments(context.WithoutCancel(ctx), row.ID)
	if dest != row.DestPath {
		row.DestPath = dest
		s.update(ctx, j, row.ID, store.DownloadPatch{DestPath: &dest})
	}

	if !s.runPost(ctx, j, row, pkg, ck) {
		return
	}
	s.complete(ctx, j, row, size, "")
}

// runPost runs the PostProcessor (if any) on the final file. It returns
// false when the download must not be completed (failed or canceled).
func (s *Service) runPost(ctx context.Context, j *job, row *store.DownloadRow, pkg store.PackageRow, ck *hosters.Checksum) bool {
	if s.post == nil {
		return true
	}
	if !s.update(ctx, j, row.ID, store.DownloadPatch{State: ptr(string(StatePostprocessing))}) {
		return false
	}
	j.set(func(j *job) { j.state = StatePostprocessing })
	s.publishDownload(ctx, row.ID)
	info := s.postInfo(ctx, row.ID, pkg)
	info.Checksum = ck
	if info.Checksum == nil && row.ChecksumAlgo != "" {
		info.Checksum = &hosters.Checksum{Algo: row.ChecksumAlgo, Value: row.ChecksumValue}
	}
	err := s.post.Process(ctx, info, row.DestPath)
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false // stopped/paused: Process runs again on resume
	}
	if errors.Is(err, ErrChecksumMismatch) {
		_ = os.Remove(row.DestPath)
		_ = s.st.ResetDownloadProgress(ctx, row.ID)
		s.fail(ctx, j, row, hosters.NewFatal(errors.New("la suma de verificación no coincide; reintenta para descargarlo de nuevo")))
		return false
	}
	s.fail(ctx, j, row, hosters.NewFatal(fmt.Errorf("postproceso: %w", err)))
	return false
}

// finishedOnDisk reports whether the download's bytes are complete and
// already renamed to DestPath (no .part, no saved segments): a previous run
// was interrupted after the rename, during post-processing.
func (s *Service) finishedOnDisk(ctx context.Context, row store.DownloadRow) bool {
	if row.Size < 0 || row.DoneBytes != row.Size || row.DestPath == "" || row.StartedAt.IsZero() {
		return false
	}
	fi, err := os.Stat(row.DestPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != row.Size {
		return false
	}
	if _, err := os.Lstat(row.DestPath + PartSuffix); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	segs, err := s.st.ListSegments(ctx, row.ID)
	return err == nil && len(segs) == 0
}

// postOnly finishes a download found complete on disk: post-processing
// (again) and completion.
func (s *Service) postOnly(ctx context.Context, j *job, row *store.DownloadRow, pkg store.PackageRow) {
	s.log.Info("resuming post-processing", "download", row.ID)
	if !s.runPost(ctx, j, row, pkg, nil) {
		return
	}
	s.complete(ctx, j, row, row.Size, "")
}

func (s *Service) postInfo(ctx context.Context, id int64, pkg store.PackageRow) PostProcessInfo {
	var info PostProcessInfo
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{PackageID: pkg.ID})
	if err != nil {
		return info
	}
	views := make([]Download, 0, len(rows))
	complete := true
	for _, r := range rows {
		v := s.downloadView(r)
		if r.ID == id {
			info.Download = v
		} else if v.State != StateCompleted {
			complete = false
		}
		views = append(views, v)
	}
	info.Package = s.packageView(pkg, views)
	info.PackageComplete = complete
	return info
}

func (s *Service) complete(ctx context.Context, j *job, row *store.DownloadRow, size int64, why string) {
	now := s.now()
	if !s.update(ctx, j, row.ID, store.DownloadPatch{State: ptr(string(StateCompleted)), Size: &size, DoneBytes: &size,
		CompletedAt: &now, Error: ptr(""), ErrorKind: ptr(""), QuotaScope: ptr(""), WaitingUntil: ptr(time.Time{})}) {
		return
	}
	row.Size = size
	j.set(func(j *job) { j.state, j.base = StateCompleted, size })
	s.addHistory(ctx, *row, store.OutcomeCompleted, "")
	s.cleanupRemote(*row)
	s.publishDownload(ctx, row.ID)
	if why != "" {
		s.downloadNotice(row.ID, NoticeInfo, fmt.Sprintf("%s: %s", row.Name, why))
	} else {
		s.downloadNotice(row.ID, NoticeSuccess, "Descarga completada: "+row.Name)
	}
}

// reserveEgress applies the per-egress limits; it records the reservation
// on the job so that it is released exactly once.
func (s *Service) reserveEgress(j *job, eid, hoster string, caps hosters.Caps) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.cfg.Downloads.MaxPerEgress; l > 0 && s.egressUse[eid] >= l {
		return false
	}
	key := eid + "|" + hoster
	if caps.MaxParallel > 0 && s.egressHost[key] >= caps.MaxParallel {
		return false
	}
	s.egressUse[eid]++
	s.egressHost[key]++
	j.reserved = true
	return true
}

func (s *Service) releaseEgress(j *job, eid, hoster string) {
	s.mu.Lock()
	if j.reserved {
		j.reserved = false
		if s.egressUse[eid]--; s.egressUse[eid] <= 0 {
			delete(s.egressUse, eid)
		}
		key := eid + "|" + hoster
		if s.egressHost[key]--; s.egressHost[key] <= 0 {
			delete(s.egressHost, key)
		}
	}
	s.mu.Unlock()
	s.rot.Release(hoster, eid)
}

func (s *Service) publicIP(eid string) string {
	for _, e := range s.rot.Egresses() {
		if e.ID == eid {
			return e.PublicIP
		}
	}
	return ""
}

// session returns the cached session of (egress, hoster, account), each
// with its own cookie jar so identities never mix.
func (s *Service) session(eg netx.Egress, hoster string, acct *accounts.Account) *hosters.Session {
	acctID := ""
	if acct != nil {
		acctID = acct.ID
	}
	key := eg.ID() + "|" + hoster + "|" + acctID
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[key]; ok && sess.Egress == eg {
		return sess
	}
	sess := hosters.NewSession(eg, acct, nil)
	sess.Hoster = hoster
	sess.Secrets = s.secrets
	sess.Log = s.log.With("hoster", hoster, "egress", eg.ID(), "account", acctID)
	s.sessions[key] = sess
	return sess
}

// cleanupRemote lets a hosters.Cleaner delete what it created for the
// download (e.g. a Drive copy), in the background with a session of the
// download's account.
func (s *Service) cleanupRemote(row store.DownloadRow) {
	h := s.reg.Get(row.Hoster)
	c, ok := h.(hosters.Cleaner)
	if !ok || row.AccountID == "" {
		return
	}
	lookup, ok := s.accts.(AccountLookup)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.bgCtx == nil || s.bgCtx.Err() != nil {
		s.mu.Unlock()
		return
	}
	parent := s.bgCtx
	s.bgWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.bgWG.Done()
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		acct, err := lookup.Get(ctx, row.AccountID)
		if err != nil || acct == nil {
			return
		}
		eg := s.rot.Get(row.EgressID)
		if eg == nil {
			for _, st := range s.rot.Egresses() {
				if e := s.rot.Get(st.ID); e != nil && st.Healthy && e.Scope() == netx.ScopePerConnection {
					eg = e
					break
				}
			}
		}
		if eg == nil {
			return
		}
		if err := c.Cleanup(ctx, s.session(eg, row.Hoster, acct), fileInfo(row)); err != nil && ctx.Err() == nil {
			s.log.Warn("hoster cleanup failed", "download", row.ID, "hoster", row.Hoster, "err", err)
		}
	}()
}
