package core

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/hosters/generic"
	"admindescargas/internal/store"
)

// TestRecoverFromSidecarWithoutDB loses the database mid-download; adding
// the same link again resumes from the .part.adesc sidecar.
func TestRecoverFromSidecarWithoutDB(t *testing.T) {
	h := newHarness(t)
	h.fs.bps.Store(1 << 20)
	data := randBytes(2 << 20)
	d := h.addFile("side.bin", data)
	waitFor(t, "persisted progress", 10*time.Second, func() bool {
		segs, _ := h.st.ListSegments(context.Background(), d.ID)
		for _, s := range segs {
			if s.Done > 200<<10 {
				return true
			}
		}
		return false
	})
	h.stop()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(h.dbPath + suffix)
	}
	h.fs.bps.Store(0)
	reqs := len(h.fs.requests())
	h.start()
	d2 := h.addFile("side.bin", data)
	got := h.waitState(d2.ID, StateCompleted, 10*time.Second)
	if fileSHA(t, got.DestPath) != sha(data) {
		t.Fatal("file differs")
	}
	for _, r := range h.fs.requests()[reqs:] {
		if r.Path == "/side.bin" && r.Range == "bytes=0-" {
			t.Fatal("sidecar ignored: restarted from zero")
		}
	}
}

func TestGenericHosterEndToEnd(t *testing.T) {
	data := randBytes(1<<20 + 99)
	mux := http.NewServeMux()
	mux.HandleFunc("/dl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="fallback.bin"; filename*=UTF-8''Pel%C3%ADcula%20final.mkv`)
		w.Header().Set("ETag", `"g"`)
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(string(data)))
	})
	srv := httptestServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg := testConfig(dir)
	st, err := store.Open(context.Background(), filepath.Join(dir, "db", store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	gh, err := generic.New(config.HosterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	reg := hosters.NewRegistry()
	reg.Register(gh)
	eng, err := New(Deps{Store: st, Registry: reg, Config: cfg, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	if _, err := eng.AddLinks(context.Background(), AddLinksRequest{Text: "bajar " + srv.URL + "/dl?id=" + strconv.Itoa(7), AutoConfirm: true}); err != nil {
		t.Fatal(err)
	}
	var d Download
	waitFor(t, "completed", 10*time.Second, func() bool {
		dls, _ := eng.ListDownloads(context.Background(), DownloadFilter{})
		if len(dls) == 1 && dls[0].State == StateCompleted {
			d = dls[0]
			return true
		}
		if len(dls) == 1 && dls[0].State == StateFailed {
			t.Fatalf("failed: %s", dls[0].Error)
		}
		return false
	})
	if d.Name != "Película final.mkv" || d.DestPath != filepath.Join(cfg.DownloadDir, "Película final.mkv") {
		t.Fatalf("name %q dest %q", d.Name, d.DestPath)
	}
	if fileSHA(t, d.DestPath) != sha(data) {
		t.Fatal("file differs")
	}
}
