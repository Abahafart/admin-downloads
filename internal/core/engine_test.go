package core

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/downloader"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/store"
)

func TestSingleFileDownload(t *testing.T) {
	h := newHarness(t)
	data := randBytes(900<<10 + 17)
	d := h.addFile("a/movie.mkv", data)
	// The scheduler may already have picked it up (queued -> resolving ...).
	if d.State == StateFailed || d.State == StatePaused || d.DestPath != filepath.Join(h.cfg.DownloadDir, "movie.mkv") {
		t.Fatalf("download %+v", d)
	}
	d = h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, d.DestPath) != sha(data) || d.Done != int64(len(data)) || d.Size != int64(len(data)) {
		t.Fatalf("bad result %+v", d)
	}
	if d.Resumable == nil || !*d.Resumable {
		t.Fatal("resumable not recorded")
	}
	for _, p := range []string{d.DestPath + PartSuffix, downloader.SidecarPath(d.DestPath + PartSuffix)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind", p)
		}
	}
	// History is written right after the state change.
	var hist []store.HistoryRow
	waitFor(t, "history row", 5*time.Second, func() bool {
		hist, _ = h.st.ListHistory(context.Background(), 10)
		return len(hist) == 1
	})
	if hist[0].Outcome != store.OutcomeCompleted {
		t.Fatalf("history %+v", hist)
	}
	pk, _, _ := h.eng.GetPackage(context.Background(), d.PackageID)
	if pk.State != StateCompleted || pk.FilesDone != 1 {
		t.Fatalf("package %+v", pk)
	}
}

func TestFolderSelectionAndPathSanitization(t *testing.T) {
	h := newHarness(t)
	files := map[string][]byte{}
	var tree []hosters.FileInfo
	for i, rel := range []string{
		"S1/ep01.mkv", "S1/ep02.mkv", "S1/extras/making.mkv", "S2/ep01.mkv",
		"../../evil.txt", "S2/CON.txt", "a:b.bin", `..\win\evil2.txt`,
	} {
		path := "/srv/" + string(rune('a'+i))
		data := randBytes(10<<10 + i)
		h.fs.put(path, data, `"x"`)
		files[rel] = data
		tree = append(tree, folderFile("id"+string(rune('a'+i)), rel, path))
	}
	h.hoster.setFolder("Show", tree)
	h.hoster.streamDelay = 5 * time.Millisecond
	entries := h.add("http://fake.test/folder/Show")
	if len(entries) != 1 || !entries[0].IsFolder || entries[0].PackageName != "Show" || len(entries[0].Files) != len(tree) {
		t.Fatalf("entry %+v", entries)
	}
	// Sanitized rel paths are what the UI shows and matches on.
	var rels []string
	for _, f := range entries[0].Files {
		rels = append(rels, f.RelPath)
	}
	for _, want := range []string{"evil.txt", "S2/_CON.txt", "a_b.bin", "win/evil2.txt"} {
		if !slices.Contains(rels, want) {
			t.Fatalf("missing sanitized %q in %v", want, rels)
		}
	}
	pkgs := h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: entries[0].ID,
		Include: []string{"S1/", "S2/", "evil.txt", "a_b.bin", "win/"}, Exclude: []string{"S1/extras/"}}}})
	if len(pkgs) != 1 || pkgs[0].FilesTotal != 7 || !pkgs[0].IsFolder {
		t.Fatalf("packages %+v", pkgs)
	}
	waitFor(t, "package completed", 15*time.Second, func() bool {
		p, _, _ := h.eng.GetPackage(context.Background(), pkgs[0].ID)
		return p.State == StateCompleted
	})
	root := filepath.Join(h.cfg.DownloadDir, "Show")
	for rel, want := range map[string]string{
		"S1/ep01.mkv": "S1/ep01.mkv", "S1/ep02.mkv": "S1/ep02.mkv", "S2/ep01.mkv": "S2/ep01.mkv",
		"../../evil.txt": "evil.txt", "S2/CON.txt": "S2/_CON.txt", "a:b.bin": "a_b.bin", `..\win\evil2.txt`: "win/evil2.txt",
	} {
		p := filepath.Join(root, filepath.FromSlash(want))
		if fileSHA(t, p) != sha(files[rel]) {
			t.Fatalf("%s wrong content", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "S1", "extras")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("excluded subfolder was downloaded")
	}
	// Nothing escaped the download directory.
	filepath.Walk(h.dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(info.Name(), "evil") && !strings.HasPrefix(p, root) {
			t.Errorf("file outside the package: %s", p)
		}
		return nil
	})
}

