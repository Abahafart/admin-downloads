package downloader

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/hosters"
)

// rangeServer serves data with configurable misbehavior.
type rangeServer struct {
	data     []byte
	etag     string
	noRange  bool // ignore Range (always 200)
	noLength bool // chunked, no Content-Length
	// cutAfter aborts each response after that many bytes (0 = never).
	cutAfter int64
	// slowAt throttles responses that start at this offset (-1 = none).
	slowAt int64
	// bytesPerSec throttles every response (0 = unlimited).
	bytesPerSec atomic.Int64
	// override returns a status to send instead of the body (0 = normal).
	override func(r *http.Request) int

	mu     sync.Mutex
	ranges []string
	served atomic.Int64
}

func newRangeServer(t *testing.T, size int) (*rangeServer, *httptest.Server) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	rs := &rangeServer{data: data, etag: `"v1"`, slowAt: -1}
	srv := httptest.NewServer(rs)
	t.Cleanup(srv.Close)
	return rs, srv
}

func (rs *rangeServer) Ranges() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.ranges...)
}

func (rs *rangeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	rs.ranges = append(rs.ranges, r.Header.Get("Range"))
	data, etag := rs.data, rs.etag
	rs.mu.Unlock()
	if rs.override != nil {
		if code := rs.override(r); code != 0 {
			w.WriteHeader(code)
			fmt.Fprint(w, "nope")
			return
		}
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/octet-stream")
	start, end := int64(0), int64(len(data))-1
	status := http.StatusOK
	rh := r.Header.Get("Range")
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		rh = ""
	}
	if !rs.noRange {
		w.Header().Set("Accept-Ranges", "bytes")
		if rh != "" {
			spec := strings.TrimPrefix(rh, "bytes=")
			a, b, _ := strings.Cut(spec, "-")
			start, _ = strconv.ParseInt(a, 10, 64)
			if b != "" {
				end, _ = strconv.ParseInt(b, 10, 64)
			}
			if start >= int64(len(data)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= int64(len(data)) {
				end = int64(len(data)) - 1
			}
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		}
	}
	body := data[start : end+1]
	if !rs.noLength {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	fl, _ := w.(http.Flusher)
	var sent int64
	step := 32 << 10
	for len(body) > 0 {
		n := min(step, len(body))
		if rs.cutAfter > 0 && sent+int64(n) > rs.cutAfter {
			n = int(rs.cutAfter - sent)
			if n > 0 {
				w.Write(body[:n])
			}
			if fl != nil {
				fl.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(body[:n]); err != nil {
			return
		}
		rs.served.Add(int64(n))
		sent += int64(n)
		body = body[n:]
		if (rs.slowAt >= 0 && start == rs.slowAt) || rs.bytesPerSec.Load() > 0 {
			if fl != nil {
				fl.Flush()
			}
			bps := int(rs.bytesPerSec.Load())
			if bps == 0 {
				bps = 256 << 10
			}
			time.Sleep(time.Duration(float64(n) / float64(bps) * float64(time.Second)))
		}
	}
}

func sum(b []byte) [32]byte { return sha256.Sum256(b) }

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func baseOpts(t *testing.T, url string) Options {
	return Options{
		Spec:           &hosters.DownloadSpec{URL: url, Size: -1, Resumable: true},
		Client:         &http.Client{},
		PartPath:       filepath.Join(t.TempDir(), "file.bin.part"),
		MaxConns:       4,
		MinSegmentSize: 256 << 10,
		FlushInterval:  50 * time.Millisecond,
		Backoff:        10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	}
}

func run(t *testing.T, opts Options) (Progress, error) {
	t.Helper()
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.Run(ctx)
}

func TestSegmentedDownload(t *testing.T) {
	rs, srv := newRangeServer(t, 3<<20+12345)
	opts := baseOpts(t, srv.URL)
	var persists atomic.Int32
	opts.Persist = func(context.Context, Progress) error { persists.Add(1); return nil }
	p, err := run(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Complete() || !p.Resumable || p.Remote.Size != int64(len(rs.data)) || p.Remote.ETag != `"v1"` {
		t.Fatalf("progress %+v", p)
	}
	if fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("sha256 mismatch")
	}
	if len(rs.Ranges()) < 4 {
		t.Fatalf("expected >= 4 range requests, got %v", rs.Ranges())
	}
	if persists.Load() == 0 {
		t.Fatal("progress never persisted")
	}
}

func TestDynamicSplitting(t *testing.T) {
	rs, srv := newRangeServer(t, 2<<20)
	rs.slowAt = 0 // the first segment crawls; idle workers must split it
	opts := baseOpts(t, srv.URL)
	opts.MaxConns = 4
	opts.MinSegmentSize = 128 << 10
	p, err := run(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	if fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("sha256 mismatch")
	}
	if len(p.Segments) <= 4 {
		t.Fatalf("expected dynamic splits beyond 4 segments, got %d", len(p.Segments))
	}
	// Segments must tile the file exactly.
	var at int64
	for _, s := range p.Segments {
		if s.Start != at || !s.Complete() {
			t.Fatalf("bad tiling at %d: %+v", at, p.Segments)
		}
		at = s.End
	}
	if at != int64(len(rs.data)) {
		t.Fatalf("tiling ends at %d", at)
	}
}

func TestCutsAreRetried(t *testing.T) {
	rs, srv := newRangeServer(t, 1<<20+777)
	rs.cutAfter = 100 << 10
	opts := baseOpts(t, srv.URL)
	opts.Retries = 50
	if _, err := run(t, opts); err != nil {
		t.Fatal(err)
	}
	if fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("sha256 mismatch")
	}
}

func TestNoRangeSingleStream(t *testing.T) {
	for _, noLength := range []bool{false, true} {
		t.Run(fmt.Sprintf("noLength=%v", noLength), func(t *testing.T) {
			rs, srv := newRangeServer(t, 700<<10)
			rs.noRange, rs.noLength = true, noLength
			opts := baseOpts(t, srv.URL)
			p, err := run(t, opts)
			if err != nil {
				t.Fatal(err)
			}
			if p.Resumable || len(p.Segments) != 1 || p.Remote.Size != int64(len(rs.data)) {
				t.Fatalf("progress %+v", p)
			}
			if fileSum(t, opts.PartPath) != sum(rs.data) {
				t.Fatal("sha256 mismatch")
			}
			if n := len(rs.Ranges()); n != 1 {
				t.Fatalf("requests = %d", n)
			}
		})
	}
}

func TestNoRangeCutFails(t *testing.T) {
	rs, srv := newRangeServer(t, 700<<10)
	rs.noRange, rs.cutAfter = true, 100<<10
	opts := baseOpts(t, srv.URL)
	_, err := run(t, opts)
	if err == nil || !hosters.IsTemporary(err) {
		t.Fatalf("err = %v, want temporary", err)
	}
}

// TestResumeAfterCancel kills a download midway (context cancel), then
// resumes from the persisted progress and checks the file is identical and
// that the second run only fetched the missing ranges.
func TestResumeAfterCancel(t *testing.T) {
	rs, srv := newRangeServer(t, 2<<20+5)
	rs.bytesPerSec.Store(2 << 20)
	opts := baseOpts(t, srv.URL)
	opts.Sidecar = &SidecarInfo{Hoster: "test", Name: "file.bin"}
	var mu sync.Mutex
	var last Progress
	opts.Persist = func(_ context.Context, p Progress) error {
		mu.Lock()
		last = p.Clone()
		mu.Unlock()
		return nil
	}
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for d.Written() < 600<<10 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	p1, err := d.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	saved := last.Clone()
	mu.Unlock()
	if saved.Done() == 0 || saved.Done() != p1.Done() || saved.Complete() {
		t.Fatalf("saved %d vs run %d", saved.Done(), p1.Done())
	}
	// The sidecar holds the same map.
	info, sp, err := ReadSidecar(opts.PartPath)
	if err != nil || info.Hoster != "test" || sp.Done() != saved.Done() {
		t.Fatalf("sidecar %+v %d %v", info, sp.Done(), err)
	}

	rs.bytesPerSec.Store(0)
	before := rs.served.Load()
	opts.Resume = &saved
	p2, err := run(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Complete() || fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("resumed file differs")
	}
	if got, want := rs.served.Load()-before, int64(len(rs.data))-saved.Done(); got > want+int64(4*chunkSize) {
		t.Fatalf("resume transferred %d bytes, want about %d", got, want)
	}
	for _, r := range rs.Ranges()[len(rs.Ranges())-1:] {
		if r == "" || strings.HasPrefix(r, "bytes=0-") && saved.Segments[0].Done > 0 {
			t.Fatalf("resume request %q restarted from zero", r)
		}
	}
}

// TestPersistedNeverAheadOfDisk checks at every Persist call that the
// bytes claimed by each segment are really in the .part file.
func TestPersistedNeverAheadOfDisk(t *testing.T) {
	rs, srv := newRangeServer(t, 1<<20+333)
	rs.bytesPerSec.Store(4 << 20)
	opts := baseOpts(t, srv.URL)
	opts.FlushInterval = 10 * time.Millisecond
	var checks atomic.Int32
	var bad atomic.Value
	opts.Persist = func(_ context.Context, p Progress) error {
		f, err := os.Open(opts.PartPath)
		if err != nil {
			bad.Store(err.Error())
			return nil
		}
		defer f.Close()
		for _, s := range p.Segments {
			if s.Done == 0 {
				continue
			}
			buf := make([]byte, s.Done)
			if _, err := f.ReadAt(buf, s.Start); err != nil {
				bad.Store(err.Error())
				return nil
			}
			if !bytes.Equal(buf, rs.data[s.Start:s.Start+s.Done]) {
				bad.Store(fmt.Sprintf("segment %+v claims bytes not on disk", s))
			}
		}
		checks.Add(1)
		return nil
	}
	if _, err := run(t, opts); err != nil {
		t.Fatal(err)
	}
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	if checks.Load() < 3 {
		t.Fatalf("only %d checks", checks.Load())
	}
}

func TestETagChangeOnResume(t *testing.T) {
	rs, srv := newRangeServer(t, 1<<20)
	opts := baseOpts(t, srv.URL)
	if _, err := run(t, opts); err != nil {
		t.Fatal(err)
	}
	// Pretend we stopped halfway with the old ETag; the server now has v2.
	half := int64(len(rs.data) / 2)
	resume := &Progress{
		Remote:    Remote{Size: int64(len(rs.data)), ETag: `"v1"`},
		Resumable: true,
		Segments:  []Segment{{Start: 0, End: half, Done: half}, {Start: half, End: int64(len(rs.data))}},
	}
	rs.mu.Lock()
	rs.etag = `"v2"`
	rs.mu.Unlock()
	opts.Resume = resume
	_, err := run(t, opts)
	if !errors.Is(err, ErrRemoteChanged) {
		t.Fatalf("err = %v, want ErrRemoteChanged", err)
	}
	if got := rs.Ranges()[len(rs.Ranges())-1]; !strings.HasPrefix(got, "bytes=") {
		t.Fatalf("resume did not send a range: %q", got)
	}
}

func TestResumeDiscardedOnSizeChange(t *testing.T) {
	rs, srv := newRangeServer(t, 600<<10)
	opts := baseOpts(t, srv.URL)
	opts.Spec.Size = int64(len(rs.data))
	opts.Resume = &Progress{
		Remote: Remote{Size: 999}, Resumable: true,
		Segments: []Segment{{Start: 0, End: 999, Done: 10}},
	}
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.Discarded() == "" {
		t.Fatal("expected the resume state to be discarded")
	}
	if fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("sha256 mismatch")
	}
}

func TestExpiredAndQuotaStopTheRun(t *testing.T) {
	for _, tc := range []struct {
		code  int
		check func(error) bool
	}{
		{http.StatusForbidden, hosters.IsExpired},
		{509, hosters.IsQuota},
		{http.StatusNotFound, hosters.IsNotFound},
	} {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			rs, srv := newRangeServer(t, 1<<20)
			var calls atomic.Int32
			rs.override = func(r *http.Request) int {
				// The first request works; later segments fail.
				if calls.Add(1) > 1 {
					return tc.code
				}
				return 0
			}
			rs.bytesPerSec.Store(1 << 20)
			opts := baseOpts(t, srv.URL)
			var saved atomic.Pointer[Progress]
			opts.Persist = func(_ context.Context, p Progress) error { c := p.Clone(); saved.Store(&c); return nil }
			p, err := run(t, opts)
			if !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			if s := saved.Load(); s == nil || s.Done() != p.Done() {
				t.Fatal("final progress not persisted")
			}
		})
	}
}

