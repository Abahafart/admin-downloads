//go:build live

package pixeldrain

import (
	"context"
	"os"
	"testing"
	"time"

	"admindescargas/internal/config"
)

// Run with: ADESC_LIVE_PIXELDRAIN_URL=https://pixeldrain.com/u/<id> \
// go test -tags live ./internal/hosters/pixeldrain
func TestLiveResolve(t *testing.T) {
	link := os.Getenv("ADESC_LIVE_PIXELDRAIN_URL")
	if link == "" {
		t.Skip("ADESC_LIVE_PIXELDRAIN_URL not set")
	}
	h, _ := New(config.HosterConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := anon()
	if key := os.Getenv("ADESC_LIVE_PIXELDRAIN_KEY"); key != "" {
		s = withKey(t, key)
	}
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
	t.Logf("files=%d first=%q size=%d resumable=%v", len(res.Files), res.Files[0].Name, spec.Size, spec.Resumable)
}
