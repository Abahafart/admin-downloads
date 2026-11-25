package downloader

import (
	"bufio"
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/hosters"
)

// Defaults used when Options leaves a field at zero.
const (
	DefaultMaxConns       = 8
	DefaultMinSegmentSize = 1 << 20
	DefaultFlushInterval  = time.Second
	DefaultRetries        = 3
	chunkSize             = 64 << 10
	peekSize              = 4 << 10
)

// Options configures one Download run.
type Options struct {
	// Hoster classifies responses (nil = hosters.DefaultClassify).
	Hoster hosters.Hoster
	// Spec is the direct download (required).
	Spec *hosters.DownloadSpec
	// Client performs the requests (Spec.Client overrides it). Required
	// unless Spec.Client is set.
	Client *http.Client
	// PartPath is the .part file (created if missing).
	PartPath string
	// Resume is the saved progress (nil = start from zero). It is ignored
	// (and the download restarts) if it does not match the remote file.
	Resume *Progress
	// MaxConns caps parallel segments (0 = DefaultMaxConns). The caller
	// applies hoster caps / Spec.MaxConns before.
	MaxConns int
	// MinSegmentSize avoids splitting below this size.
	MinSegmentSize int64
	// Limiters are applied to every byte (global, per download...). nil
	// entries are ignored.
	Limiters []*rate.Limiter
	// FlushInterval is how often progress is fsynced and persisted.
	FlushInterval time.Duration
	// Persist stores the progress; it is called after the .part file was
	// fsynced, never with bytes that are not on disk. Errors are logged.
	Persist func(ctx context.Context, p Progress) error
	// Sidecar, if non-nil, makes the downloader maintain <PartPath>.adesc.
	Sidecar *SidecarInfo
	// Retries is the number of consecutive transient failures tolerated per
	// segment before the run fails (0 = DefaultRetries; < 0 = none).
	Retries int
	// Backoff / MaxBackoff drive the retry wait (exponential + jitter).
	Backoff, MaxBackoff time.Duration
	// Log receives debug/warn messages (never URLs).
	Log *slog.Logger
}

// Download is one run over one DownloadSpec. Create it with New and call Run
// once; Written / Conns / Size may be called concurrently for live progress.
type Download struct {
	opts   Options
	client *http.Client
	log    *slog.Logger
	chunk  int

	mu        sync.Mutex
	segs      []*seg
	remote    Remote
	resumable bool
	identity  bool // remote identity known (If-Range can be sent)

	f         *os.File
	flushMu   sync.Mutex
	persisted *Progress

	written atomic.Int64
	conns   atomic.Int32
	size    atomic.Int64

	discarded string
}

type seg struct {
	Segment
	active bool
}