func TestSpecExpiresBeforeRequest(t *testing.T) {
	_, srv := newRangeServer(t, 1000)
	opts := baseOpts(t, srv.URL)
	opts.Spec.Expires = time.Now().Add(-time.Minute)
	if _, err := run(t, opts); !hosters.IsExpired(err) {
		t.Fatalf("err = %v", err)
	}
}

// ctrStream returns an AES-CTR keystream positioned at an arbitrary offset,
// like the MEGA plugin's Decryptor.
func ctrStream(block cipher.Block, nonce [8]byte, offset int64) cipher.Stream {
	var iv [16]byte
	copy(iv[:8], nonce[:])
	binary.BigEndian.PutUint64(iv[8:], uint64(offset/16))
	s := cipher.NewCTR(block, iv[:])
	if skip := offset % 16; skip > 0 {
		junk := make([]byte, skip)
		s.XORKeyStream(junk, junk)
	}
	return s
}

func TestDecryptorAtArbitraryOffsets(t *testing.T) {
	plain := make([]byte, 1<<20+13)
	rand.Read(plain)
	key := make([]byte, 16)
	rand.Read(key)
	var nonce [8]byte
	rand.Read(nonce[:])
	block, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	ctrStream(block, nonce, 0).XORKeyStream(enc, plain)

	rs, srv := newRangeServer(t, 1)
	rs.data = enc
	rs.cutAfter = 70<<10 + 3 // odd cut points: resumes at non-16-aligned offsets
	opts := baseOpts(t, srv.URL)
	opts.Retries = 100
	opts.MinSegmentSize = 128<<10 + 7
	opts.Spec.Decryptor = func(off int64) (cipher.Stream, error) { return ctrStream(block, nonce, off), nil }
	if _, err := run(t, opts); err != nil {
		t.Fatal(err)
	}
	if fileSum(t, opts.PartPath) != sum(plain) {
		t.Fatal("decrypted file differs")
	}
}

