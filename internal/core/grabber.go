package core

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
	"admindescargas/internal/linkgrabber"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
	"admindescargas/internal/store"
)

// AddLinks implements Engine.
func (s *Service) AddLinks(ctx context.Context, req AddLinksRequest) ([]GrabberEntry, error) {
	if err := s.checkRunning(); err != nil {
		return nil, err
	}
	var urls []string
	seen := map[string]bool{}
	add := func(u string) {
		u = linkgrabber.NormalizeURL(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		urls = append(urls, u)
	}
	for _, u := range req.URLs {
		if found := linkgrabber.ExtractURLs(u); len(found) > 0 {
			for _, f := range found {
				add(f)
			}
		} else {
			add(u)
		}
	}
	for _, u := range linkgrabber.ExtractURLs(req.Text) {
		add(u)
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("%w: no links found", ErrInvalidArgument)
	}
	if req.Source == "clipboard" {
		refs := make([]store.DownloadRef, len(urls))
		for i, u := range urls {
			refs[i] = store.DownloadRef{URL: u}
		}
		known, err := s.st.KnownLinks(ctx, refs)
		if err != nil {
			return nil, err
		}
		kept := urls[:0]
		for i, u := range urls {
			if !known[i] {
				kept = append(kept, u)
			}
		}
		urls = kept
	}
	var out []GrabberEntry
	for _, u := range urls {
		row := store.GrabberEntryRow{URL: u, State: string(GrabberPending), Source: req.Source,
			AutoConfirm: req.AutoConfirm, DestDir: req.DestDir, PackageName: req.PackageName}
		h := s.reg.MatchString(u)
		if h == nil {
			row.State = string(GrabberUnsupported)
			row.Error = "ningún plugin reconoce este enlace"
		} else {
			row.Hoster = h.Name()
		}
		created, err := s.st.CreateGrabberEntry(ctx, &row)
		if err != nil {
			return out, err
		}
		if !created {
			// Already in the LinkGrabber. An entry whose listing failed is
			// resolved again (adding the link again is the natural retry).
			if h != nil {
				if e, ok := s.retryFailedEntry(ctx, u); ok {
					out = append(out, e)
				}
			}
			continue
		}
		e := s.grabberView(ctx, row, false)
		s.bus.Publish(Event{Type: EventGrabberAdded, Grabber: &e})
		out = append(out, e)
		if h != nil {
			s.startResolve(row.ID)
		}
	}
	return out, nil
}

// ListLinkGrabber implements Engine.
func (s *Service) ListLinkGrabber(ctx context.Context) ([]GrabberEntry, error) {
	rows, err := s.st.ListGrabberEntries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GrabberEntry, len(rows))
	for i, r := range rows {
		out[i] = s.grabberView(ctx, r, true)
	}
	return out, nil
}

// RemoveLinkGrabber implements Engine.
func (s *Service) RemoveLinkGrabber(ctx context.Context, ids []int64) error {
	ids = dedupe(ids)
	for _, id := range ids {
		if _, err := s.st.GetGrabberEntry(ctx, id); err != nil {
			return wrapStore(err)
		}
	}
	s.mu.Lock()
	for _, id := range ids {
		if c := s.resolvers[id]; c != nil {
			c()
		}
	}
	s.mu.Unlock()
	if err := s.st.DeleteGrabberEntries(ctx, ids); err != nil {
		return err
	}
	for _, id := range ids {
		s.bus.Publish(Event{Type: EventGrabberRemoved, ID: id})
	}
	return nil
}