// New validates opts and returns a Download.
func New(opts Options) (*Download, error) {
	if opts.Spec == nil || opts.Spec.URL == "" {
		return nil, errors.New("downloader: missing spec URL")
	}
	if opts.PartPath == "" {
		return nil, errors.New("downloader: missing part path")
	}
	client := opts.Client
	if opts.Spec.Client != nil {
		client = opts.Spec.Client
	}
	if client == nil {
		return nil, errors.New("downloader: missing HTTP client")
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = DefaultMaxConns
	}
	if opts.MinSegmentSize <= 0 {
		opts.MinSegmentSize = DefaultMinSegmentSize
	}
	if opts.MinSegmentSize < 2*chunkSize {
		// Splitting relies on MinSegmentSize >= 2 chunks so that a split
		// point is always beyond any in-flight write (see acquire).
		opts.MinSegmentSize = 2 * chunkSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultFlushInterval
	}
	if opts.Retries == 0 {
		opts.Retries = DefaultRetries
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = time.Minute
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	d := &Download{opts: opts, client: client, log: log, chunk: chunkSize}
	for _, l := range opts.Limiters {
		if l != nil && l.Limit() != rate.Inf && l.Burst() > 0 && l.Burst() < d.chunk {
			d.chunk = l.Burst()
		}
	}
	d.size.Store(-1)
	return d, nil
}

// Written returns the bytes written so far (including resumed ones; may be
// slightly ahead of the persisted progress).
func (d *Download) Written() int64 { return d.written.Load() }

// Conns returns the number of open connections.
func (d *Download) Conns() int { return int(d.conns.Load()) }

// Size returns the remote size (-1 while unknown).
func (d *Download) Size() int64 { return d.size.Load() }

// Discarded returns why the Resume progress was thrown away ("" if it was
// used or none was given).
func (d *Download) Discarded() string { return d.discarded }

// Snapshot returns the current progress (bytes may not be fsynced yet).
func (d *Download) Snapshot() Progress {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

func (d *Download) snapshotLocked() Progress {
	p := Progress{Remote: d.remote, Resumable: d.resumable, Segments: make([]Segment, 0, len(d.segs))}
	if d.remote.Checksum != nil {
		ck := *d.remote.Checksum
		p.Remote.Checksum = &ck
	}
	for _, s := range d.segs {
		p.Segments = append(p.Segments, s.Segment)
	}
	slices.SortFunc(p.Segments, func(a, b Segment) int { return int(a.Start - b.Start) })
	return p
}

// Run downloads until the file is complete, an error stops it, or ctx is
// canceled. It always flushes (fsync + Persist) before returning and
// returns the progress at that point.
func (d *Download) Run(ctx context.Context) (Progress, error) {
	f, err := os.OpenFile(d.opts.PartPath, os.O_RDWR|os.O_CREATE, 0o644) //nolint:gosec // G302: downloads are user files and must stay readable like any other download
	if err != nil {
		return Progress{}, hosters.NewFatal(fmt.Errorf("open part file: %w", err))
	}
	d.f = f
	defer f.Close()

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var first *response
	if !d.loadResume() {
		first, err = d.startFresh(runCtx)
		if err != nil {
			d.flush(context.WithoutCancel(ctx))
			return d.Snapshot(), err
		}
	}
	d.flush(ctx)

	// Flusher.
	flushDone := make(chan struct{})
	stopFlush := make(chan struct{})
	go func() {
		defer close(flushDone)
		t := time.NewTicker(d.opts.FlushInterval)
		defer t.Stop()
		for {
			select {
			case <-stopFlush:
				return
			case <-t.C:
				d.flush(runCtx)
			}
		}
	}()

	workers := d.opts.MaxConns
	if !d.resumable {
		workers = 1
	}
	var wg sync.WaitGroup
	var errOnce sync.Once
	var runErr error
	fail := func(err error) {
		errOnce.Do(func() {
			runErr = err
			cancel(err)
		})
	}
	for i := 0; i < workers; i++ {
		var r *response
		if i == 0 {
			r = first
		}
		wg.Add(1)
		go func(r *response) {
			defer wg.Done()
			if err := d.worker(runCtx, r); err != nil {
				fail(err)
			}
		}(r)
	}
	wg.Wait()
	close(stopFlush)
	<-flushDone

	if runErr == nil && ctx.Err() != nil {
		runErr = ctx.Err()
	}
	if runErr != nil && errors.Is(runErr, context.Canceled) && ctx.Err() != nil {
		runErr = context.Cause(ctx)
		if runErr == nil {
			runErr = ctx.Err()
		}
	}

	d.mu.Lock()
	if runErr == nil {
		// Open-ended stream: the size is what we got.
		for _, s := range d.segs {
			if s.End < 0 {
				runErr = ErrIncomplete
			}
		}
		if d.remote.Size < 0 {
			var n int64
			for _, s := range d.segs {
				n += s.Done
			}
			d.remote.Size = n
			d.size.Store(n)
		}
	}
	d.mu.Unlock()
	if runErr == nil {
		if st, err := f.Stat(); err == nil && st.Size() != d.remote.Size {
			if err := f.Truncate(d.remote.Size); err != nil {
				runErr = hosters.NewFatal(fmt.Errorf("truncate part: %w", err))
			}
		}
	}
	d.flush(context.WithoutCancel(ctx))
	p := d.Snapshot()
	if runErr == nil && !p.Complete() {
		runErr = ErrIncomplete
	}
	return p, runErr
}

// loadResume adopts opts.Resume if it is consistent with the spec and the
// .part file. It returns false (and records why) otherwise.
func (d *Download) loadResume() bool {
	r := d.opts.Resume
	if r == nil || len(r.Segments) == 0 {
		return false
	}
	spec := d.opts.Spec
	reason := ""
	switch {
	case !r.Resumable:
		reason = "server does not support resuming"
	case spec.Size >= 0 && r.Remote.Size >= 0 && spec.Size != r.Remote.Size:
		reason = "remote size changed"
	case r.Remote.Size < 0:
		reason = "remote size unknown"
	case checksumChanged(r.Remote.Checksum, spec.Checksum):
		reason = "remote checksum changed"
	}
	if reason == "" {
		st, err := d.f.Stat()
		switch {
		case err != nil:
			reason = "part file unreadable"
		case st.Size() != r.Remote.Size:
			reason = "part file size mismatch"
		}
	}
	if reason == "" {
		for _, s := range r.Segments {
			if s.Start < 0 || s.End < s.Start || s.End > r.Remote.Size || s.Done < 0 || s.Start+s.Done > s.End {
				reason = "invalid segment map"
				break
			}
		}
	}
	if reason != "" {
		if r.Done() > 0 {
			d.discarded = reason
		}
		return false
	}
	d.remote = r.Remote
	if d.remote.Checksum == nil && spec.Checksum != nil {
		ck := *spec.Checksum
		d.remote.Checksum = &ck
	}
	d.resumable = true
	d.identity = true
	d.size.Store(r.Remote.Size)
	var done int64
	for _, s := range r.Segments {
		d.segs = append(d.segs, &seg{Segment: s})
		done += s.Done
	}
	d.written.Store(done)
	return true
}

func checksumChanged(a, b *hosters.Checksum) bool {
	return a != nil && b != nil && strings.EqualFold(a.Algo, b.Algo) && !strings.EqualFold(a.Value, b.Value)
}

// startFresh truncates the part file, sends the first request and plans the
// segments. The returned response feeds the first segment.
func (d *Download) startFresh(ctx context.Context) (*response, error) {
	if err := d.f.Truncate(0); err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("truncate part: %w", err))
	}
	spec := d.opts.Spec
	d.mu.Lock()
	d.segs = nil
	d.remote = Remote{Size: spec.Size}
	if spec.Checksum != nil {
		ck := *spec.Checksum
		d.remote.Checksum = &ck
	}
	d.mu.Unlock()
	d.written.Store(0)

	r, err := d.open(ctx, 0, -1, spec.Resumable, true)
	if err != nil {
		return nil, err
	}
	resp := r.resp
	d.mu.Lock()
	defer d.mu.Unlock()
	d.remote.ETag = resp.Header.Get("ETag")
	d.remote.LastModified = resp.Header.Get("Last-Modified")
	size := int64(-1)
	if resp.StatusCode == http.StatusPartialContent {
		_, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if ok && total >= 0 {
			size = total
			d.resumable = true
		}
	} else if resp.ContentLength >= 0 {
		size = resp.ContentLength
	}
	d.remote.Size = size
	d.size.Store(size)
	d.identity = true

	if !d.resumable {
		d.segs = []*seg{{Segment: Segment{Start: 0, End: size}, active: true}}
		r.seg = d.segs[0]
		return r, nil
	}
	if err := d.f.Truncate(size); err != nil {
		r.close()
		return nil, hosters.NewFatal(fmt.Errorf("preallocate part: %w", err))
	}
	n := int64(d.opts.MaxConns)
	if max := size / d.opts.MinSegmentSize; max < n {
		n = max
	}
	if n < 1 {
		n = 1
	}
	step := size / n
	for i := int64(0); i < n; i++ {
		start, end := i*step, (i+1)*step
		if i == n-1 {
			end = size
		}
		d.segs = append(d.segs, &seg{Segment: Segment{Start: start, End: end}})
	}
	d.segs[0].active = true
	r.seg = d.segs[0]
	return r, nil
}

