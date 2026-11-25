//go:build live

package mediafire

// Live tests against www.mediafire.com. Run with:
//
//	MEDIAFIRE_LIVE_FILE=https://www.mediafire.com/file/... \
//	MEDIAFIRE_LIVE_FOLDER=https://www.mediafire.com/folder/... \
//	[MEDIAFIRE_LIVE_COOKIES="$(cat cookies.txt)"] \
//	go test -tags live -run Live -v ./internal/hosters/mediafire/

import (
	"context"
	"os"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

func liveHoster(t *testing.T) *Hoster {
	hh, err := New(config.HosterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return hh.(*Hoster)
}

func TestLiveFile(t *testing.T) {
	link := os.Getenv("MEDIAFIRE_LIVE_FILE")
	if link == "" {
		t.Skip("MEDIAFIRE_LIVE_FILE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h, s := liveHoster(t), anonSession()
	res, err := h.Resolve(ctx, s, link)
	if err != nil {
		t.Fatal(err)
	}
	f := res.Files[0]
	t.Logf("name=%q size=%d checksum=%v", f.Name, f.Size, f.Checksum != nil)
	spec, err := h.Prepare(ctx, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL == "" {
		t.Fatal("empty direct link")
	}
}

func TestLiveFolder(t *testing.T) {
	link := os.Getenv("MEDIAFIRE_LIVE_FOLDER")
	if link == "" {
		t.Skip("MEDIAFIRE_LIVE_FOLDER not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := liveHoster(t).ResolveStream(ctx, anonSession(), link, func(f hosters.FileInfo) {
		t.Logf("%s (%d)", f.RelPath, f.Size)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("folder %q: %d files", res.Name, len(res.Files))
}

func TestLiveAccount(t *testing.T) {
	cookies := os.Getenv("MEDIAFIRE_LIVE_COOKIES")
	if cookies == "" {
		t.Skip("MEDIAFIRE_LIVE_COOKIES not set")
	}
	res, err := liveHoster(t).CheckAccount(context.Background(), accountSession(t, cookies))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s premium=%v", res.Status, res.Premium)
}

func TestLiveSelfTest(t *testing.T) {
	if err := liveHoster(t).SelfTest(context.Background(), anonSession()); err != nil {
		t.Fatal(err)
	}
}