func (s *Service) grabberView(ctx context.Context, r store.GrabberEntryRow, withKnown bool) GrabberEntry {
	e := GrabberEntry{ID: r.ID, URL: r.URL, Hoster: r.Hoster, State: GrabberState(r.State), Error: r.Error,
		PackageName: r.PackageName, IsFolder: r.IsFolder, Source: r.Source, AddedAt: r.CreatedAt,
		Files: make([]GrabberFile, 0, len(r.Files))}
	var refs []store.DownloadRef
	for _, f := range r.Files {
		gf := GrabberFile{Index: f.Idx, Name: f.Name, RelPath: fsutil.SanitizeRelPath(f.RelPath), Size: f.Size}
		if f.ChecksumAlgo != "" {
			gf.Checksum = &hosters.Checksum{Algo: f.ChecksumAlgo, Value: f.ChecksumValue}
		}
		e.Files = append(e.Files, gf)
		if f.Size < 0 || e.TotalSize < 0 {
			e.TotalSize = -1
		} else {
			e.TotalSize += f.Size
		}
		refs = append(refs, store.DownloadRef{Hoster: r.Hoster, FileID: f.FileID, URL: f.Link})
	}
	if withKnown && len(refs) > 0 {
		if known, err := s.st.KnownLinks(ctx, refs); err == nil {
			for i := range e.Files {
				e.Files[i].Known = known[i]
			}
		}
	}
	return e
}

// --- resolution ---

func (s *Service) startResolve(entryID int64) {
	s.mu.Lock()
	if s.bgCtx == nil || s.stopping {
		s.mu.Unlock()
		return
	}
	if _, busy := s.resolvers[entryID]; busy {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.bgCtx)
	s.resolvers[entryID] = cancel
	s.bgWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.bgWG.Done()
		defer func() {
			s.mu.Lock()
			delete(s.resolvers, entryID)
			s.mu.Unlock()
			cancel()
		}()
		select {
		case s.resolveSem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-s.resolveSem }()
		s.resolveEntry(ctx, entryID)
	}()
}

func (s *Service) resolveLimiter(hoster string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.resolveRL[hoster]; ok {
		return l
	}
	rps := s.cfg.Hoster(hoster).RequestsPerSecond
	if rps <= 0 {
		rps = s.cfg.Downloads.ResolveRequestsPerSecond
	}
	l := rate.NewLimiter(rate.Inf, 1)
	if rps > 0 {
		l = rate.NewLimiter(rate.Limit(rps), 1)
	}
	s.resolveRL[hoster] = l
	return l
}

// resolveSession returns a session for listing links of hoster. Listing
// usually works even on an egress that is over its transfer quota, so a
// WaitError falls back to any healthy egress.
func (s *Service) resolveSession(ctx context.Context, hoster string, caps hosters.Caps) (*hosters.Session, func(), error) {
	acct := s.pickForResolve(ctx, hoster, caps)
	eg, err := s.rot.Acquire(ctx, rotation.Request{Hoster: hoster, AllowRotation: s.cfg.Rotation.Enabled && acct == nil,
		QuotaScope: caps.QuotaScope, AccountID: accountID(acct)})
	release := func() {}
	if err == nil {
		release = func() { s.rot.Release(hoster, eg.ID()) }
	} else {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		var fallback netx.Egress
		for _, st := range s.rot.Egresses() {
			if st.Healthy {
				if e := s.rot.Get(st.ID); e != nil {
					fallback = e
					break
				}
			}
		}
		if fallback == nil {
			return nil, nil, err
		}
		eg = fallback
	}
	return s.session(eg, hoster, acct), release, nil
}