func TestResumeAfterEngineRestart(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(1 << 20)
	data := randBytes(3 << 20)
	d := h.addFile("big.bin", data)
	// Wait until some progress is persisted, then "kill" the engine.
	waitFor(t, "persisted progress", 10*time.Second, func() bool {
		segs, _ := h.st.ListSegments(context.Background(), d.ID)
		var done int64
		for _, s := range segs {
			done += s.Done
		}
		return done > 400<<10
	})
	h.stop()

	// Persisted progress is never ahead of the data on disk.
	st, err := store.Open(context.Background(), h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := st.GetDownload(context.Background(), d.ID)
	segs, _ := st.ListSegments(context.Background(), d.ID)
	st.Close()
	if row.State != string(StateDownloading) {
		t.Fatalf("state after stop = %s (want downloading, re-queued on start)", row.State)
	}
	part, err := os.ReadFile(d.DestPath + PartSuffix)
	if err != nil {
		t.Fatal(err)
	}
	var persisted int64
	for _, s := range segs {
		if !bytes.Equal(part[s.Start:s.Start+s.Done], data[s.Start:s.Start+s.Done]) {
			t.Fatalf("segment %+v claims bytes that are not on disk", s)
		}
		persisted += s.Done
	}
	if persisted != row.DoneBytes || persisted == 0 || persisted >= int64(len(data)) {
		t.Fatalf("persisted %d, done_bytes %d", persisted, row.DoneBytes)
	}

	h.fs.bps.Store(0)
	reqsBefore := len(h.fs.requests())
	h.start()
	got := h.waitState(d.ID, StateCompleted, 15*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("resumed file differs")
	}
	for _, r := range h.fs.requests()[reqsBefore:] {
		if r.Range == "" || r.Range == "bytes=0-" {
			t.Fatalf("resume restarted from zero: %+v", r)
		}
		a, _, _ := strings.Cut(strings.TrimPrefix(r.Range, "bytes="), "-")
		start, _ := strconv.ParseInt(a, 10, 64)
		for _, s := range segs {
			if start >= s.Start && start < s.Start+s.Done {
				t.Fatalf("request %s re-fetches persisted bytes of %+v", r.Range, s)
			}
		}
	}
}

func TestResumeOnStartDisabled(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(512 << 10)
	d := h.addFile("slow.bin", randBytes(2<<20))
	h.waitState(d.ID, StateDownloading, 5*time.Second)
	h.stop()
	h.cfg.Downloads.ResumeOnStart = false
	h.start()
	if got := h.get(d.ID); got.State != StatePaused {
		t.Fatalf("state = %s, want paused", got.State)
	}
}

func TestQuotaRotatesEgressAndResumes(t *testing.T) {
	// Two servers with the same content; egress A reaches srvA (which
	// answers 509 after the first chunk), egress B reaches srvB.
	data := randBytes(2 << 20)
	srvA := newFileServer()
	var aReqs atomic.Int32
	srvA.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if aReqs.Add(1) > 1 {
			w.WriteHeader(509)
			return true
		}
		return false
	}
	srvA.cutAfter = func(*http.Request) int64 { return 300 << 10 }
	srvA.put("/q.bin", data, `"v1"`)
	tsA := newTestServer(t, srvA)
	var rot *fakeRotation
	h := newHarness(t, func(h *harness) {
		h.fs.put("/q.bin", data, `"v1"`)
		rot = newFakeRotation(&redirectEgress{id: "A", target: tsA}, &redirectEgress{id: "B", target: h.srv.Listener.Addr().String()})
		h.rot = rot
	})
	d := h.addFile("q.bin", data)
	got := h.waitState(d.ID, StateCompleted, 15*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("file differs")
	}
	if r := rot.Reports(); len(r) == 0 || r[0] != "fake/A" {
		t.Fatalf("quota reports %v", r)
	}
	if got.EgressID != "B" || got.PublicIP != "192.0.2.1" {
		t.Fatalf("egress %q ip %q", got.EgressID, got.PublicIP)
	}
	// What A delivered before the quota was kept: B resumed, never from 0.
	if srvA.served.Load() > 0 {
		for _, r := range h.fs.requests() {
			if r.Range == "" || r.Range == "bytes=0-" {
				t.Fatalf("B restarted from zero: %+v", h.fs.requests())
			}
		}
	}
}

