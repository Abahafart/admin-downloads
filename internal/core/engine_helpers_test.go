package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
	"admindescargas/internal/store"
)

// --- content server ---

type srvFile struct {
	data []byte
	etag string
}

type reqInfo struct {
	Path, Range, Query, Local string
}

// fileServer serves files with Range support and test hooks.
type fileServer struct {
	mu      sync.Mutex
	files   map[string]*srvFile
	noRange bool
	reqs    []reqInfo
	bps     atomic.Int64 // throttle (0 = none)
	// hook may answer the request itself (returns true).
	hook func(w http.ResponseWriter, r *http.Request) bool
	// cutAfter aborts responses after n bytes when it returns n > 0.
	cutAfter func(r *http.Request) int64
	served   atomic.Int64
}

func newFileServer() *fileServer { return &fileServer{files: map[string]*srvFile{}} }

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func (fs *fileServer) put(path string, data []byte, etag string) {
	fs.mu.Lock()
	fs.files[path] = &srvFile{data: data, etag: etag}
	fs.mu.Unlock()
}

func (fs *fileServer) requests() []reqInfo {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]reqInfo(nil), fs.reqs...)
}

func (fs *fileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	local := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		local = a.String()
	}
	fs.mu.Lock()
	fs.reqs = append(fs.reqs, reqInfo{Path: r.URL.Path, Range: r.Header.Get("Range"), Query: r.URL.RawQuery, Local: local})
	f := fs.files[r.URL.Path]
	noRange := fs.noRange
	fs.mu.Unlock()
	if fs.hook != nil && fs.hook(w, r) {
		return
	}
	if f == nil {
		http.NotFound(w, r)
		return
	}
	data := f.data
	w.Header().Set("ETag", f.etag)
	w.Header().Set("Content-Type", "application/octet-stream")
	start, end := int64(0), int64(len(data))-1
	status := http.StatusOK
	rh := r.Header.Get("Range")
	if ir := r.Header.Get("If-Range"); ir != "" && ir != f.etag {
		rh = ""
	}
	if !noRange && rh != "" {
		a, b, _ := strings.Cut(strings.TrimPrefix(rh, "bytes="), "-")
		start, _ = strconv.ParseInt(a, 10, 64)
		if b != "" {
			end, _ = strconv.ParseInt(b, 10, 64)
		}
		if start >= int64(len(data)) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		end = min(end, int64(len(data))-1)
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	}
	body := data[start : end+1]
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	var cut int64
	if fs.cutAfter != nil {
		cut = fs.cutAfter(r)
	}
	fl, _ := w.(http.Flusher)
	var sent int64
	for len(body) > 0 {
		n := min(16<<10, len(body))
		if cut > 0 && sent+int64(n) > cut {
			w.Write(body[:cut-sent])
			fl.Flush()
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(body[:n]); err != nil {
			return
		}
		fs.served.Add(int64(n))
		sent += int64(n)
		body = body[n:]
		if bps := fs.bps.Load(); bps > 0 {
			fl.Flush()
			time.Sleep(time.Duration(float64(n) / float64(bps) * float64(time.Second)))
		}
	}
}

// --- fake hoster ---

// fakeHoster resolves "http://fake.test/<kind>/<name>" links:
//
//	/file/<path>    one file served by the content server at <path>
//	/folder/<name>  the tree in folders[name]
type fakeHoster struct {
	base     string // content server URL
	caps     hosters.Caps
	mu       sync.Mutex
	folders  map[string][]hosters.FileInfo
	prepares atomic.Int32
	// prepare, if set, overrides the spec URL / errors.
	prepare func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error)
	// stream makes Resolve emit files one by one with a delay.
	streamDelay time.Duration
}

func (h *fakeHoster) Name() string          { return "fake" }
func (h *fakeHoster) Match(u *url.URL) bool { return u.Hostname() == "fake.test" }
func (h *fakeHoster) Caps() hosters.Caps    { return h.caps }
func (h *fakeHoster) Classify(r *http.Response, b []byte, err error) error {
	return hosters.DefaultClassify(r, b, err)
}

func (h *fakeHoster) setFolder(name string, files []hosters.FileInfo) {
	h.mu.Lock()
	if h.folders == nil {
		h.folders = map[string][]hosters.FileInfo{}
	}
	h.folders[name] = files
	h.mu.Unlock()
}

func (h *fakeHoster) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return h.ResolveStream(ctx, s, link, func(hosters.FileInfo) {})
}

