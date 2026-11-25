//go:build live

package terabox

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

// Live tests against the real site:
//
//	TERABOX_LIVE_LINK=https://www.terabox.com/s/1...  [TERABOX_LIVE_COOKIES='ndus=...']
//	go test -tags live -run Live -v ./internal/hosters/terabox/
func TestLiveShare(t *testing.T) {
	link := os.Getenv("TERABOX_LIVE_LINK")
	if link == "" {
		t.Skip("TERABOX_LIVE_LINK not set")
	}
	h, err := New(config.HosterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var acct *accounts.Account
	cookies := os.Getenv("TERABOX_LIVE_COOKIES")
	if cookies != "" {
		acct = &accounts.Account{ID: "live", Hoster: Name, Kind: accounts.KindCookie}
	}
	s := hosters.NewSession(netx.NewDirect(), acct, nil)
	s.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if acct != nil {
		st := accounts.NewMemoryStore()
		_ = st.Set(context.Background(), acct.ID, accounts.Secret{Cookies: cookies})
		s.Secrets = st
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := h.Resolve(ctx, s, link)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("share %q folder=%v files=%d", res.Name, res.IsFolder, len(res.Files))
	for i, f := range res.Files {
		if i < 20 {
			t.Logf("  %s / %s (%d bytes)", f.RelPath, f.Name, f.Size)
		}
	}
	if len(res.Files) > 0 {
		spec, err := h.Prepare(ctx, s, res.Files[0])
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("prepared: size=%d resumable=%v", spec.Size, spec.Resumable)
	}
	if acct != nil {
		r, err := h.(hosters.AccountChecker).CheckAccount(ctx, s)
		t.Logf("account: %+v %v", r, err)
	}
}
