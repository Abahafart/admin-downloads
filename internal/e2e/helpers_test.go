package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/app"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/hosters"
)

const testToken = "e2e-token-0123456789abcdef"

// --- local "internet" ------------------------------------------------------

// origin is the local HTTP server standing in for every hoster.
type origin struct {
	srv *httptest.Server

	mu    sync.Mutex
	files map[string][]byte
	// served counts body bytes written per path.
	served map[string]int64
	// ranges records the Range header of each GET per path.
	ranges map[string][]string
	// throttle (bytes/s) applied to /files/ responses; 0 = unlimited.
	throttle atomic.Int64

	// quota: bytes allowed per X-Egress identity on /quota/.
	quotaBudget int64
	quotaUsed   map[string]int64
	egressSeen  map[string]bool
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	o := &origin{files: map[string][]byte{}, served: map[string]int64{}, ranges: map[string][]string{},
		quotaUsed: map[string]int64{}, egressSeen: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /echo", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "198.51.100.7\n") })
	mux.HandleFunc("/files/{name...}", o.serveFile)
	mux.HandleFunc("/quota/{name...}", o.serveQuota)
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)
	return o
}

func (o *origin) url(path string) string { return o.srv.URL + path }

// add registers random content (or data if non-nil) at name and returns it.
func (o *origin) add(t *testing.T, name string, size int, data []byte) []byte {
	t.Helper()
	if data == nil {
		data = make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
	}
	o.mu.Lock()
	o.files[name] = data
	o.mu.Unlock()
	return data
}

func (o *origin) servedBytes(name string) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.served[name]
}

func (o *origin) rangesOf(name string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.ranges[name]...)
}

type countingWriter struct {
	http.ResponseWriter
	o    *origin
	name string
	rate int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := p
		if c.rate > 0 && len(chunk) > 16<<10 {
			chunk = chunk[:16<<10]
		}
		n, err := c.ResponseWriter.Write(chunk)
		total += n
		c.o.mu.Lock()
		c.o.served[c.name] += int64(n)
		c.o.mu.Unlock()
		if err != nil {
			return total, err
		}
		if f, ok := c.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		if c.rate > 0 {
			time.Sleep(time.Duration(float64(n) / float64(c.rate) * float64(time.Second)))
		}
		p = p[len(chunk):]
	}
	return total, nil
}

func (o *origin) serveFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	o.mu.Lock()
	data, ok := o.files[name]
	if r.Method == http.MethodGet {
		o.ranges[name] = append(o.ranges[name], r.Header.Get("Range"))
	}
	o.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", `"`+name+`-v1"`)
	cw := &countingWriter{ResponseWriter: w, o: o, name: name, rate: o.throttle.Load()}
	http.ServeContent(cw, r, name, time.Unix(1_700_000_000, 0), bytes.NewReader(data))
}

// serveQuota serves /quota/<name> with Range support, but each identity
// (X-Egress header set by the fake hoster from Session.Egress) may only
// download quotaBudget bytes: the response is cut there and later requests
// get 509.
func (o *origin) serveQuota(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	who := r.Header.Get("X-Egress")
	o.mu.Lock()
	data, ok := o.files[name]
	used := o.quotaUsed[who]
	o.egressSeen[who] = true
	o.mu.Unlock()
	if !ok || who == "" {
		http.NotFound(w, r)
		return
	}
	if used >= o.quotaBudget {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "bandwidth limit exceeded", 509)
		return
	}
	start, end := int64(0), int64(len(data)-1)
	status := http.StatusOK
	if rg := r.Header.Get("Range"); rg != "" {
		a, b, _ := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
		start, _ = strconv.ParseInt(a, 10, 64)
		if b != "" {
			end, _ = strconv.ParseInt(b, 10, 64)
		}
		end = min(end, int64(len(data)-1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		status = http.StatusPartialContent
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(status)
	allowed := min(end-start+1, o.quotaBudget-used)
	n, _ := w.Write(data[start : start+allowed])
	o.mu.Lock()
	o.quotaUsed[who] += int64(n)
	o.mu.Unlock()
	// Returning before Content-Length bytes makes the server close the
	// connection: the client sees an unexpected EOF, retries and gets 509.
}

func (o *origin) identities() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for k := range o.egressSeen {
		out = append(out, k)
	}
	return out
}

// startSOCKS runs a local SOCKS5 proxy and counts its outgoing dials.
func startSOCKS(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int64
	srv := socks5.NewServer(
		socks5.WithLogger(nopLog{}),
		socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}),
	)
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), &dials
}

type nopLog struct{}

func (nopLog) Errorf(string, ...any) {}

// --- fake hosters --------------------------------------------------------------

// quotaHoster is a plugin whose files live under /quota/ on the origin and
// which reports 509 as an IP quota.
type quotaHoster struct{ o *origin }