func (h *fakeHoster) ResolveStream(ctx context.Context, _ *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	if len(parts) != 2 {
		return nil, hosters.NewNotFound(errors.New("bad link"))
	}
	switch parts[0] {
	case "file":
		f := hosters.FileInfo{ID: "f-" + parts[1], Name: filepath.Base(parts[1]), Size: -1, Link: link,
			Meta: map[string]string{"path": "/" + parts[1]}}
		emit(f)
		return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
	case "folder":
		h.mu.Lock()
		files, ok := h.folders[parts[1]]
		h.mu.Unlock()
		if !ok {
			return nil, hosters.NewNotFound(errors.New("no such folder"))
		}
		for _, f := range files {
			if h.streamDelay > 0 {
				select {
				case <-time.After(h.streamDelay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			emit(f)
		}
		return &hosters.ResolveResult{Name: parts[1], IsFolder: true, Files: files}, nil
	}
	return nil, hosters.NewNotFound(errors.New("bad link"))
}

func (h *fakeHoster) Prepare(_ context.Context, _ *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	n := h.prepares.Add(1)
	if h.prepare != nil {
		return h.prepare(f, n)
	}
	return &hosters.DownloadSpec{URL: h.base + f.Meta["path"], Size: -1, Resumable: true}, nil
}

func folderFile(id, rel, path string) hosters.FileInfo {
	return hosters.FileInfo{ID: id, Name: filepath.Base(rel), RelPath: rel, Size: -1,
		Link: "http://fake.test/file" + path, Meta: map[string]string{"path": path}}
}

// --- fake egresses / rotation ---

// redirectEgress dials target whatever address is requested, so two
// egresses can reach two different test servers with the same URL.
type redirectEgress struct {
	id     string
	target string
}

func (e *redirectEgress) ID() string        { return e.id }
func (e *redirectEgress) Kind() netx.Kind   { return netx.KindSOCKS5 }
func (e *redirectEgress) Scope() netx.Scope { return netx.ScopePerConnection }
func (e *redirectEgress) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, e.target)
}
func (e *redirectEgress) Rotate(context.Context) error { return netx.ErrRotateUnsupported }
func (e *redirectEgress) PublicIP(context.Context) (netip.Addr, error) {
	return netip.MustParseAddr("192.0.2.1"), nil
}
func (e *redirectEgress) Healthy() bool { return true }
func (e *redirectEgress) Close() error  { return nil }

// fakeRotation hands out egresses in order, skipping those reported over
// quota; when all are exhausted it answers with a WaitError.
type fakeRotation struct {
	mu       sync.Mutex
	egresses []netx.Egress
	quota    map[string]time.Time
	reports  []string
	inUse    map[string]int
}

func newFakeRotation(es ...netx.Egress) *fakeRotation {
	return &fakeRotation{egresses: es, quota: map[string]time.Time{}, inUse: map[string]int{}}
}

func (f *fakeRotation) Acquire(ctx context.Context, req rotation.Request) (netx.Egress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.egresses {
		if time.Now().Before(f.quota[e.ID()]) {
			continue
		}
		f.inUse[e.ID()]++
		return e, nil
	}
	return nil, &rotation.WaitError{Until: time.Now().Add(time.Hour), Reason: "all exhausted"}
}
func (f *fakeRotation) Release(_, id string) {
	f.mu.Lock()
	f.inUse[id]--
	f.mu.Unlock()
}
func (f *fakeRotation) ReportQuota(_ context.Context, hoster, id string, _ hosters.QuotaScope, ra time.Duration) {
	f.mu.Lock()
	f.quota[id] = time.Now().Add(ra)
	f.reports = append(f.reports, hoster+"/"+id)
	f.mu.Unlock()
}
func (f *fakeRotation) ReportFailure(string, error)          {}
func (f *fakeRotation) Rotate(context.Context, string) error { return nil }
func (f *fakeRotation) Get(id string) netx.Egress {
	for _, e := range f.egresses {
		if e.ID() == id {
			return e
		}
	}
	return nil
}
func (f *fakeRotation) Egresses() []rotation.EgressStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rotation.EgressStatus
	for _, e := range f.egresses {
		out = append(out, rotation.EgressStatus{ID: e.ID(), Kind: e.Kind(), Scope: e.Scope(), Healthy: true,
			PublicIP: "192.0.2.1", InUse: f.inUse[e.ID()]})
	}
	return out
}
func (f *fakeRotation) Reports() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reports...)
}

// --- engine harness ---

type harness struct {
	t      *testing.T
	dir    string
	dbPath string
	cfg    *config.Config
	st     *store.Store
	eng    *Service
	hoster *fakeHoster
	srv    *httptest.Server
	fs     *fileServer
	rot    rotation.Manager
	post   PostProcessor
}

