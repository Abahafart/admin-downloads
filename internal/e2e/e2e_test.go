package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"admindescargas/internal/api"
	"admindescargas/internal/apiclient"
	"admindescargas/internal/core"
	"admindescargas/internal/netx"
)

// TestGenericDownloadAndRestart: a link served by a local server (generic
// plugin) completes with the right sha256, SSE reports it, and "restart"
// downloads it again from zero.
func TestGenericDownloadAndRestart(t *testing.T) {
	o := newOrigin(t)
	data := o.add(t, "a.bin", 700<<10, nil)
	e := newEnv(t, o)
	ctx := ctxT(t)

	stream, err := e.client.Events(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	completed := make(chan int64, 4)
	go func() {
		for {
			ev, err := stream.Next()
			if err != nil {
				return
			}
			if ev.Type == core.EventDownloadUpdated && ev.Download != nil && ev.Download.State == core.StateCompleted {
				completed <- ev.Download.ID
			}
		}
	}()

	if _, err := e.client.AddLinks(ctx, core.AddLinksRequest{Text: "mira esto: " + o.url("/files/a.bin") + " gracias",
		Source: "api", AutoConfirm: true}); err != nil {
		t.Fatal(err)
	}
	dls := e.waitCompleted("generic", 1, time.Minute)
	d := dls[0]
	if got := fileSHA(t, d.DestPath); got != sha(data) {
		t.Fatalf("sha256 = %s, want %s", got, sha(data))
	}
	if filepath.Dir(d.DestPath) != e.cfg.DownloadDir {
		t.Fatalf("dest %s not in download dir %s", d.DestPath, e.cfg.DownloadDir)
	}
	select {
	case id := <-completed:
		if id != d.ID {
			t.Fatalf("SSE completed id %d, want %d", id, d.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no download.updated(completed) event on SSE")
	}
	st, err := e.client.Stats(ctx)
	if err != nil || st.Completed != 1 {
		t.Fatalf("stats = %+v, %v", st, err)
	}

	// Restart from zero: the file is fetched again in full.
	before := o.servedBytes("a.bin")
	rangesBefore := len(o.rangesOf("a.bin"))
	if err := e.client.RestartDownloads(ctx, []int64{d.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Minute, "restarted download to complete again", func() (bool, string) {
		got, err := e.client.Download(ctx, d.ID)
		if err != nil {
			return false, err.Error()
		}
		return got.State == core.StateCompleted && o.servedBytes("a.bin")-before >= int64(len(data)), describe([]core.Download{got})
	})
	if fileSHA(t, d.DestPath) != sha(data) {
		t.Fatal("restarted download has the wrong content")
	}
	if r := o.rangesOf("a.bin")[rangesBefore:]; !slices.ContainsFunc(r, func(v string) bool { return v == "" || strings.HasPrefix(v, "bytes=0-") }) {
		t.Fatalf("restart did not start from offset 0: %q", r)
	}
	// (Served bytes may exceed the size: an open-ended first request keeps
	// streaming into socket buffers until the downloader closes it.)
	if n := o.servedBytes("a.bin") - before; n < int64(len(data)) {
		t.Fatalf("restart served %d bytes, want >= %d", n, len(data))
	}
}

// TestResumeAfterRestart: the core is stopped mid-download and started
// again; the download resumes from the saved offset.
func TestResumeAfterRestart(t *testing.T) {
	o := newOrigin(t)
	const size = 3 << 20
	data := o.add(t, "big.bin", size, nil)
	o.throttle.Store(1 << 20) // ~1 MiB/s per connection
	e := newEnv(t, o)
	ctx := ctxT(t)
	if _, err := e.client.AddLinks(ctx, core.AddLinksRequest{URLs: []string{o.url("/files/big.bin")}, AutoConfirm: true}); err != nil {
		t.Fatal(err)
	}
	var id int64
	waitFor(t, 30*time.Second, "partial progress", func() (bool, string) {
		dls, err := e.client.Downloads(ctx, apiclient.DownloadQuery{})
		if err != nil || len(dls) != 1 {
			return false, "no download yet"
		}
		id = dls[0].ID
		return dls[0].Done >= size/4 && dls[0].State == core.StateDownloading, describe(dls)
	})
	e.stop()
	servedAtStop := o.servedBytes("big.bin")
	if servedAtStop >= size {
		t.Fatalf("download finished before the stop (%d bytes)", servedAtStop)
	}
	part := ""
	if m, _ := filepath.Glob(filepath.Join(e.cfg.DownloadDir, "*.part")); len(m) == 1 {
		part = m[0]
	}
	if part == "" {
		t.Fatal("no .part file after stop")
	}

	o.throttle.Store(0)
	rangesBefore := len(o.rangesOf("big.bin"))
	e.start()
	dls := e.waitCompleted("generic", 1, time.Minute)
	if dls[0].ID != id || fileSHA(t, dls[0].DestPath) != sha(data) {
		t.Fatalf("resumed download wrong: %s", describe(dls))
	}
	// Every request after the restart continues a saved segment: none
	// starts again from offset 0. (Served byte counts are not compared:
	// open-ended range responses keep streaming into socket buffers until
	// the downloader closes them.)
	resumed := o.rangesOf("big.bin")[rangesBefore:]
	if len(resumed) == 0 {
		t.Fatal("no request after restart")
	}
	for _, r := range resumed {
		if r == "" || strings.HasPrefix(r, "bytes=0-") {
			t.Fatalf("download restarted from zero after the restart: %q", resumed)
		}
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf(".part left behind: %v", err)
	}
}

// TestQuotaRotationThroughSOCKS: a hoster that answers 509 after N bytes
// per egress, two local SOCKS5 proxies imported through the API: the
// download completes by rotating between direct and the proxies.
func TestQuotaRotationThroughSOCKS(t *testing.T) {
	o := newOrigin(t)
	const size = 1 << 20
	data := o.add(t, "q.bin", size, nil)
	o.quotaBudget = size * 2 / 5 // three identities are needed
	p1, dials1 := startSOCKS(t)
	p2, dials2 := startSOCKS(t)
	e := newEnv(t, o, &quotaHoster{o: o})
	ctx := ctxT(t)

	res, err := e.client.ImportProxies(ctx, "# test\nsocks5://"+p1+"\nsocks5://"+p2+"\nno es un proxy\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || len(res.Errors) != 1 {
		t.Fatalf("import = %+v", res)
	}
	waitFor(t, 30*time.Second, "imported proxies healthy", func() (bool, string) {
		list, err := e.client.Egresses(ctx)
		if err != nil {
			return false, err.Error()
		}
		healthy := 0
		for _, s := range list {
			if s.Kind != netx.KindDirect && s.Healthy {
				healthy++
			}
		}
		return healthy == 2, ""
	})

	if _, err := e.client.AddLinks(ctx, core.AddLinksRequest{URLs: []string{"http://quota.test/file/q.bin"}, AutoConfirm: true}); err != nil {
		t.Fatal(err)
	}
	dls := e.waitCompleted("quotafake", 1, time.Minute)
	if fileSHA(t, dls[0].DestPath) != sha(data) {
		t.Fatal("rotated download has the wrong content")
	}
	ids := o.identities()
	if len(ids) < 3 {
		t.Fatalf("download used egresses %v, want 3 (direct + 2 proxies)", ids)
	}
	if dials1.Load() == 0 || dials2.Load() == 0 {
		t.Fatalf("proxies not used: dials %d / %d", dials1.Load(), dials2.Load())
	}
	// The exhausted marks are visible in the egress list.
	list, err := e.client.Egresses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, s := range list {
		if !s.Exhausted["quotafake"].IsZero() {
			marked++
		}
	}
	if marked < 2 {
		t.Fatalf("only %d egresses marked as exhausted", marked)
	}
}

// TestFolderPackageWithDeselectedSubfolder: a folder link is listed into
// the LinkGrabber, confirmed without one subfolder, and the structure on
// disk matches.
func TestFolderPackageWithDeselectedSubfolder(t *testing.T) {
	o := newOrigin(t)
	tree := map[string]string{
		"Temporada 1/ep01.bin": "s1e1", "Temporada 1/ep02.bin": "s1e2",
		"Temporada 2/ep01.bin": "s2e1", "Extras/making of/clip.bin": "clip", "leeme.txt": "readme",
	}
	content := map[string][]byte{}
	for rel, name := range tree {
		content[rel] = o.add(t, name, 40<<10+len(rel), nil)
	}
	e := newEnv(t, o, &folderHoster{o: o, files: tree})
	ctx := ctxT(t)

	entries, err := e.client.AddLinks(ctx, core.AddLinksRequest{URLs: []string{"http://folder.test/f/abc"}, Source: "api"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("add = %v, %v", entries, err)
	}
	var entry core.GrabberEntry
	waitFor(t, 30*time.Second, "folder listed", func() (bool, string) {
		list, err := e.client.LinkGrabber(ctx)
		if err != nil || len(list) != 1 {
			return false, "no entry"
		}
		entry = list[0]
		return entry.State == core.GrabberReady, string(entry.State) + " " + entry.Error
	})
	if !entry.IsFolder || len(entry.Files) != len(tree) || entry.PackageName != "Serie" {
		t.Fatalf("entry = %+v", entry)
	}
	pkgs, err := e.client.ConfirmLinkGrabber(ctx, core.ConfirmRequest{Items: []core.ConfirmItem{{
		EntryID: entry.ID, Exclude: []string{"Temporada 2/"},
	}}})
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("confirm = %v, %v", pkgs, err)
	}
	e.waitCompleted("folderfake", len(tree)-1, time.Minute)
	root := filepath.Join(e.cfg.DownloadDir, "Serie")
	for rel, want := range content {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasPrefix(rel, "Temporada 2/") {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("deselected %s exists (%v)", rel, err)
			}
			continue
		}
		if fileSHA(t, p) != sha(want) {
			t.Fatalf("%s has the wrong content", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Temporada 2")); !os.IsNotExist(err) {
		t.Fatalf("deselected subfolder created: %v", err)
	}
	p, files, err := e.client.Package(ctx, pkgs[0].ID)
	if err != nil || p.FilesTotal != len(tree)-1 || p.FilesDone != len(tree)-1 || len(files) != len(tree)-1 || !p.IsFolder {
		t.Fatalf("package = %+v (%d files), %v", p, len(files), err)
	}
}

// TestPostProcessExtractsZip: a .zip is extracted next to itself after
// the download.
func TestPostProcessExtractsZip(t *testing.T) {
	o := newOrigin(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	inner := bytes.Repeat([]byte("AdminDescargas "), 4000)
	for _, n := range []string{"dentro/hola.txt", "otro.txt"} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(inner)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	o.add(t, "pack.zip", 0, buf.Bytes())
	e := newEnv(t, o)
	ctx := ctxT(t)
	if _, err := e.client.AddLinks(ctx, core.AddLinksRequest{URLs: []string{o.url("/files/pack.zip")}, AutoConfirm: true}); err != nil {
		t.Fatal(err)
	}
	e.waitCompleted("generic", 1, time.Minute)
	for _, n := range []string{"dentro/hola.txt", "otro.txt"} {
		got, err := os.ReadFile(filepath.Join(e.cfg.DownloadDir, filepath.FromSlash(n)))
		if err != nil || !bytes.Equal(got, inner) {
			t.Fatalf("extracted %s: %v", n, err)
		}
	}
}

// TestAuthAndSecrets: the token is required, foreign Origins are refused,
// extension origins work with the token, and account secrets are never
// echoed by the API.
func TestAuthAndSecrets(t *testing.T) {
	o := newOrigin(t)
	e := newEnv(t, o)
	ctx := ctxT(t)
	base := e.app.BaseURL()

	raw := func(method, path, body string, hdr map[string]string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	bearer := map[string]string{"Authorization": "Bearer " + testToken}

	if st, _ := raw("GET", "/api/v1/health", "", nil); st != 200 {
		t.Fatalf("health: %d", st)
	}
	if st, _ := raw("GET", "/api/v1/downloads", "", nil); st != 401 {
		t.Fatalf("no token: %d", st)
	}
	if st, _ := raw("GET", "/api/v1/downloads", "", map[string]string{"Authorization": "Bearer wrong"}); st != 401 {
		t.Fatalf("bad token: %d", st)
	}
	if st, _ := raw("GET", "/api/v1/downloads", "", map[string]string{"Authorization": "Bearer " + testToken, "Origin": "https://evil.example"}); st != 403 {
		t.Fatalf("bad origin: %d", st)
	}
	if st, _ := raw("GET", "/api/v1/stats", "", map[string]string{"Authorization": "Bearer " + testToken, "Origin": "chrome-extension://abcdefghijklmnop"}); st != 200 {
		t.Fatalf("extension origin with token: %d", st)
	}
	if st, _ := raw("GET", "/api/v1/stats", "", map[string]string{"Origin": "moz-extension://1234"}); st != 401 {
		t.Fatalf("extension origin without token: %d", st)
	}

	// Secret store: a vault unlocked from vault_password_file.
	ss, err := e.client.SecretsStatus(ctx)
	if err != nil || ss.Backend != "vault" || ss.Locked {
		t.Fatalf("secrets = %+v, %v", ss, err)
	}

	const secret = "pd-api-key-S3CR3T-value"
	st, body := raw("POST", "/api/v1/accounts", `{"hoster":"pixeldrain","kind":"apikey","label":"principal","api_key":"`+secret+`"}`, bearer)
	if st != 201 || strings.Contains(body, secret) || !strings.Contains(body, `"hoster":"pixeldrain"`) {
		t.Fatalf("create account: %d %s", st, body)
	}
	if st, body := raw("GET", "/api/v1/accounts", "", bearer); st != 200 || strings.Contains(body, secret) || !strings.Contains(body, "principal") {
		t.Fatalf("list accounts: %d %s", st, body)
	}
	if st, _ := raw("POST", "/api/v1/accounts", `{"hoster":"nohoster","kind":"apikey","api_key":"x"}`, bearer); st != 400 {
		t.Fatalf("unknown hoster: %d", st)
	}
	// Cookie import creates a cookie account (and updates it the 2nd time).
	cookies := "# Netscape HTTP Cookie File\n.terabox.com\tTRUE\t/\tTRUE\t4102444800\tndus\tCOOKIE-S3CR3T\n"
	acc1, err := e.client.ImportCookies(ctx, "terabox", "tb", cookies)
	if err != nil {
		t.Fatal(err)
	}
	acc2, err := e.client.ImportCookies(ctx, "terabox", "tb", cookies)
	if err != nil || acc2.ID != acc1.ID {
		t.Fatalf("second import: %v %v", acc2.ID, err)
	}
	if st, body := raw("GET", "/api/v1/accounts", "", bearer); strings.Contains(body, "COOKIE-S3CR3T") || st != 200 {
		t.Fatal("cookies echoed")
	}
	// The secret is in the vault file encrypted, not in clear anywhere in
	// the data dir.
	filepath.WalkDir(e.cfg.DataDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entries
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte(secret)) || bytes.Contains(b, []byte("COOKIE-S3CR3T")) {
			t.Errorf("secret found in clear in %s", p)
		}
		return nil
	})
	// Lock / unlock with a wrong password.
	if _, err := e.client.LockSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.UnlockSecrets(ctx, "wrong password!"); !apiclient.IsCode(err, api.CodeWrongPassword) {
		t.Fatalf("wrong master password: %v", err)
	}
	if st, err := e.client.UnlockSecrets(ctx, "e2e master password"); err != nil || st.Locked {
		t.Fatalf("unlock: %+v %v", st, err)
	}
	// Known links (desktop clipboard filter).
	known, err := e.client.KnownLinks(ctx, []string{"http://nada.example/x"})
	if err != nil || len(known) != 0 {
		t.Fatalf("known = %v, %v", known, err)
	}
}

// TestRotationErrorsOverAPI maps rotation errors to HTTP codes.
func TestRotationErrorsOverAPI(t *testing.T) {
	o := newOrigin(t)
	e := newEnv(t, o)
	ctx := ctxT(t)
	if _, err := e.client.TestEgress(ctx, "nope"); !apiclient.IsStatus(err, 404) {
		t.Fatalf("unknown egress: %v", err)
	}
	if err := e.client.Rotate(ctx, ""); !apiclient.IsStatus(err, 409) {
		t.Fatalf("global rotate without router/VPN: %v", err)
	}
	_ = context.Background()
}