func (s *Service) resolveEntry(ctx context.Context, id int64) {
	e, err := s.st.GetGrabberEntry(ctx, id)
	if err != nil {
		return
	}
	h := s.reg.Get(e.Hoster)
	if h == nil {
		s.setGrabberError(ctx, id, GrabberUnsupported, "ningún plugin reconoce este enlace")
		return
	}
	if err := s.st.UpdateGrabberEntry(ctx, id, store.GrabberEntryPatch{State: ptr(string(GrabberResolving)), Error: ptr("")}); err != nil {
		return
	}
	s.publishGrabber(ctx, id)

	if err := s.resolveLimiter(e.Hoster).Wait(ctx); err != nil {
		return
	}
	sess, release, err := s.resolveSession(ctx, e.Hoster, h.Caps())
	if err != nil {
		if ctx.Err() == nil {
			s.setGrabberError(ctx, id, GrabberError, userError(err))
		}
		return
	}
	defer release()

	// Files emitted while listing are stored in batches so the tree shows
	// up progressively; indexes stay stable for the final result.
	var mu sync.Mutex
	idx := map[string]int{}
	var pending []store.GrabberFileRow
	lastFlush := time.Now()
	flush := func() {
		mu.Lock()
		batch := pending
		pending = nil
		lastFlush = time.Now()
		mu.Unlock()
		if len(batch) == 0 {
			return
		}
		if err := s.st.AddGrabberFiles(ctx, id, batch); err == nil {
			s.publishGrabber(ctx, id)
		}
	}
	assign := func(f hosters.FileInfo) (store.GrabberFileRow, bool) {
		mu.Lock()
		defer mu.Unlock()
		k := fileKey(f)
		i, ok := idx[k]
		if !ok {
			i = len(idx)
			idx[k] = i
		}
		return grabberFileRow(i, f), !ok
	}
	emit := func(f hosters.FileInfo) {
		row, isNew := assign(f)
		if !isNew {
			return
		}
		mu.Lock()
		pending = append(pending, row)
		due := len(pending) >= 100 || time.Since(lastFlush) > 500*time.Millisecond
		mu.Unlock()
		if due {
			flush()
		}
	}
	res, err := linkgrabber.Resolve(ctx, h, sess, e.URL, emit)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.setGrabberError(ctx, id, GrabberError, userError(err))
		return
	}
	files := make([]store.GrabberFileRow, 0, len(res.Files))
	for _, f := range res.Files {
		row, _ := assign(f)
		files = append(files, row)
	}
	if err := s.st.ReplaceGrabberFiles(ctx, id, files); err != nil {
		s.setGrabberError(ctx, id, GrabberError, err.Error())
		return
	}
	patch := store.GrabberEntryPatch{State: ptr(string(GrabberReady)), IsFolder: ptr(res.IsFolder)}
	if e.PackageName == "" {
		patch.PackageName = ptr(linkgrabber.PackageName(res, e.URL))
	}
	if len(files) == 0 && !res.IsFolder {
		patch.State, patch.Error = ptr(string(GrabberError)), ptr("el enlace no contiene archivos")
	}
	if err := s.st.UpdateGrabberEntry(ctx, id, patch); err != nil {
		return
	}
	s.publishGrabber(ctx, id)
	if e.AutoConfirm && *patch.State == string(GrabberReady) && len(files) > 0 {
		if _, err := s.ConfirmLinkGrabber(ctx, ConfirmRequest{DestDir: e.DestDir, Items: []ConfirmItem{{EntryID: id}}}); err != nil {
			s.log.Warn("auto-confirm failed", "entry", id, "err", err)
		}
	}
}

func fileKey(f hosters.FileInfo) string {
	if f.ID != "" {
		return "id:" + f.ID
	}
	return "p:" + f.RelPath + "\x00" + f.Name + "\x00" + f.Link
}

func grabberFileRow(i int, f hosters.FileInfo) store.GrabberFileRow {
	r := store.GrabberFileRow{Idx: i, FileID: f.ID, Name: f.Name, RelPath: f.RelPath, Size: f.Size, Link: f.Link, Meta: f.Meta}
	if f.Checksum != nil {
		r.ChecksumAlgo, r.ChecksumValue = f.Checksum.Algo, f.Checksum.Value
	}
	return r
}

func (s *Service) setGrabberError(ctx context.Context, id int64, st GrabberState, msg string) {
	if err := s.st.UpdateGrabberEntry(context.WithoutCancel(ctx), id, store.GrabberEntryPatch{State: ptr(string(st)), Error: &msg}); err == nil {
		s.publishGrabber(ctx, id)
	}
}

func (s *Service) publishGrabber(ctx context.Context, id int64) {
	r, err := s.st.GetGrabberEntry(context.WithoutCancel(ctx), id)
	if err != nil {
		return
	}
	e := s.grabberView(ctx, r, false)
	s.bus.Publish(Event{Type: EventGrabberUpdated, Grabber: &e})
}

func accountID(a *accounts.Account) string {
	if a == nil {
		return ""
	}
	return a.ID
}

// pickForResolve returns the account to list links with (nil = anonymous).
func (s *Service) pickForResolve(ctx context.Context, hoster string, caps hosters.Caps) *accounts.Account {
	if s.accts == nil || len(caps.AccountKinds) == 0 && !caps.AccountRequired {
		return nil
	}
	a, err := s.accts.Pick(ctx, hoster)
	if err != nil {
		return nil
	}
	return a
}