// acquire hands a worker the next segment: an idle incomplete one, or the
// second half of the largest active one (dynamic splitting). It returns nil
// when there is nothing left to do for an extra connection.
func (d *Download) acquire() *seg {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.segs {
		if !s.active && !s.Complete() && s.End >= 0 {
			s.active = true
			return s
		}
	}
	if !d.resumable {
		return nil
	}
	var best *seg
	var bestRem int64
	for _, s := range d.segs {
		if !s.active || s.End < 0 {
			continue
		}
		if rem := s.End - (s.Start + s.Done); rem > bestRem {
			best, bestRem = s, rem
		}
	}
	if best == nil || bestRem < 2*d.opts.MinSegmentSize {
		return nil
	}
	// mid >= current position + MinSegmentSize >= position + 2 chunks, so
	// the owner's in-flight chunk (computed against the old End) can never
	// overlap the new segment.
	mid := best.Start + best.Done + bestRem/2
	ns := &seg{Segment: Segment{Start: mid, End: best.End}, active: true}
	best.End = mid
	d.segs = append(d.segs, ns)
	return ns
}

func (d *Download) release(s *seg) {
	d.mu.Lock()
	s.active = false
	d.mu.Unlock()
}

func (d *Download) worker(ctx context.Context, first *response) error {
	for {
		var s *seg
		if first != nil {
			s = first.seg
		} else {
			s = d.acquire()
		}
		if s == nil {
			return nil
		}
		err := d.fetchSegment(ctx, s, first)
		first = nil
		d.release(s)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
	}
}

func (d *Download) position(s *seg) (pos, end int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return s.Start + s.Done, s.End
}

