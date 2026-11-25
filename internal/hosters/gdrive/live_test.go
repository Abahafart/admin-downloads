//go:build live

package gdrive

// Live tests against Google Drive. Run with any of:
//
//	GDRIVE_LIVE_FILE=https://drive.google.com/file/d/.../view
//	GDRIVE_LIVE_BIGFILE=...      (a >100 MB file: virus-scan form)
//	GDRIVE_LIVE_FOLDER=https://drive.google.com/drive/folders/...
//	GDRIVE_LIVE_DOC=https://docs.google.com/document/d/.../edit
//	GDRIVE_API_KEY=...           (API key mode)
//	GDRIVE_OAUTH_TOKEN='{"access_token":...,"refresh_token":...}'
//	GDRIVE_CLIENT_ID=... GDRIVE_CLIENT_SECRET=...
//	GDRIVE_LIVE_QUOTA_FILE=...   (a file over its quota: exercises the copy)
//
//	go test -tags live -run Live -v ./internal/hosters/gdrive/

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

func liveHoster(t *testing.T, opts map[string]string) *Hoster {
	hh, err := New(config.HosterConfig{Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	return hh.(*Hoster)
}

func liveSessions(t *testing.T) map[string]func() (*Hoster, *hosters.Session) {
	out := map[string]func() (*Hoster, *hosters.Session){
		"anonymous": func() (*Hoster, *hosters.Session) { return liveHoster(t, nil), anon() },
	}
	if k := os.Getenv("GDRIVE_API_KEY"); k != "" {
		out["apikey"] = func() (*Hoster, *hosters.Session) { return liveHoster(t, map[string]string{"api_key": k}), anon() }
	}
	if raw := os.Getenv("GDRIVE_OAUTH_TOKEN"); raw != "" {
		var tok oauth2.Token
		if err := json.Unmarshal([]byte(raw), &tok); err != nil {
			t.Fatal(err)
		}
		out["oauth"] = func() (*Hoster, *hosters.Session) {
			s, _ := oauthSession(t, &tok)
			return liveHoster(t, map[string]string{
				"client_id": os.Getenv("GDRIVE_CLIENT_ID"), "client_secret": os.Getenv("GDRIVE_CLIENT_SECRET"),
			}), s
		}
	}
	return out
}

func TestLiveLinks(t *testing.T) {
	for _, env := range []string{"GDRIVE_LIVE_FILE", "GDRIVE_LIVE_BIGFILE", "GDRIVE_LIVE_FOLDER", "GDRIVE_LIVE_DOC"} {
		link := os.Getenv(env)
		if link == "" {
			continue
		}
		for mode, mk := range liveSessions(t) {
			t.Run(env+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				h, s := mk()
				res, err := h.ResolveStream(ctx, s, link, func(f hosters.FileInfo) { t.Logf("%s %q (%d)", f.ID, f.RelPath, f.Size) })
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("name=%q folder=%v files=%d", res.Name, res.IsFolder, len(res.Files))
				if len(res.Files) == 0 {
					return
				}
				spec, err := h.Prepare(ctx, s, res.Files[0])
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("size=%d resumable=%v", spec.Size, spec.Resumable)
			})
		}
	}
}

func TestLiveCopyWorkaround(t *testing.T) {
	link := os.Getenv("GDRIVE_LIVE_QUOTA_FILE")
	mk, ok := liveSessions(t)["oauth"]
	if link == "" || !ok {
		t.Skip("GDRIVE_LIVE_QUOTA_FILE / GDRIVE_OAUTH_TOKEN not set")
	}
	ctx := context.Background()
	h, s := mk()
	res, err := h.Resolve(ctx, s, link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Prepare(ctx, s, res.Files[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.Cleanup(ctx, s, res.Files[0]); err != nil {
		t.Fatal(err)
	}
}

func TestLiveCheckAccount(t *testing.T) {
	mk, ok := liveSessions(t)["oauth"]
	if !ok {
		t.Skip("GDRIVE_OAUTH_TOKEN not set")
	}
	h, s := mk()
	res, err := h.CheckAccount(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s quota=%d/%d", res.Status, res.QuotaUsed, res.QuotaTotal)
}

func TestLiveSelfTest(t *testing.T) {
	if err := liveHoster(t, nil).SelfTest(context.Background(), anon()); err != nil {
		t.Fatal(err)
	}
}