// --- confirm ---

// ConfirmLinkGrabber implements Engine.
func (s *Service) ConfirmLinkGrabber(ctx context.Context, req ConfirmRequest) ([]Package, error) {
	if err := s.checkRunning(); err != nil {
		return nil, err
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("%w: no entries", ErrInvalidArgument)
	}
	base, policy, err := s.destAndPolicy(req.DestDir, req.ExistsPolicy)
	if err != nil {
		return nil, err
	}
	type plan struct {
		entry store.GrabberEntryRow
		item  ConfirmItem
		files []store.GrabberFileRow
		name  string
	}
	var plans []plan
	for _, it := range req.Items {
		e, err := s.st.GetGrabberEntry(ctx, it.EntryID)
		if err != nil {
			return nil, wrapStore(err)
		}
		if e.State != string(GrabberReady) {
			return nil, fmt.Errorf("%w: entry %d is %s", ErrInvalidState, e.ID, e.State)
		}
		items := make([]linkgrabber.Item, len(e.Files))
		byIdx := map[int]store.GrabberFileRow{}
		for i, f := range e.Files {
			items[i] = linkgrabber.Item{Index: f.Idx, RelPath: fsutil.SanitizeRelPath(f.RelPath), Name: f.Name, Size: f.Size}
			byIdx[f.Idx] = f
		}
		sel := linkgrabber.Select(items, it.Include, it.Exclude, it.FileIndexes)
		if len(sel) == 0 {
			return nil, fmt.Errorf("%w: nothing selected in entry %d", ErrInvalidArgument, e.ID)
		}
		p := plan{entry: e, item: it, name: e.PackageName}
		if it.PackageName != "" {
			p.name = it.PackageName
		}
		if strings.TrimSpace(p.name) == "" {
			p.name = linkgrabber.PackageName(nil, e.URL)
		}
		for _, i := range sel {
			p.files = append(p.files, byIdx[i])
		}
		plans = append(plans, p)
	}

	state := StateQueued
	if req.Paused {
		state = StatePaused
	}
	var created []int64
	var newDownloads []int64
	err = s.st.Tx(ctx, func(tx *sql.Tx) error {
		// Loose files sharing an explicit package name go into one package.
		loose := map[string]*store.PackageRow{}
		used := map[string]bool{}
		positions := map[int64]int{}
		for _, p := range plans {
			var pkg *store.PackageRow
			if !p.entry.IsFolder && p.item.PackageName != "" {
				pkg = loose[p.item.PackageName]
			}
			if pkg == nil {
				pkg = &store.PackageRow{Name: p.name, SourceURL: p.entry.URL, Hoster: p.entry.Hoster,
					IsFolder: p.entry.IsFolder, Priority: req.Priority, ExistsPolicy: string(policy), DestDir: base}
				if p.entry.IsFolder {
					dir, err := fsutil.SafeJoin(base, p.name)
					if err != nil {
						return fmt.Errorf("%w: package name %q: %w", ErrInvalidArgument, p.name, err)
					}
					pkg.DestDir = dir
				} else {
					pkg.SourceURL = ""
				}
				if err := s.st.CreatePackageTx(ctx, tx, pkg); err != nil {
					return err
				}
				created = append(created, pkg.ID)
				if !p.entry.IsFolder && p.item.PackageName != "" {
					loose[p.item.PackageName] = pkg
				}
			}
			seenFile := map[string]bool{}
			for _, f := range p.files {
				if f.FileID != "" {
					k := p.entry.Hoster + "\x00" + f.FileID
					if seenFile[k] {
						continue
					}
					seenFile[k] = true
				}
				d, err := s.newDownloadRow(pkg, p.entry.Hoster, f, base, p.entry.IsFolder, used)
				if err != nil {
					return err
				}
				d.Position = positions[pkg.ID]
				positions[pkg.ID]++
				d.State = string(state)
				d.Priority = req.Priority
				if err := s.st.CreateDownloadTx(ctx, tx, d); err != nil {
					return err
				}
				newDownloads = append(newDownloads, d.ID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var entryIDs []int64
	for _, p := range plans {
		entryIDs = append(entryIDs, p.entry.ID)
	}
	if err := s.st.DeleteGrabberEntries(ctx, entryIDs); err != nil {
		return nil, err
	}
	for _, id := range entryIDs {
		s.bus.Publish(Event{Type: EventGrabberRemoved, ID: id})
	}
	out := make([]Package, 0, len(created))
	for _, id := range created {
		p, dls, err := s.GetPackage(ctx, id)
		if err != nil {
			return nil, err
		}
		s.bus.Publish(Event{Type: EventPackageAdded, Package: &p})
		for i := range dls {
			s.bus.Publish(Event{Type: EventDownloadAdded, Download: &dls[i]})
		}
		out = append(out, p)
	}
	s.kick()
	return out, nil
}

// destAndPolicy resolves the base directory and exists policy of a confirm.
func (s *Service) destAndPolicy(destDir, policy string) (string, config.ExistsPolicy, error) {
	base := destDir
	if base == "" {
		base = s.cfg.DownloadDir
	}
	if base == "" {
		return "", "", fmt.Errorf("%w: no destination directory", ErrInvalidArgument)
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return "", "", fmt.Errorf("%w: destination: %w", ErrInvalidArgument, err)
	}
	p := config.ExistsPolicy(policy)
	if p == "" {
		p = s.cfg.Downloads.ExistsPolicy
	}
	switch p {
	case config.ExistsSkip, config.ExistsOverwrite, config.ExistsRename:
	case "":
		p = config.ExistsSkip
	default:
		return "", "", fmt.Errorf("%w: exists_policy %q", ErrInvalidArgument, policy)
	}
	return abs, p, nil
}

// newDownloadRow builds a download for file f of package pkg. Destination
// paths always go through fsutil.SafeJoin: <base>/<package>/<rel_path> for
// folders and <base>/<name> for loose files. used de-duplicates paths
// within one confirm.
func (s *Service) newDownloadRow(pkg *store.PackageRow, hoster string, f store.GrabberFileRow, base string, folder bool, used map[string]bool) (*store.DownloadRow, error) {
	name := fsutil.SanitizeName(f.Name)
	rel := fsutil.SanitizeRelPath(f.RelPath)
	if folder && rel == "" {
		rel = name
	}
	var dest string
	var err error
	if folder {
		dest, err = fsutil.SafeJoin(base, pkg.Name+"/"+rel)
	} else {
		dest, err = fsutil.SafeJoin(base, name)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: unsafe path %q: %w", ErrInvalidArgument, f.RelPath, err)
	}
	if used[dest] {
		ext := filepath.Ext(dest)
		stem := strings.TrimSuffix(dest, ext)
		for i := 1; ; i++ {
			cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
			if !used[cand] {
				dest = cand
				break
			}
		}
	}
	used[dest] = true
	d := &store.DownloadRow{PackageID: pkg.ID, Hoster: hoster, URL: f.Link, FileID: f.FileID, Name: name,
		RelPath: rel, DestPath: dest, Meta: f.Meta, Size: f.Size, ChecksumAlgo: f.ChecksumAlgo, ChecksumValue: f.ChecksumValue}
	if !folder {
		d.RelPath = ""
	}
	if d.URL == "" {
		d.URL = pkg.SourceURL
	}
	return d, nil
}

// --- rescan ---

// RescanPackage implements Engine.
func (s *Service) RescanPackage(ctx context.Context, id int64) ([]Download, error) {
	if err := s.checkRunning(); err != nil {
		return nil, err
	}
	pkg, err := s.st.GetPackage(ctx, id)
	if err != nil {
		return nil, wrapStore(err)
	}
	if !pkg.IsFolder || pkg.SourceURL == "" {
		return nil, fmt.Errorf("%w: package %d is not a folder", ErrInvalidState, id)
	}
	h := s.reg.Get(pkg.Hoster)
	if h == nil {
		h = s.reg.MatchString(pkg.SourceURL)
	}
	if h == nil {
		return nil, fmt.Errorf("%w: no plugin for package %d", ErrInvalidState, id)
	}
	if err := s.resolveLimiter(h.Name()).Wait(ctx); err != nil {
		return nil, err
	}
	sess, release, err := s.resolveSession(ctx, h.Name(), h.Caps())
	if err != nil {
		return nil, err
	}
	res, err := linkgrabber.Resolve(ctx, h, sess, pkg.SourceURL, nil)
	release()
	if err != nil {
		return nil, err
	}
	existing, err := s.st.ListDownloads(ctx, store.DownloadQuery{PackageID: id})
	if err != nil {
		return nil, err
	}
	haveID, havePath, used := map[string]bool{}, map[string]bool{}, map[string]bool{}
	pos := -1
	for _, d := range existing {
		if d.FileID != "" {
			haveID[d.FileID] = true
		}
		havePath[d.RelPath] = true
		used[d.DestPath] = true
		pos = max(pos, d.Position)
	}
	base := filepath.Dir(pkg.DestDir)
	var ids []int64
	err = s.st.Tx(ctx, func(tx *sql.Tx) error {
		for i, f := range res.Files {
			rel := fsutil.SanitizeRelPath(f.RelPath)
			if f.ID != "" && haveID[f.ID] || f.ID == "" && havePath[rel] {
				continue
			}
			if f.ID != "" {
				haveID[f.ID] = true
			}
			havePath[rel] = true
			d, err := s.newDownloadRow(&pkg, h.Name(), grabberFileRow(i, f), base, true, used)
			if err != nil {
				return err
			}
			pos++
			d.Position, d.Priority, d.State = pos, pkg.Priority, string(StateQueued)
			if err := s.st.CreateDownloadTx(ctx, tx, d); err != nil {
				return err
			}
			ids = append(ids, d.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Download, 0, len(ids))
	for _, did := range ids {
		d, err := s.GetDownload(ctx, did)
		if err != nil {
			return nil, err
		}
		s.bus.Publish(Event{Type: EventDownloadAdded, Download: &d})
		out = append(out, d)
	}
	s.markPackageDirty(id)
	s.kick()
	return out, nil
}

// LinkChecker is implemented by *Service (optional for other Engine
// implementations): it backs POST /api/v1/links/known, used by the desktop
// clipboard monitor so that "ask" mode does not prompt for links that are
// already queued, downloaded or in the LinkGrabber.
type LinkChecker interface {
	KnownLinks(ctx context.Context, urls []string) ([]string, error)
}

var _ LinkChecker = (*Service)(nil)

// KnownLinks returns the subset of urls (normalized like AddLinks) that is
// already in the queue, the history (completed) or the LinkGrabber.
func (s *Service) KnownLinks(ctx context.Context, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return []string{}, nil
	}
	entries, err := s.st.ListGrabberEntries(ctx)
	if err != nil {
		return nil, err
	}
	inGrabber := make(map[string]bool, len(entries))
	for _, e := range entries {
		inGrabber[e.URL] = true
	}
	norm := make([]string, 0, len(urls))
	refs := make([]store.DownloadRef, 0, len(urls))
	seen := map[string]bool{}
	for _, raw := range urls {
		u := linkgrabber.NormalizeURL(raw)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		norm = append(norm, u)
		refs = append(refs, store.DownloadRef{URL: u})
	}
	known, err := s.st.KnownLinks(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for i, u := range norm {
		if known[i] || inGrabber[u] {
			out = append(out, u)
		}
	}
	return out, nil
}

// retryFailedEntry restarts the listing of the LinkGrabber entry with url
// if it is in the error state.
func (s *Service) retryFailedEntry(ctx context.Context, url string) (GrabberEntry, bool) {
	entries, err := s.st.ListGrabberEntries(ctx)
	if err != nil {
		return GrabberEntry{}, false
	}
	for _, r := range entries {
		if r.URL != url || r.State != string(GrabberError) {
			continue
		}
		st, empty := string(GrabberPending), ""
		if err := s.st.UpdateGrabberEntry(ctx, r.ID, store.GrabberEntryPatch{State: &st, Error: &empty}); err != nil {
			return GrabberEntry{}, false
		}
		r.State, r.Error = st, ""
		e := s.grabberView(ctx, r, false)
		s.bus.Publish(Event{Type: EventGrabberUpdated, Grabber: &e})
		s.startResolve(r.ID)
		return e, true
	}
	return GrabberEntry{}, false
}
