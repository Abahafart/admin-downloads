package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
)

// globalRotation simulates rotation.Rotator's global rotation: the first
// Acquire starts it (SuspendForRotation in another goroutine, like the
// detached runGlobal) and blocks until its own context is canceled.
type globalRotation struct {
	*fakeRotation
	eng     atomic.Pointer[Service]
	started atomic.Bool
	done    chan struct{}
}

func (g *globalRotation) Acquire(ctx context.Context, req rotation.Request) (netx.Egress, error) {
	if eng := g.eng.Load(); eng != nil && g.started.CompareAndSwap(false, true) {
		go func() {
			bg := context.Background()
			_ = eng.SuspendForRotation(bg)
			time.Sleep(150 * time.Millisecond)
			_ = eng.ResumeFromRotation(bg)
			close(g.done)
		}()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return g.fakeRotation.Acquire(ctx, req)
}

// TestGlobalRotationRequeuesAcquiringDownload: the download that triggers a
// global rotation (its job is canceled while inside Acquire) goes back to
// the queue and completes; a download paused by the user stays paused.
func TestGlobalRotationRequeuesAcquiringDownload(t *testing.T) {
	gr := &globalRotation{fakeRotation: newFakeRotation(netx.NewDirect()), done: make(chan struct{})}
	h := newHarness(t, func(h *harness) { h.rot = gr })
	paused := h.addFile("paused.bin", randBytes(300<<10))
	if err := h.eng.PauseDownloads(context.Background(), []int64{paused.ID}); err != nil {
		t.Fatal(err)
	}
	h.waitState(paused.ID, StatePaused, 5*time.Second)
	data := randBytes(512 << 10)
	h.fs.put("/rot.bin", data, `"v1"`)
	entries := h.add("http://fake.test/file/rot.bin")
	var entryID int64
	for _, e := range entries {
		if e.URL == "http://fake.test/file/rot.bin" {
			entryID = e.ID
		}
	}
	// From now on the first Acquire (the download's) runs the rotation.
	gr.eng.Store(h.eng)
	pkgs := h.confirm(ConfirmRequest{Items: []ConfirmItem{{EntryID: entryID}}})
	_, dls, err := h.eng.GetPackage(context.Background(), pkgs[0].ID)
	if err != nil || len(dls) != 1 {
		t.Fatal(dls, err)
	}
	d := dls[0]
	select {
	case <-gr.done:
	case <-time.After(10 * time.Second):
		t.Fatal("global rotation never ran")
	}
	got := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("content differs")
	}
	if st := h.get(paused.ID).State; st != StatePaused {
		t.Fatalf("user-paused download is %s after the rotation", st)
	}
	if st, _ := h.eng.Stats(context.Background()); st.Rotating || st.Paused {
		t.Fatalf("stats after rotation: %+v", st)
	}
}

// blockingPost blocks the first Process until the context is canceled.
type blockingPost struct {
	mu    sync.Mutex
	calls int
	block bool
	in    chan struct{}
}

func (p *blockingPost) Process(ctx context.Context, _ PostProcessInfo, _ string) error {
	p.mu.Lock()
	p.calls++
	block := p.block
	p.mu.Unlock()
	if block {
		select {
		case p.in <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// TestPostProcessResumesAfterStop: stopping the engine during
// post-processing must not lose it; the next start runs it again without
// downloading the file again.
func TestPostProcessResumesAfterStop(t *testing.T) {
	post := &blockingPost{block: true, in: make(chan struct{}, 1)}
	h := newHarness(t, func(h *harness) { h.post = post })
	data := randBytes(256 << 10)
	d := h.addFile("pp.bin", data)
	select {
	case <-post.in:
	case <-time.After(10 * time.Second):
		t.Fatal("post-processing never started")
	}
	h.stop()
	post.mu.Lock()
	post.block = false
	post.mu.Unlock()
	reqs := len(h.fs.requests())
	h.start()
	got := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("content differs")
	}
	for _, r := range h.fs.requests()[reqs:] {
		if r.Path == "/pp.bin" {
			t.Fatalf("file downloaded again after restart: %+v", r)
		}
	}
	post.mu.Lock()
	defer post.mu.Unlock()
	if post.calls != 2 {
		t.Fatalf("post-processor called %d times, want 2", post.calls)
	}
}

// fileQuotaHoster answers a file quota on the first Prepare and offers the
// FileQuotaBypasser workaround.
type fileQuotaHoster struct{ *fakeHoster }

func (fileQuotaHoster) CanBypassFileQuota(*hosters.Session) bool { return true }

func TestFileQuotaBypassPreparesAgain(t *testing.T) {
	h := newHarness(t)
	base := h.srv.URL
	h.hoster.prepare = func(f hosters.FileInfo, n int32) (*hosters.DownloadSpec, error) {
		if n == 1 {
			return nil, hosters.NewQuota(hosters.ScopeFile, time.Hour, nil)
		}
		return &hosters.DownloadSpec{URL: base + f.Meta["path"], Size: -1, Resumable: true}, nil
	}
	// Restart with the plugin wrapped so that it implements the optional
	// interface.
	h.stop()
	h.startWith(fileQuotaHoster{h.hoster})
	data := randBytes(100 << 10)
	d := h.addFile("fq.bin", data)
	got := h.waitState(d.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) || h.hoster.prepares.Load() < 2 {
		t.Fatalf("bypass not used: prepares=%d", h.hoster.prepares.Load())
	}
}