func TestQuotaWithoutAlternativeWaits(t *testing.T) {
	h := newHarness(t) // rotation.Static: only the direct egress
	h.fs.hook = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(509)
		return true
	}
	d := h.addFile("limited.bin", randBytes(1000))
	got := h.waitState(d.ID, StateWaiting, 5*time.Second)
	if got.ErrorKind != "quota" || time.Until(got.WaitingUntil) < 100*time.Second || time.Until(got.WaitingUntil) > 130*time.Second {
		t.Fatalf("waiting %+v (until in %v)", got, time.Until(got.WaitingUntil))
	}
	// Resume forces an immediate retry.
	h.fs.hook = nil
	if err := h.eng.ResumeDownloads(context.Background(), []int64{d.ID}); err != nil {
		t.Fatal(err)
	}
	h.waitState(d.ID, StateCompleted, 5*time.Second)
}

func TestAllEgressesExhaustedWaits(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		rot := newFakeRotation(&redirectEgress{id: "A", target: h.srv.Listener.Addr().String()})
		h.rot = rot
	})
	h.fs.hook = func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(509); return true }
	d := h.addFile("x.bin", randBytes(1000))
	got := h.waitState(d.ID, StateWaiting, 5*time.Second)
	if got.WaitingUntil.IsZero() || got.ErrorKind != "quota" {
		t.Fatalf("%+v", got)
	}
}

func TestExpiredLinkIsPreparedAgain(t *testing.T) {
	data := randBytes(1<<20 + 3)
	h := newHarness(t)
	h.fs.put("/exp.bin", data, `"e"`)
	var firstServed atomic.Bool
	h.fs.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("t") == "1" {
			if firstServed.CompareAndSwap(false, true) {
				return false // first request with token 1 works (and is cut)
			}
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	h.fs.cutAfter = func(r *http.Request) int64 {
		if r.URL.Query().Get("t") == "1" {
			return 200 << 10
		}
		return 0
	}
	h.hoster.prepare = func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error) {
		return &hosters.DownloadSpec{URL: h.srv.URL + f.Meta["path"] + "?t=" + string('0'+n), Size: -1, Resumable: true}, nil
	}
	h.fs.put("/exp.bin", data, `"e"`)
	entries := h.add("http://fake.test/file/exp.bin")
	h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: entries[0].ID}}})
	dls, _ := h.eng.ListDownloads(context.Background(), DownloadFilter{})
	got := h.waitState(dls[0].ID, StateCompleted, 10*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("file differs")
	}
	if n := h.hoster.prepares.Load(); n < 2 {
		t.Fatalf("prepares = %d", n)
	}
}