func TestRateLimit(t *testing.T) {
	rs, srv := newRangeServer(t, 300<<10)
	opts := baseOpts(t, srv.URL)
	lim := rate.NewLimiter(rate.Limit(1<<20), 64<<10) // 1 MiB/s
	opts.Limiters = []*rate.Limiter{lim, nil}
	start := time.Now()
	if _, err := run(t, opts); err != nil {
		t.Fatal(err)
	}
	// 300 KiB at 1 MiB/s with a 64 KiB burst takes >= ~230 ms.
	if el := time.Since(start); el < 180*time.Millisecond {
		t.Fatalf("rate limit not applied: %v", el)
	}
	if fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("sha256 mismatch")
	}
}

func TestEmptyFile(t *testing.T) {
	_, srv := newRangeServer(t, 0)
	opts := baseOpts(t, srv.URL)
	p, err := run(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	if p.Remote.Size != 0 || !p.Complete() {
		t.Fatalf("progress %+v", p)
	}
}

func TestParseContentRange(t *testing.T) {
	for _, tc := range []struct {
		in                string
		start, end, total int64
		ok                bool
	}{
		{"bytes 0-99/1000", 0, 99, 1000, true},
		{"bytes 5-5/*", 5, 5, -1, true},
		{"bytes 10-5/100", 10, 5, 100, false},
		{"items 0-1/2", 0, 0, 0, false},
		{"bytes 0-x/2", 0, 0, 0, false},
		{"bytes */100", 0, 0, 0, false},
	} {
		s, e, tot, ok := parseContentRange(tc.in)
		if ok != tc.ok || ok && (s != tc.start || e != tc.end || tot != tc.total) {
			t.Errorf("%q = %d %d %d %v", tc.in, s, e, tot, ok)
		}
	}
}

func TestBackoff(t *testing.T) {
	for i := 1; i < 20; i++ {
		d := Backoff(i, 100*time.Millisecond, 2*time.Second)
		if d < 50*time.Millisecond || d > 2*time.Second {
			t.Fatalf("attempt %d: %v", i, d)
		}
	}
}
