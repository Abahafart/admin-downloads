//go:build live

package mega

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

// Live tests against the real MEGA service:
//
//	MEGA_LIVE_FILE=https://mega.nz/file/...   MEGA_LIVE_FOLDER=https://mega.nz/folder/...
//	MEGA_LIVE_EMAIL=... MEGA_LIVE_PASSWORD=... [MEGA_LIVE_MFA=123456]
//	go test -tags live -run Live ./internal/hosters/mega/
func liveSession(t *testing.T) *hosters.Session {
	s := hosters.NewSession(netx.NewDirect(), nil, nil)
	s.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	return s
}

func TestLiveFile(t *testing.T) {
	link := os.Getenv("MEGA_LIVE_FILE")
	if link == "" {
		t.Skip("MEGA_LIVE_FILE not set")
	}
	h, _ := New(config.HosterConfig{})
	p := h.(*Plugin)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := liveSession(t)
	res, err := p.Resolve(ctx, s, link)
	if err != nil {
		t.Fatal(err)
	}
	f := res.Files[0]
	t.Logf("name=%q size=%d", f.Name, f.Size)
	spec, err := p.Prepare(ctx, s, f)
	if err != nil {
		t.Fatal(err)
	}
	// Fetch the first 64 KiB with a Range header and decrypt from offset 16.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	req.Header.Set("Range", "bytes=16-65551")
	resp, err := s.Client.Do(req)
	if err != nil {
		t.Fatal(p.Classify(nil, nil, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if err := p.Classify(resp, body, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("range status=%d len=%d", resp.StatusCode, len(body))
	stream, _ := spec.Decryptor(16)
	stream.XORKeyStream(body, body)
}

func TestLiveFolder(t *testing.T) {
	link := os.Getenv("MEGA_LIVE_FOLDER")
	if link == "" {
		t.Skip("MEGA_LIVE_FOLDER not set")
	}
	h, _ := New(config.HosterConfig{})
	res, err := h.Resolve(context.Background(), liveSession(t), link)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("folder %q: %d files", res.Name, len(res.Files))
	for i, f := range res.Files {
		if i < 20 {
			t.Logf("  %s (%d)", f.RelPath, f.Size)
		}
	}
}

func TestLiveAccount(t *testing.T) {
	email, pw := os.Getenv("MEGA_LIVE_EMAIL"), os.Getenv("MEGA_LIVE_PASSWORD")
	if email == "" || pw == "" {
		t.Skip("MEGA_LIVE_EMAIL / MEGA_LIVE_PASSWORD not set")
	}
	h, _ := New(config.HosterConfig{})
	acct := &accounts.Account{ID: "live", Hoster: Name, Kind: accounts.KindPassword, Username: email}
	s := hosters.NewSession(netx.NewDirect(), acct, nil)
	store := accounts.NewMemoryStore()
	sec := accounts.Secret{Password: pw}
	if code := os.Getenv("MEGA_LIVE_MFA"); code != "" {
		sec.Extra = map[string]string{SecretExtraMFA: code}
	}
	_ = store.Set(context.Background(), acct.ID, sec)
	s.Secrets = store
	r, err := h.(hosters.AccountChecker).CheckAccount(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s msg=%q premium=%v quota=%d/%d", r.Status, r.Message, r.Premium, r.QuotaUsed, r.QuotaTotal)
}