func TestETagChangeRestartsFromZero(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(1 << 20)
	old := randBytes(2 << 20)
	d := h.addFile("chg.bin", old)
	waitFor(t, "progress", 10*time.Second, func() bool { return h.get(d.ID).Done > 300<<10 })
	if err := h.eng.PauseDownloads(context.Background(), []int64{d.ID}); err != nil {
		t.Fatal(err)
	}
	if got := h.get(d.ID); got.State != StatePaused || got.Done == 0 {
		t.Fatalf("paused %+v", got)
	}
	notices := h.eng.Subscribe(t.Context())
	fresh := randBytes(2<<20 - 5)
	h.fs.put("/chg.bin", fresh, `"v2"`)
	h.fs.bps.Store(0)
	if err := h.eng.ResumeDownloads(context.Background(), []int64{d.ID}); err != nil {
		t.Fatal(err)
	}
	got := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, got.DestPath) != sha(fresh) {
		t.Fatal("file is not the new remote version")
	}
	sawNotice := false
	for !sawNotice {
		select {
		case ev := <-notices:
			if ev.Type == EventNotice && (strings.Contains(ev.Message, "cambió") || strings.Contains(ev.Message, "desde cero")) {
				sawNotice = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no restart notice")
		}
	}
}

func TestServerWithoutRange(t *testing.T) {
	h := newHarness(t)
	h.fs.noRange = true
	data := randBytes(700 << 10)
	d := h.addFile("norange.bin", data)
	got := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if got.Resumable == nil || *got.Resumable {
		t.Fatalf("resumable = %v, want false", got.Resumable)
	}
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("file differs")
	}
}

func TestPauseResumeRestart(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(1 << 20)
	data := randBytes(2 << 20)
	d := h.addFile("p.bin", data)
	waitFor(t, "progress", 10*time.Second, func() bool { return h.get(d.ID).Done > 200<<10 })
	if err := h.eng.PausePackages(context.Background(), []int64{d.PackageID}); err != nil {
		t.Fatal(err)
	}
	paused := h.get(d.ID)
	if paused.State != StatePaused || paused.Done == 0 {
		t.Fatalf("paused %+v", paused)
	}
	time.Sleep(150 * time.Millisecond)
	if again := h.get(d.ID); again.State != StatePaused {
		t.Fatal("scheduler restarted a paused download")
	}
	h.fs.bps.Store(0)
	if err := h.eng.ResumeDownloads(context.Background(), []int64{d.ID}); err != nil {
		t.Fatal(err)
	}
	done := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, done.DestPath) != sha(data) {
		t.Fatal("file differs after resume")
	}
	reqs := len(h.fs.requests())
	if err := h.eng.RestartDownloads(context.Background(), []int64{d.ID}); err != nil {
		t.Fatal(err)
	}
	re := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, re.DestPath) != sha(data) || len(h.fs.requests()) == reqs {
		t.Fatal("restart did not download again")
	}
	if r := h.fs.requests()[reqs]; r.Range != "bytes=0-" {
		t.Fatalf("restart first request %+v", r)
	}
	if err := h.eng.PauseDownloads(context.Background(), []int64{9999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v", err)
	}
}

func TestRetryFailed(t *testing.T) {
	h := newHarness(t)
	var broken atomic.Bool
	broken.Store(true)
	h.hoster.prepare = func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error) {
		if broken.Load() {
			return nil, hosters.NewNotFound(errors.New("gone"))
		}
		return &hosters.DownloadSpec{URL: h.srv.URL + f.Meta["path"], Size: -1, Resumable: true}, nil
	}
	data := randBytes(5000)
	d := h.addFile("r.bin", data)
	failed := h.waitState(d.ID, StateFailed, 5*time.Second)
	if failed.ErrorKind != "not_found" || failed.Error == "" {
		t.Fatalf("failed %+v", failed)
	}
	broken.Store(false)
	if err := h.eng.RetryAllFailed(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitState(d.ID, StateCompleted, 5*time.Second)
}

func TestTemporaryErrorsExhaustRetries(t *testing.T) {
	h := newHarness(t)
	h.fs.hook = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	d := h.addFile("t.bin", randBytes(1000))
	got := h.waitState(d.ID, StateFailed, 10*time.Second)
	if got.Attempts != h.cfg.Downloads.MaxRetries+1 || got.ErrorKind != "temporary" {
		t.Fatalf("%+v", got)
	}
}