func (h *quotaHoster) Name() string { return "quotafake" }
func (h *quotaHoster) Match(u *url.URL) bool {
	return u.Host == "quota.test"
}
func (h *quotaHoster) Caps() hosters.Caps {
	return hosters.Caps{Resumable: true, MaxConnsPerFile: 1, QuotaScope: hosters.ScopeIP, DefaultRetryAfter: time.Hour}
}
func (h *quotaHoster) Resolve(_ context.Context, _ *hosters.Session, link string) (*hosters.ResolveResult, error) {
	name := strings.TrimPrefix(mustURL(link).Path, "/file/")
	h.o.mu.Lock()
	data, ok := h.o.files[name]
	h.o.mu.Unlock()
	if !ok {
		return nil, hosters.NewNotFound(errors.New("no such file"))
	}
	return &hosters.ResolveResult{Files: []hosters.FileInfo{{ID: name, Name: name, Size: int64(len(data)), Link: link}}}, nil
}
func (h *quotaHoster) Prepare(_ context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	return &hosters.DownloadSpec{
		URL:       h.o.url("/quota/" + f.ID),
		Header:    http.Header{"X-Egress": {s.Egress.ID()}},
		Size:      f.Size,
		Resumable: true,
	}, nil
}
func (h *quotaHoster) Classify(resp *http.Response, body []byte, err error) error {
	if resp != nil && resp.StatusCode == 509 {
		return hosters.NewQuota(hosters.ScopeIP, hosters.ParseRetryAfter(resp.Header, time.Now()), errors.New("límite de ancho de banda"))
	}
	return hosters.DefaultClassify(resp, body, err)
}

// folderHoster lists a fixed tree whose files are on the origin's /files/.
type folderHoster struct {
	o     *origin
	files map[string]string // rel path -> origin file name
}

func (h *folderHoster) Name() string          { return "folderfake" }
func (h *folderHoster) Match(u *url.URL) bool { return u.Host == "folder.test" }
func (h *folderHoster) Caps() hosters.Caps {
	return hosters.Caps{Resumable: true, Folders: true}
}
func (h *folderHoster) Resolve(_ context.Context, _ *hosters.Session, link string) (*hosters.ResolveResult, error) {
	res := &hosters.ResolveResult{Name: "Serie", IsFolder: true}
	for rel, name := range h.files {
		h.o.mu.Lock()
		size := len(h.o.files[name])
		h.o.mu.Unlock()
		base := rel[strings.LastIndex(rel, "/")+1:]
		res.Files = append(res.Files, hosters.FileInfo{ID: name, Name: base, RelPath: rel, Size: int64(size),
			Link: link + "#" + name})
	}
	return res, nil
}
func (h *folderHoster) Prepare(_ context.Context, _ *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	return &hosters.DownloadSpec{URL: h.o.url("/files/" + f.ID), Size: f.Size, Resumable: true}, nil
}
func (h *folderHoster) Classify(resp *http.Response, body []byte, err error) error {
	return hosters.DefaultClassify(resp, body, err)
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// --- the app ---------------------------------------------------------------------

type env struct {
	t      *testing.T
	o      *origin
	cfg    *config.Config
	extra  []hosters.Hoster
	app    *app.App
	client *apiclient.Client
}

func newEnv(t *testing.T, o *origin, extra ...hosters.Hoster) *env {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.DownloadDir = t.TempDir()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.Token = testToken
	pwFile := cfg.DataDir + "/vault.pw"
	if err := os.WriteFile(pwFile, []byte("e2e master password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Secrets = config.SecretsConfig{Backend: config.SecretsVault, VaultPasswordFile: pwFile}
	cfg.Log.Level = "error"
	cfg.Downloads.ConnsPerFile = 2
	cfg.Downloads.MinSegmentSize = 64 << 10
	cfg.Downloads.FlushInterval = config.D(100 * time.Millisecond)
	cfg.Downloads.RetryBackoff = config.D(50 * time.Millisecond)
	cfg.Downloads.MaxRetryBackoff = config.D(200 * time.Millisecond)
	cfg.Egress.HealthInterval = config.D(0)
	cfg.Egress.Proxies.DedupeByIP = false
	cfg.Rotation.IPCheckServices = []string{o.url("/echo")}
	cfg.PostProcess.Extract = true
	e := &env{t: t, o: o, cfg: cfg, extra: extra}
	e.start()
	t.Cleanup(func() { e.stop() })
	return e
}

func (e *env) start() {
	e.t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	a, err := app.New(e.cfg, app.WithLogger(log), app.WithHosters(e.extra...))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.app = a
	c, err := apiclient.New(a.BaseURL(), testToken)
	if err != nil {
		e.t.Fatal(err)
	}
	e.client = c
}

func (e *env) stop() {
	if e.app == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.app.Stop(ctx); err != nil {
		e.t.Errorf("stop: %v", err)
	}
	e.app = nil
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// waitFor polls cond every 50 ms until it returns true or d elapses.
func waitFor(t *testing.T, d time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(d)
	last := ""
	for time.Now().Before(deadline) {
		ok, info := cond()
		if ok {
			return
		}
		last = info
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s; last: %s", what, last)
}

func describe(dls []core.Download) string {
	var b strings.Builder
	for _, d := range dls {
		fmt.Fprintf(&b, "[%d %s %s %d/%d err=%q kind=%s scope=%s egress=%s] ", d.ID, d.Name, d.State, d.Done, d.Size,
			d.Error, d.ErrorKind, d.QuotaScope, d.EgressID)
	}
	return b.String()
}

// waitCompleted waits until every download of hoster is completed.
func (e *env) waitCompleted(hoster string, n int, d time.Duration) []core.Download {
	e.t.Helper()
	var out []core.Download
	waitFor(e.t, d, hoster+" downloads completed", func() (bool, string) {
		dls, err := e.client.Downloads(context.Background(), apiclient.DownloadQuery{Hoster: hoster})
		if err != nil {
			return false, err.Error()
		}
		if len(dls) != n {
			return false, fmt.Sprintf("%d downloads: %s", len(dls), describe(dls))
		}
		for _, dl := range dls {
			if dl.State == core.StateFailed {
				e.t.Fatalf("download failed: %s", describe(dls))
			}
			if dl.State != core.StateCompleted {
				return false, describe(dls)
			}
		}
		out = dls
		return true, ""
	})
	return out
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
