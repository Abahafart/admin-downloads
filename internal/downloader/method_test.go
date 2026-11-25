package downloader

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// TestMethodAndHeaderWithRange: DownloadSpec.Method (Dropbox uses POST) and
// Header (TeraBox UA/Referer) are sent on every request, segmented Range
// requests included.
func TestMethodAndHeaderWithRange(t *testing.T) {
	rs, srv := newRangeServer(t, 2<<20+7)
	var mu sync.Mutex
	var bad []string
	ranged := 0
	rs.override = func(r *http.Request) int {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodPost || r.Header.Get("X-Test") != "yes" || r.Header.Get("Referer") != "https://example.test/" {
			bad = append(bad, r.Method+" "+r.Header.Get("X-Test"))
			return http.StatusBadRequest
		}
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=") {
			ranged++
		}
		return 0
	}
	opts := baseOpts(t, srv.URL)
	opts.Spec.Method = http.MethodPost
	opts.Spec.Header = http.Header{"X-Test": {"yes"}, "Referer": {"https://example.test/"}}
	p, err := run(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Complete() || fileSum(t, opts.PartPath) != sum(rs.data) {
		t.Fatal("download incomplete or corrupt")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bad) > 0 || ranged < 2 {
		t.Fatalf("bad requests %q, ranged %d", bad, ranged)
	}
}