func TestChecksumMismatchFails(t *testing.T) {
	h := newHarness(t)
	h.hoster.prepare = func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error) {
		return &hosters.DownloadSpec{URL: h.srv.URL + f.Meta["path"], Size: -1, Resumable: true,
			Checksum: &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: strings.Repeat("0", 64)}}, nil
	}
	d := h.addFile("c.bin", randBytes(3000))
	got := h.waitState(d.ID, StateFailed, 5*time.Second)
	if !strings.Contains(got.Error, "verificación") {
		t.Fatalf("%+v", got)
	}
	data := randBytes(3000)
	h.fs.put("/c2.bin", data, `"z"`)
	h.hoster.prepare = func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error) {
		return &hosters.DownloadSpec{URL: h.srv.URL + f.Meta["path"], Size: -1, Resumable: true,
			Checksum: &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: sha(data)}}, nil
	}
	d2 := h.addFile("c2.bin", data)
	h.waitState(d2.ID, StateCompleted, 5*time.Second)
}

type recordingPost struct{ calls atomic.Int32 }

func (p *recordingPost) Process(_ context.Context, info PostProcessInfo, path string) error {
	p.calls.Add(1)
	if info.Download.ID == 0 || info.Package.ID == 0 || !info.PackageComplete || path == "" {
		return errors.New("bad info")
	}
	return nil
}

func TestPostProcessor(t *testing.T) {
	post := &recordingPost{}
	h := newHarness(t, func(h *harness) { h.post = post })
	d := h.addFile("pp.bin", randBytes(2000))
	h.waitState(d.ID, StateCompleted, 5*time.Second)
	if post.calls.Load() != 1 {
		t.Fatalf("post calls = %d", post.calls.Load())
	}
}

func TestExistsPolicies(t *testing.T) {
	h := newHarness(t)
	data := randBytes(4000)
	dest := filepath.Join(h.cfg.DownloadDir, "e.bin")
	os.MkdirAll(h.cfg.DownloadDir, 0o755)
	os.WriteFile(dest, data, 0o644)
	h.hoster.prepare = func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error) {
		h.fs.mu.Lock()
		size := int64(len(h.fs.files[f.Meta["path"]].data))
		h.fs.mu.Unlock()
		return &hosters.DownloadSpec{URL: h.srv.URL + f.Meta["path"], Size: size, Resumable: true}, nil
	}
	reqs := len(h.fs.requests())
	d := h.addFile("e.bin", data)
	got := h.waitState(d.ID, StateCompleted, 5*time.Second)
	for _, r := range h.fs.requests()[reqs:] {
		if r.Path == "/e.bin" && r.Range != "" {
			t.Fatal("skip policy still downloaded the file")
		}
	}
	if got.DestPath != dest {
		t.Fatalf("dest %s", got.DestPath)
	}
	// Different content with skip policy: keep both (renamed).
	other := randBytes(5000)
	h.fs.put("/sub/e.bin", other, `"o"`)
	entries := h.add("http://fake.test/file/sub/e.bin")
	var id int64
	for _, e := range entries {
		if strings.HasSuffix(e.URL, "sub/e.bin") {
			id = e.ID
		}
	}
	pkgs := h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: id}}, ExistsPolicy: "rename"})
	_, dls, _ := h.eng.GetPackage(context.Background(), pkgs[0].ID)
	got2 := h.waitState(dls[0].ID, StateCompleted, 5*time.Second)
	if got2.DestPath != filepath.Join(h.cfg.DownloadDir, "e (1).bin") || fileSHA(t, got2.DestPath) != sha(other) {
		t.Fatalf("renamed dest %s", got2.DestPath)
	}
	if fileSHA(t, dest) != sha(data) {
		t.Fatal("original file was modified")
	}
}