// fetchSegment downloads one segment, retrying transient failures in place.
func (d *Download) fetchSegment(ctx context.Context, s *seg, first *response) error {
	failures := 0
	for {
		pos, end := d.position(s)
		if end >= 0 && pos >= end {
			if first != nil {
				first.close()
			}
			return nil
		}
		r := first
		first = nil
		var err error
		if r == nil {
			if !d.resumable && pos > 0 {
				// A stream without Range cannot continue mid-way.
				return hosters.NewTemporary(0, errors.New("connection lost on a non-resumable download"))
			}
			r, err = d.open(ctx, pos, end, d.resumable, false)
		}
		if err == nil {
			var n int64
			n, err = d.copy(ctx, s, r, pos)
			if n > 0 {
				failures = 0
			}
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if errors.Is(err, ErrRemoteChanged) || hosters.KindOf(err) != hosters.KindTemporary || !d.resumable {
			return err
		}
		failures++
		if d.opts.Retries < 0 || failures > d.opts.Retries {
			return err
		}
		wait := Backoff(failures, d.opts.Backoff, d.opts.MaxBackoff)
		if he, ok := hosters.AsError(err); ok && he.RetryAfter > wait {
			wait = he.RetryAfter
		}
		d.log.Debug("segment retry", "start", s.Start, "failures", failures, "wait", wait, "err", err)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return context.Cause(ctx)
		case <-t.C:
		}
	}
}

type response struct {
	resp  *http.Response
	body  io.Reader
	start int64
	seg   *seg
}

func (r *response) close() {
	if r != nil && r.resp != nil {
		r.resp.Body.Close()
	}
}

func (d *Download) classify(resp *http.Response, body []byte, err error) error {
	var out error
	if d.opts.Hoster != nil {
		out = d.opts.Hoster.Classify(resp, body, err)
	} else {
		out = hosters.DefaultClassify(resp, body, err)
	}
	if out == nil && err != nil {
		out = hosters.NewTemporary(0, err)
	}
	return out
}

// open sends a request for [pos, end) (end -1 = to EOF). withRange selects
// a Range request; fresh marks the very first request of a new download
// (a 200 answer then just means "no Range support").
func (d *Download) open(ctx context.Context, pos, end int64, withRange, fresh bool) (*response, error) {
	spec := d.opts.Spec
	if !spec.Expires.IsZero() && time.Now().After(spec.Expires) {
		return nil, hosters.NewExpired(errors.New("direct link expired"))
	}
	method := spec.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, spec.URL, nil)
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("build request: %w", err))
	}
	for k, vs := range spec.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if withRange {
		if end > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, end-1))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", pos))
		}
		if !fresh {
			d.mu.Lock()
			etag, lm := d.remote.ETag, d.remote.LastModified
			d.mu.Unlock()
			switch {
			case etag != "" && !strings.HasPrefix(etag, "W/"):
				req.Header.Set("If-Range", etag)
			case lm != "":
				req.Header.Set("If-Range", lm)
			}
		}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, d.classify(nil, nil, err)
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && withRange {
		resp.Body.Close()
		if fresh {
			// Typically an empty file ("bytes */0"): fetch it without Range.
			return d.open(ctx, pos, end, false, true)
		}
		return nil, fmt.Errorf("%w: HTTP 416", ErrRemoteChanged)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, peekSize))
		resp.Body.Close()
		cerr := d.classify(resp, body, nil)
		if cerr == nil {
			cerr = hosters.NewTemporary(0, fmt.Errorf("unexpected HTTP %d", resp.StatusCode))
		}
		return nil, cerr
	}
	r := &response{resp: resp, body: resp.Body, start: pos}
	// Let the plugin look at textual bodies: hosters answer 200 with an
	// HTML/JSON error page instead of the file.
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") {
		br := bufio.NewReaderSize(resp.Body, peekSize)
		peek, _ := br.Peek(peekSize)
		if cerr := d.classify(resp, peek, nil); cerr != nil {
			resp.Body.Close()
			return nil, cerr
		}
		r.body = br
	}
	if !withRange {
		return r, nil
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != pos {
			resp.Body.Close()
			return nil, hosters.NewTemporary(0, fmt.Errorf("bad Content-Range %q", resp.Header.Get("Content-Range")))
		}
		if !fresh {
			d.mu.Lock()
			size, etag := d.remote.Size, d.remote.ETag
			d.mu.Unlock()
			newTag := resp.Header.Get("ETag")
			if (size >= 0 && total >= 0 && total != size) || (etag != "" && newTag != "" && newTag != etag) {
				resp.Body.Close()
				return nil, fmt.Errorf("%w: identity mismatch", ErrRemoteChanged)
			}
		}
	case http.StatusOK:
		if !fresh {
			// If-Range failed (remote changed) or the server ignores Range.
			resp.Body.Close()
			return nil, fmt.Errorf("%w: full response to a range request", ErrRemoteChanged)
		}
	}
	return r, nil
}

