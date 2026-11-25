//go:build live

package onedrive

import (
	"context"
	"os"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

// Run with: ADESC_LIVE_ONEDRIVE_URL=<public link> go test -tags live ./internal/hosters/onedrive
// It resolves the link and prepares the first file (no download).
func TestLiveResolve(t *testing.T) {
	link := os.Getenv("ADESC_LIVE_ONEDRIVE_URL")
	if link == "" {
		t.Skip("ADESC_LIVE_ONEDRIVE_URL not set")
	}
	h, err := New(config.HosterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s := hosters.NewSession(netx.NewDirect(), nil, nil)
	res, err := h.Resolve(ctx, s, link)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Fatal("no files")
	}
	spec, err := h.Prepare(ctx, s, res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	// Never log spec.URL: it may be signed.
	t.Logf("folder=%v name=%q files=%d first=%q size=%d resumable=%v", res.IsFolder, res.Name, len(res.Files), res.Files[0].RelPath, spec.Size, spec.Resumable)
}