func TestRescanAddsOnlyMissing(t *testing.T) {
	h := newHarness(t)
	h.fs.put("/r/1", randBytes(100), `"1"`)
	h.fs.put("/r/2", randBytes(100), `"2"`)
	h.hoster.setFolder("Grow", []hosters.FileInfo{folderFile("1", "a.bin", "/r/1"), folderFile("2", "b.bin", "/r/2")})
	entries := h.add("http://fake.test/folder/Grow")
	pkgs := h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: entries[0].ID}}})
	h.fs.put("/r/3", randBytes(100), `"3"`)
	h.hoster.setFolder("Grow", []hosters.FileInfo{folderFile("1", "a.bin", "/r/1"), folderFile("2", "b.bin", "/r/2"),
		folderFile("3", "sub/c.bin", "/r/3")})
	added, err := h.eng.RescanPackage(context.Background(), pkgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].RelPath != "sub/c.bin" || added[0].DestPath != filepath.Join(h.cfg.DownloadDir, "Grow", "sub", "c.bin") {
		t.Fatalf("added %+v", added)
	}
	again, err := h.eng.RescanPackage(context.Background(), pkgs[0].ID)
	if err != nil || len(again) != 0 {
		t.Fatalf("second rescan %v %v", again, err)
	}
	waitFor(t, "package done", 10*time.Second, func() bool {
		p, _, _ := h.eng.GetPackage(context.Background(), pkgs[0].ID)
		return p.State == StateCompleted && p.FilesTotal == 3
	})
}