func testConfig(dir string) *config.Config {
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.DownloadDir = filepath.Join(dir, "dl")
	cfg.Downloads.MaxActive = 3
	cfg.Downloads.MaxPerHoster = 3
	cfg.Downloads.MaxPerEgress = 0
	cfg.Downloads.ConnsPerFile = 4
	cfg.Downloads.MinSegmentSize = 128 << 10
	cfg.Downloads.FlushInterval = config.D(20 * time.Millisecond)
	cfg.Downloads.RetryBackoff = config.D(10 * time.Millisecond)
	cfg.Downloads.MaxRetryBackoff = config.D(30 * time.Millisecond)
	cfg.Downloads.MaxRetries = 3
	cfg.Hosters["fake"] = config.HosterConfig{RetryAfter: config.D(time.Hour)}
	return cfg
}

func newHarness(t *testing.T, opts ...func(*harness)) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), fs: newFileServer()}
	h.srv = httptest.NewServer(h.fs)
	t.Cleanup(h.srv.Close)
	h.cfg = testConfig(h.dir)
	h.dbPath = filepath.Join(h.cfg.DataDir, store.FileName)
	h.hoster = &fakeHoster{base: h.srv.URL, caps: hosters.Caps{Resumable: true, Folders: true}}
	for _, o := range opts {
		o(h)
	}
	h.start()
	t.Cleanup(func() { h.stop() })
	return h
}

func (h *harness) start() { h.startWith(h.hoster) }

// startWith starts the engine with hs registered instead of h.hoster.
func (h *harness) startWith(hs hosters.Hoster) {
	h.t.Helper()
	st, err := store.Open(context.Background(), h.dbPath)
	if err != nil {
		h.t.Fatal(err)
	}
	reg := hosters.NewRegistry()
	if err := reg.Register(hs); err != nil {
		h.t.Fatal(err)
	}
	eng, err := New(Deps{Store: st, Registry: reg, Rotation: h.rot, Config: h.cfg, PostProcessor: h.post, Logger: testLogger()})
	if err != nil {
		h.t.Fatal(err)
	}
	eng.progressInterval = 50 * time.Millisecond
	eng.scheduleInterval = 50 * time.Millisecond
	if err := eng.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.st, h.eng = st, eng
}

func (h *harness) stop() {
	if h.eng == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.eng.Stop(ctx); err != nil {
		h.t.Errorf("stop: %v", err)
	}
	h.st.Close()
	h.eng, h.st = nil, nil
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// add puts links in the LinkGrabber and waits until they are ready.
func (h *harness) add(links ...string) []GrabberEntry {
	h.t.Helper()
	entries, err := h.eng.AddLinks(context.Background(), AddLinksRequest{URLs: links, Source: "api"})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, e := range entries {
		id := e.ID
		waitFor(h.t, "grabber ready", 5*time.Second, func() bool {
			all, _ := h.eng.ListLinkGrabber(context.Background())
			for _, g := range all {
				if g.ID == id && g.State != GrabberPending && g.State != GrabberResolving {
					return true
				}
			}
			return false
		})
	}
	all, _ := h.eng.ListLinkGrabber(context.Background())
	return all
}

func (h *harness) confirm(req ConfirmRequest) []Package {
	h.t.Helper()
	pkgs, err := h.eng.ConfirmLinkGrabber(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return pkgs
}

// addFile adds and confirms one /file/<path> link and returns its download.
func (h *harness) addFile(path string, data []byte) Download {
	h.t.Helper()
	h.fs.put("/"+path, data, `"v1"`)
	entries := h.add("http://fake.test/file/" + path)
	var id int64
	for _, e := range entries {
		if strings.HasSuffix(e.URL, path) {
			id = e.ID
		}
	}
	pkgs := h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: id}}})
	_, dls, err := h.eng.GetPackage(context.Background(), pkgs[0].ID)
	if err != nil || len(dls) != 1 {
		h.t.Fatalf("package: %v %v", dls, err)
	}
	return dls[0]
}

func (h *harness) get(id int64) Download {
	h.t.Helper()
	d, err := h.eng.GetDownload(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *harness) waitState(id int64, want State, timeout time.Duration) Download {
	h.t.Helper()
	var d Download
	waitFor(h.t, fmt.Sprintf("download %d %s", id, want), timeout, func() bool {
		d = h.get(id)
		return d.State == want
	})
	return d
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha(b)
}

func httptestServer(h http.Handler) *httptest.Server { return httptest.NewServer(h) }

func testLogger() *slog.Logger {
	lvl := slog.LevelWarn
	if os.Getenv("ADESC_TEST_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