// copy streams r into the part file from pos, honoring segment end
// changes made by acquire. It returns the bytes written.
func (d *Download) copy(ctx context.Context, s *seg, r *response, pos int64) (int64, error) {
	defer r.close()
	d.conns.Add(1)
	defer d.conns.Add(-1)

	var stream cipher.Stream
	if dec := d.opts.Spec.Decryptor; dec != nil {
		var err error
		stream, err = dec(pos)
		if err != nil {
			return 0, hosters.NewFatal(fmt.Errorf("decryptor at %d: %w", pos, err))
		}
	}
	buf := make([]byte, d.chunk)
	var total int64
	for {
		_, end := d.position(s)
		want := len(buf)
		if end >= 0 {
			left := end - (pos + total)
			if left <= 0 {
				return total, nil
			}
			if left < int64(want) {
				want = int(left)
			}
		}
		if err := d.wait(ctx, want); err != nil {
			return total, context.Cause(ctx)
		}
		nr, rerr := r.body.Read(buf[:want])
		if nr > 0 {
			// Re-read End: a split may have shortened the segment while we
			// were reading (never below pos+total+chunk, see acquire).
			_, end = d.position(s)
			n := int64(nr)
			at := pos + total
			if end >= 0 && at+n > end {
				n = end - at
			}
			if n > 0 {
				if stream != nil {
					stream.XORKeyStream(buf[:n], buf[:n])
				}
				if _, err := d.f.WriteAt(buf[:n], at); err != nil {
					return total, hosters.NewFatal(fmt.Errorf("write part: %w", err))
				}
				d.mu.Lock()
				s.Done += n
				d.mu.Unlock()
				d.written.Add(n)
				total += n
			}
			if end >= 0 && at+n >= end {
				return total, nil
			}
		}
		if rerr == io.EOF {
			if end < 0 {
				// Open-ended stream finished: freeze the segment.
				d.mu.Lock()
				s.End = s.Start + s.Done
				d.mu.Unlock()
				return total, nil
			}
			return total, hosters.NewTemporary(0, io.ErrUnexpectedEOF)
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return total, context.Cause(ctx)
			}
			return total, d.classify(nil, nil, rerr)
		}
	}
}

func (d *Download) wait(ctx context.Context, n int) error {
	for _, l := range d.opts.Limiters {
		if l == nil || l.Limit() == rate.Inf {
			continue
		}
		left := n
		for left > 0 {
			step := left
			if b := l.Burst(); b > 0 && step > b {
				step = b
			}
			if err := l.WaitN(ctx, step); err != nil {
				return err
			}
			left -= step
		}
	}
	return nil
}

// flush fsyncs the part file and persists the progress snapshot taken
// before the fsync, so the persisted state never claims unsynced bytes.
func (d *Download) flush(ctx context.Context) {
	d.flushMu.Lock()
	defer d.flushMu.Unlock()
	snap := d.Snapshot()
	if d.persisted != nil && d.persisted.equal(snap) {
		return
	}
	if err := d.f.Sync(); err != nil {
		d.log.Warn("fsync part file failed; progress not persisted", "err", err)
		return
	}
	if d.opts.Sidecar != nil {
		if err := WriteSidecar(d.opts.PartPath, *d.opts.Sidecar, snap); err != nil {
			d.log.Warn("write sidecar failed", "err", err)
		}
	}
	if d.opts.Persist != nil {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := d.opts.Persist(pctx, snap)
		cancel()
		if err != nil {
			d.log.Warn("persist progress failed", "err", err)
			return
		}
	}
	d.persisted = &snap
}

// parseContentRange parses "bytes START-END/TOTAL" (TOTAL may be "*", then
// total = -1).
func parseContentRange(v string) (start, end, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "bytes ") {
		return 0, 0, 0, false
	}
	v = strings.TrimSpace(v[len("bytes "):])
	rng, tot, found := strings.Cut(v, "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if start, err = strconv.ParseInt(strings.TrimSpace(a), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if end, err = strconv.ParseInt(strings.TrimSpace(b), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if tot = strings.TrimSpace(tot); tot == "*" {
		total = -1
	} else if total, err = strconv.ParseInt(tot, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	return start, end, total, start <= end
}