func TestAddLinksDedupeAndUnsupported(t *testing.T) {
	h := newHarness(t)
	h.fs.put("/d.bin", randBytes(100), `"d"`)
	text := "mira esto: http://fake.test/file/d.bin, y esto https://unknown.example/x (no soportado)"
	got, err := h.eng.AddLinks(context.Background(), AddLinksRequest{Text: text, Source: "api"})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[1].State != GrabberUnsupported || got[0].Hoster != "fake" {
		t.Fatalf("%+v", got)
	}
	again, err := h.eng.AddLinks(context.Background(), AddLinksRequest{URLs: []string{"http://fake.test/file/d.bin"}})
	if err != nil || len(again) != 0 {
		t.Fatalf("duplicate added: %v %v", again, err)
	}
	if _, err := h.eng.AddLinks(context.Background(), AddLinksRequest{Text: "sin enlaces"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
	// Confirm, finish, then the clipboard must ignore the known link.
	waitFor(t, "ready", 5*time.Second, func() bool {
		l, _ := h.eng.ListLinkGrabber(context.Background())
		return l[0].State == GrabberReady
	})
	if _, err := h.eng.ConfirmLinkGrabber(context.Background(), ConfirmRequest{Items: []ConfirmItem{{EntryID: got[1].ID}}}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("confirm unsupported err = %v", err)
	}
	pk := h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: got[0].ID}}})
	_, dls, _ := h.eng.GetPackage(context.Background(), pk[0].ID)
	h.waitState(dls[0].ID, StateCompleted, 5*time.Second)
	clip, err := h.eng.AddLinks(context.Background(), AddLinksRequest{Text: "http://fake.test/file/d.bin", Source: "clipboard"})
	if err != nil || len(clip) != 0 {
		t.Fatalf("clipboard re-added a known link: %v %v", clip, err)
	}
	if err := h.eng.RemoveLinkGrabber(context.Background(), []int64{got[1].ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.RemoveLinkGrabber(context.Background(), []int64{424242}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamingResolverShowsTreeProgressively(t *testing.T) {
	h := newHarness(t)
	var tree []hosters.FileInfo
	for i := 0; i < 6; i++ {
		tree = append(tree, folderFile(string(rune('a'+i)), "f"+string(rune('a'+i)), "/s"))
	}
	h.hoster.setFolder("Slow", tree)
	h.hoster.streamDelay = 150 * time.Millisecond
	events := h.eng.Subscribe(t.Context())
	if _, err := h.eng.AddLinks(context.Background(), AddLinksRequest{URLs: []string{"http://fake.test/folder/Slow"}}); err != nil {
		t.Fatal(err)
	}
	partial := false
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type != EventGrabberUpdated {
				continue
			}
			if ev.Grabber.State == GrabberResolving && len(ev.Grabber.Files) > 0 && len(ev.Grabber.Files) < len(tree) {
				partial = true
			}
			if ev.Grabber.State == GrabberReady {
				if !partial {
					t.Fatal("tree only appeared at the end")
				}
				if len(ev.Grabber.Files) != len(tree) {
					t.Fatalf("files %d", len(ev.Grabber.Files))
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

func TestAutoConfirmAndDelete(t *testing.T) {
	h := newHarness(t)
	data := randBytes(3000)
	h.fs.put("/auto.bin", data, `"a"`)
	dest := filepath.Join(h.dir, "custom")
	if _, err := h.eng.AddLinks(context.Background(), AddLinksRequest{URLs: []string{"http://fake.test/file/auto.bin"},
		AutoConfirm: true, DestDir: dest}); err != nil {
		t.Fatal(err)
	}
	var d Download
	waitFor(t, "auto-confirmed download", 5*time.Second, func() bool {
		dls, _ := h.eng.ListDownloads(context.Background(), DownloadFilter{States: []State{StateCompleted}})
		if len(dls) == 1 {
			d = dls[0]
			return true
		}
		return false
	})
	if d.DestPath != filepath.Join(dest, "auto.bin") {
		t.Fatalf("dest %s", d.DestPath)
	}
	if err := h.eng.DeleteDownloads(context.Background(), []int64{d.ID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.DestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file not deleted")
	}
	if _, err := h.eng.GetDownload(context.Background(), d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if pk, _ := h.eng.ListPackages(context.Background()); len(pk) != 0 {
		t.Fatalf("empty package kept: %+v", pk)
	}
}

func TestPauseAllResumeAllAndStats(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(512 << 10)
	d := h.addFile("pa.bin", randBytes(2<<20))
	h.waitState(d.ID, StateDownloading, 5*time.Second)
	if err := h.eng.PauseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := h.eng.Stats(context.Background())
	if !st.Paused || st.Queued != 1 {
		t.Fatalf("stats %+v", st)
	}
	time.Sleep(150 * time.Millisecond)
	if got := h.get(d.ID); got.State != StateQueued {
		t.Fatalf("state %s", got.State)
	}
	h.fs.bps.Store(0)
	if err := h.eng.ResumeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitState(d.ID, StateCompleted, 10*time.Second)
	st, _ = h.eng.Stats(context.Background())
	if st.Paused || st.Completed != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestProgressEvents(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(1 << 20)
	events := h.eng.Subscribe(t.Context())
	d := h.addFile("ev.bin", randBytes(1<<20))
	var progress, stats int
	deadline := time.After(10 * time.Second)
	for progress < 2 || stats < 1 {
		select {
		case ev := <-events:
			switch ev.Type {
			case EventProgress:
				for _, p := range ev.Progress {
					if p.ID == d.ID && p.Done > 0 {
						progress++
					}
				}
			case EventStats:
				stats++
			}
		case <-deadline:
			t.Fatalf("progress %d stats %d", progress, stats)
		}
	}
}

func TestPerEgressLimit(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.cfg.Downloads.MaxPerEgress = 1
	})
	h.fs.bps.Store(2 << 20)
	a := h.addFile("l1.bin", randBytes(512<<10))
	b := h.addFile("l2.bin", randBytes(512<<10))
	maxActive := 0
	waitFor(t, "both completed", 15*time.Second, func() bool {
		n := 0
		for _, id := range []int64{a.ID, b.ID} {
			if s := h.get(id).State; s == StateDownloading || s == StateResolving {
				n++
			}
		}
		maxActive = max(maxActive, n)
		return h.get(a.ID).State == StateCompleted && h.get(b.ID).State == StateCompleted
	})
	if maxActive > 1 {
		t.Fatalf("%d downloads ran on one egress with max_per_egress=1", maxActive)
	}
}

// Compile-time checks that the fakes satisfy the contracts.
var (
	_ hosters.StreamingResolver = (*fakeHoster)(nil)
	_ netx.Egress               = (*redirectEgress)(nil)
	_                           = url.Parse
	_                           = config.ExistsSkip
)

func newTestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptestServer(h)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}
