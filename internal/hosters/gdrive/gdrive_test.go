package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

const fixtures = "../../../testdata/gdrive"

const (
	idBig     = "1BigFileAAAAAAAAAAAAAAAAAAAAAAAAA"
	idOld     = "1OldFileAAAAAAAAAAAAAAAAAAAAAAAAA"
	idSmall   = "1SmallFileAAAAAAAAAAAAAAAAAAAAAAA"
	idHot     = "1HotFileAAAAAAAAAAAAAAAAAAAAAAAAA"
	idMissing = "1MissingAAAAAAAAAAAAAAAAAAAAAAAAA"
	idRoot    = "1RootFoldAAAAAAAAAAAAAAAAAAAAAAAA"
	idSub     = "1SubFolderAAAAAAAAAAAAAAAAAAAAAAA"
	idDoc     = "1DocRootAAAAAAAAAAAAAAAAAAAAAAAAA"
	idAPIRoot = "1ApiRootAAAAAAAAAAAAAAAAAAAAAAAAA"
	idAPISub  = "1ApiSubAAAAAAAAAAAAAAAAAAAAAAAAAA"
	idAPIFile = "1ApiFileAAAAAAAAAAAAAAAAAAAAAAAAA"
	idAPIDoc  = "1ApiDocAAAAAAAAAAAAAAAAAAAAAAAAAA"
	idTarget  = "1ApiTargetAAAAAAAAAAAAAAAAAAAAAAA"
	idQuota   = "1ApiQuotaAAAAAAAAAAAAAAAAAAAAAAAA"
	idAbuse   = "1ApiAbuseAAAAAAAAAAAAAAAAAAAAAAAA"
	idCopy    = "1CopyOfQuotaAAAAAAAAAAAAAAAAAAAAA"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeGoogle emulates usercontent, drive.google.com, docs.google.com, the
// Drive API v3 and the OAuth token endpoint on one server.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	authz      []string // Authorization headers seen on /drive/v3
	apiKeys    []string // X-Goog-Api-Key headers seen
	queries    []url.Values
	copies     int
	deleted    []string
	copyExists bool
	refreshes  int
	tokenSeen  []string // refresh tokens sent to /token
}

func newFake(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{t: t}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) hoster(t *testing.T, opts map[string]string) *Hoster {
	t.Helper()
	hh, err := New(config.HosterConfig{RequestsPerSecond: 1000, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	h := hh.(*Hoster)
	h.apiBase, h.ucBase, h.driveBase, h.docsBase = f.srv.URL, f.srv.URL, f.srv.URL, f.srv.URL
	h.oauthEndpoint = oauth2.Endpoint{TokenURL: f.srv.URL + "/token", AuthURL: f.srv.URL + "/auth"}
	return h
}

func (f *fakeGoogle) html(w http.ResponseWriter, status int, name string) {
	b := strings.ReplaceAll(string(fixture(f.t, name)), "https://drive.usercontent.google.com", f.srv.URL)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	io.WriteString(w, b)
}

func (f *fakeGoogle) jsonFile(w http.ResponseWriter, status int, name string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	w.Write(fixture(f.t, name))
}

func serveFile(w http.ResponseWriter, r *http.Request, name string, size int) {
	w.Header().Set("Content-Type", "application/octet-stream")
	if name != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="x"; filename*=UTF-8''%s`, url.PathEscape(name)))
	}
	w.Header().Set("Accept-Ranges", "bytes")
	if r.Header.Get("Range") == "bytes=0-0" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", size))
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("x"))
		return
	}
	w.Write([]byte(strings.Repeat("x", size)))
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := r.URL.Path
	switch {
	case p == "/download" || p == "/uc":
		switch q.Get("id") {
		case idBig:
			if q.Get("uuid") == "" {
				f.html(w, 200, "virus_scan.html")
				return
			}
			serveFile(w, r, "big archive.zip", 1288490188)
		case idOld:
			if q.Get("confirm") != "AbCd" {
				f.html(w, 200, "virus_scan_legacy.html")
				return
			}
			serveFile(w, r, "old.iso", 734003200)
		case idSmall:
			serveFile(w, r, "ñandú.txt", 5)
		case idHot:
			f.html(w, 200, "quota_exceeded.html")
		default:
			f.html(w, 404, "not_found.html")
		}
	case p == "/file/d/"+idHot+"/view":
		f.html(w, 200, "view_page.html")
	case p == "/open":
		switch q.Get("id") {
		case idRoot:
			http.Redirect(w, r, f.srv.URL+"/drive/folders/"+idRoot, http.StatusFound)
		case idSmall:
			http.Redirect(w, r, f.srv.URL+"/file/d/"+idSmall+"/view?usp=sharing", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	case strings.HasPrefix(p, "/drive/folders/") || strings.HasPrefix(p, "/file/d/"):
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><title>x</title></html>")
	case p == "/embeddedfolderview":
		switch q.Get("id") {
		case idRoot:
			f.html(w, 200, "embedded_root.html")
		case idSub:
			if q.Get("resourcekey") != "" {
				f.t.Errorf("unexpected resourcekey for sub folder")
			}
			f.html(w, 200, "embedded_sub.html")
		default:
			f.html(w, 404, "not_found.html")
		}
	case strings.HasPrefix(p, "/document/d/") && strings.HasSuffix(p, "/export"):
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Notas.%s"`, q.Get("format")))
		io.WriteString(w, "doc")
	case p == "/token":
		f.token(w, r)
	case strings.HasPrefix(p, "/drive/v3/"):
		f.api(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGoogle) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f.mu.Lock()
	f.refreshes++
	f.tokenSeen = append(f.tokenSeen, r.Form.Get("refresh_token"))
	f.mu.Unlock()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if id != "cid" || secret != "csecret" || r.Form.Get("grant_type") != "refresh_token" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_client"}`)
		return
	}
	if r.Form.Get("refresh_token") != "REFRESH" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"access_token":"NEWACCESS","token_type":"Bearer","expires_in":3600}`)
}

func (f *fakeGoogle) api(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authz = append(f.authz, r.Header.Get("Authorization"))
	f.apiKeys = append(f.apiKeys, r.Header.Get(headerAPIKey))
	f.queries = append(f.queries, r.URL.Query())
	f.mu.Unlock()
	if a := r.Header.Get("Authorization"); a != "" && a != "Bearer GOODACCESS" && a != "Bearer NEWACCESS" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":401,"message":"Invalid Credentials","errors":[{"reason":"authError"}]}}`)
		return
	}
	if q := r.URL.Query().Get("key"); q != "" {
		f.t.Errorf("API key leaked into the URL")
	}
	q := r.URL.Query()
	rest := strings.TrimPrefix(r.URL.Path, "/drive/v3/")
	switch {
	case rest == "about":
		f.jsonFile(w, 200, "api_about.json")
	case rest == "files" && r.Method == http.MethodGet:
		f.list(w, q)
	case strings.HasSuffix(rest, "/copy") && r.Method == http.MethodPost:
		var body struct {
			Name          string            `json:"name"`
			AppProperties map[string]string `json:"appProperties"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.AppProperties[appPropCopyOf] != idQuota || body.Name != "popular.bin" {
			f.t.Errorf("copy body = %+v", body)
		}
		f.mu.Lock()
		f.copies++
		f.copyExists = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"name":"popular.bin","size":"10","md5Checksum":"abc"}`, idCopy)
	case strings.HasPrefix(rest, "files/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(rest, "files/")
		f.mu.Lock()
		exists := f.copyExists && id == idCopy
		if exists {
			f.deleted = append(f.deleted, id)
			f.copyExists = false
		}
		f.mu.Unlock()
		if !exists {
			f.jsonFile(w, 404, "api_error_notfound.json")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(rest, "files/"):
		f.file(w, r, strings.TrimPrefix(rest, "files/"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGoogle) list(w http.ResponseWriter, q url.Values) {
	query := q.Get("q")
	switch {
	case strings.Contains(query, "appProperties has"):
		f.mu.Lock()
		exists := f.copyExists
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if exists && strings.Contains(query, idQuota) {
			fmt.Fprintf(w, `{"files":[{"id":%q}]}`, idCopy)
		} else {
			io.WriteString(w, `{"files":[]}`)
		}
	case query == fmt.Sprintf("'%s' in parents and trashed = false", idAPIRoot):
		if q.Get("supportsAllDrives") != "true" || q.Get("includeItemsFromAllDrives") != "true" ||
			q.Get("corpora") != "drive" || q.Get("driveId") != "0SharedDriveAAAAAAAAA" {
			f.t.Errorf("list params = %v", q)
		}
		if q.Get("pageToken") == "PAGE2TOKEN" {
			f.jsonFile(w, 200, "api_list_root_2.json")
		} else {
			f.jsonFile(w, 200, "api_list_root_1.json")
		}
	case query == fmt.Sprintf("'%s' in parents and trashed = false", idAPISub):
		f.jsonFile(w, 200, "api_list_sub.json")
	default:
		f.t.Errorf("unexpected list query %q", query)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"files":[]}`)
	}
}

func (f *fakeGoogle) file(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	if q.Get("supportsAllDrives") != "true" {
		f.t.Errorf("files/%s without supportsAllDrives", id)
	}
	media := q.Get("alt") == "media"
	writeMeta := func(js string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, js)
	}
	switch id {
	case idAPIRoot:
		f.jsonFile(w, 200, "api_folder_meta.json")
	case idTarget:
		if media {
			serveFile(w, r, "", 2048)
			return
		}
		f.jsonFile(w, 200, "api_target_meta.json")
	case idAPIFile:
		if media {
			serveFile(w, r, "", 1048576)
			return
		}
		writeMeta(fmt.Sprintf(`{"id":%q,"name":"intro.mp4","mimeType":"video/mp4","size":"1048576","md5Checksum":"0CC175B9C0F1B6A831C399E269772661"}`, idAPIFile))
	case idAPIDoc:
		writeMeta(fmt.Sprintf(`{"id":%q,"name":"Syllabus","mimeType":"application/vnd.google-apps.document","exportLinks":{"application/pdf":"%s/export/pdf","application/vnd.openxmlformats-officedocument.wordprocessingml.document":"%s/export/docx"}}`, idAPIDoc, f.srv.URL, f.srv.URL))
	case idQuota:
		if media {
			f.jsonFile(w, 403, "api_error_quota.json")
			return
		}
		writeMeta(fmt.Sprintf(`{"id":%q,"name":"popular.bin","mimeType":"application/octet-stream","size":"10","md5Checksum":"abc"}`, idQuota))
	case idCopy:
		f.mu.Lock()
		exists := f.copyExists
		f.mu.Unlock()
		if !exists {
			f.jsonFile(w, 404, "api_error_notfound.json")
			return
		}
		if media {
			serveFile(w, r, "", 10)
			return
		}
		writeMeta(fmt.Sprintf(`{"id":%q,"name":"popular.bin","mimeType":"application/octet-stream","size":"10","md5Checksum":"abc"}`, idCopy))
	case idAbuse:
		if media {
			if q.Get("acknowledgeAbuse") != "true" {
				f.jsonFile(w, 403, "api_error_abusive.json")
				return
			}
			serveFile(w, r, "", 10)
			return
		}
		writeMeta(fmt.Sprintf(`{"id":%q,"name":"tool.exe","mimeType":"application/octet-stream","size":"10"}`, idAbuse))
	default:
		f.jsonFile(w, 404, "api_error_notfound.json")
	}
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func oauthSession(t *testing.T, tok *oauth2.Token) (*hosters.Session, *accounts.MemoryStore) {
	t.Helper()
	acct := &accounts.Account{ID: "g1", Hoster: Name, Kind: accounts.KindOAuth, Enabled: true}
	s := hosters.NewSession(netx.NewDirect(), acct, nil)
	store := accounts.NewMemoryStore()
	if err := store.Set(context.Background(), acct.ID, accounts.Secret{OAuthToken: tok, Extra: map[string]string{"keep": "me"}}); err != nil {
		t.Fatal(err)
	}
	s.Secrets = store
	return s, store
}

func goodToken() *oauth2.Token {
	return &oauth2.Token{AccessToken: "GOODACCESS", TokenType: "Bearer", RefreshToken: "REFRESH", Expiry: time.Now().Add(time.Hour)}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		link string
		kind targetKind
		id   string
	}{
		{"https://drive.google.com/file/d/" + idBig + "/view?usp=sharing", tFile, idBig},
		{"https://drive.google.com/file/d/" + idBig, tFile, idBig},
		{"https://drive.google.com/file/u/1/d/" + idBig + "/view", tFile, idBig},
		{"https://drive.google.com/open?id=" + idBig, tUnknown, idBig},
		{"https://drive.google.com/uc?id=" + idBig + "&export=download", tFile, idBig},
		{"https://drive.google.com/u/0/uc?id=" + idBig, tFile, idBig},
		{"https://drive.usercontent.google.com/download?id=" + idBig + "&export=download", tFile, idBig},
		{"https://drive.google.com/drive/folders/" + idRoot + "?usp=sharing", tFolder, idRoot},
		{"https://drive.google.com/drive/u/2/folders/" + idRoot, tFolder, idRoot},
		{"https://drive.google.com/drive/mobile/folders/" + idRoot, tFolder, idRoot},
		{"https://drive.google.com/embeddedfolderview?id=" + idRoot + "#list", tFolder, idRoot},
		{"https://docs.google.com/document/d/" + idDoc + "/edit", tDoc, idDoc},
		{"https://docs.google.com/spreadsheets/d/" + idDoc + "/edit#gid=0", tDoc, idDoc},
		{"https://docs.google.com/presentation/d/" + idDoc + "/edit", tDoc, idDoc},
		{"https://docs.google.com/document/u/0/d/" + idDoc + "/edit", tDoc, idDoc},
		{"https://docs.google.com/file/d/" + idBig + "/edit", tFile, idBig},
		{"https://docs.google.com/forms/d/" + idDoc + "/viewform", tNone, ""},
		{"https://docs.google.com/spreadsheets/d/e/2PACX-pub/pubhtml", tNone, ""},
		{"https://drive.google.com/", tNone, ""},
		{"https://drive.google.com/drive/my-drive", tNone, ""},
		{"https://drive.google.com/open?id=short", tNone, ""},
		{"https://example.com/file/d/" + idBig, tNone, ""},
	}
	h := &Hoster{}
	for _, c := range cases {
		u, _ := url.Parse(c.link)
		got := parseLink(u)
		if got.kind != c.kind || got.id != c.id {
			t.Errorf("%s: got %+v, want %d %s", c.link, got, c.kind, c.id)
		}
		if h.Match(u) != (c.kind != tNone) {
			t.Errorf("%s: Match mismatch", c.link)
		}
	}
	u, _ := url.Parse("https://drive.google.com/file/d/" + idBig + "/view?resourcekey=0-abc")
	if parseLink(u).resourceKey != "0-abc" {
		t.Error("resource key lost")
	}
}

func TestOptions(t *testing.T) {
	hh, err := New(config.HosterConfig{Options: map[string]string{"export_format": "pdf", "export_spreadsheet": "csv"}})
	if err != nil {
		t.Fatal(err)
	}
	h := hh.(*Hoster)
	want := map[string]string{"document": "pdf", "spreadsheets": "csv", "presentation": "pdf", "drawings": "pdf"}
	for k, v := range want {
		if h.exports[k] != v {
			t.Errorf("export %s = %s, want %s", k, h.exports[k], v)
		}
	}
	if !h.copyOnQuota || !h.deleteCopy {
		t.Error("defaults")
	}
	for _, bad := range []map[string]string{
		{"export_document": "xlsx"}, {"export_format": "exe"}, {"copy_on_quota": "maybe"},
	} {
		if _, err := New(config.HosterConfig{Options: bad}); err == nil {
			t.Errorf("options %v must fail", bad)
		}
	}
	hh, _ = New(config.HosterConfig{Options: map[string]string{"copy_on_quota": "false", "delete_copy": "0"}})
	if hh.(*Hoster).copyOnQuota || hh.(*Hoster).deleteCopy {
		t.Error("boolean options ignored")
	}
}

func TestAnonVirusScanForm(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	ctx := context.Background()
	res, err := h.Resolve(ctx, anon(), "https://drive.google.com/file/d/"+idBig+"/view")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0].Name != "big archive.zip" || res.Files[0].Size != 1288490188 || res.IsFolder {
		t.Fatalf("res = %+v", res)
	}
	spec, err := h.Prepare(ctx, anon(), res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(spec.URL)
	if u.Query().Get("uuid") != "0b7a1c2d-REDACTED-uuid" || u.Query().Get("confirm") != "t" || u.Query().Get("id") != idBig {
		t.Fatalf("form not followed: %s", spec.URL)
	}
	if !spec.Resumable || spec.Size != 1288490188 || spec.Client != nil {
		t.Fatalf("spec = %+v", spec)
	}
	// Legacy uc-download-link variant.
	spec, err = h.Prepare(ctx, anon(), hosters.FileInfo{ID: idOld, Link: "https://drive.google.com/uc?id=" + idOld})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.URL, "confirm=AbCd") || spec.Filename != "old.iso" || spec.Size != 734003200 {
		t.Fatalf("legacy spec = %+v", spec)
	}
	// Direct file with RFC 5987 name.
	res, err = h.Resolve(ctx, anon(), "https://drive.usercontent.google.com/download?id="+idSmall)
	if err != nil || res.Files[0].Name != "ñandú.txt" || res.Files[0].Size != 5 {
		t.Fatalf("small: %v %+v", err, res)
	}
}

func TestAnonQuotaExceeded(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	ctx := context.Background()
	res, err := h.Resolve(ctx, anon(), "https://drive.google.com/file/d/"+idHot+"/view")
	if err != nil {
		t.Fatal(err)
	}
	if res.Files[0].Name != "hot movie.mkv" || res.Files[0].Size != -1 {
		t.Fatalf("listing during quota: %+v", res.Files[0])
	}
	_, err = h.Prepare(ctx, anon(), res.Files[0])
	he, ok := hosters.AsError(err)
	if !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeFile {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "rotating the IP will not help") || !strings.Contains(msg, "connect a Google Drive account") {
		t.Fatalf("message = %q", msg)
	}
	if _, err := h.Resolve(ctx, anon(), "https://drive.google.com/file/d/"+idMissing+"/view"); !hosters.IsNotFound(err) {
		t.Fatalf("missing: %v", err)
	}
}

func TestAnonOpenAndDoc(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, map[string]string{"export_document": "pdf"})
	ctx := context.Background()
	res, err := h.Resolve(ctx, anon(), "https://drive.google.com/open?id="+idSmall)
	if err != nil || res.IsFolder || res.Files[0].Name != "ñandú.txt" {
		t.Fatalf("open file: %v %+v", err, res)
	}
	res, err = h.Resolve(ctx, anon(), "https://drive.google.com/open?id="+idRoot)
	if err != nil || !res.IsFolder {
		t.Fatalf("open folder: %v %+v", err, res)
	}
	res, err = h.Resolve(ctx, anon(), "https://docs.google.com/document/d/"+idDoc+"/edit")
	if err != nil {
		t.Fatal(err)
	}
	fi := res.Files[0]
	if fi.Name != "Notas.pdf" || fi.Meta[metaDocType] != "document" || fi.Meta[metaExport] != "pdf" {
		t.Fatalf("doc = %+v", fi)
	}
	spec, err := h.Prepare(ctx, anon(), fi)
	if err != nil || spec.Resumable || !strings.HasSuffix(spec.URL, "/document/d/"+idDoc+"/export?format=pdf") {
		t.Fatalf("doc spec: %v %+v", err, spec)
	}
}

func TestEmbeddedFolder(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	var emitted int
	res, err := h.ResolveStream(context.Background(), anon(), "https://drive.google.com/drive/folders/"+idRoot, func(hosters.FileInfo) { emitted++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Mis Fotos" {
		t.Fatalf("res = %+v", res)
	}
	var paths []string
	for _, fi := range res.Files {
		paths = append(paths, fi.RelPath)
		if fi.Meta[metaListing] != "embedded" || fi.Size != -1 {
			t.Errorf("file %+v lacks the embedded marker", fi)
		}
	}
	want := "foto 1.jpg|Notas.docx|Viaje_2025/playa.png"
	if strings.Join(paths, "|") != want || emitted != 3 {
		t.Fatalf("paths = %v (emitted %d)", paths, emitted)
	}
	if res.Files[2].Meta[metaResourceKey] != "0-RKsub" || res.Files[1].Meta[metaDocType] != "document" {
		t.Fatalf("meta = %+v / %+v", res.Files[2].Meta, res.Files[1].Meta)
	}
	if _, err := h.Resolve(context.Background(), anon(), "https://drive.google.com/drive/folders/"+idMissing); !hosters.IsNotFound(err) {
		t.Fatalf("missing folder: %v", err)
	}
}

func TestAPIFolderListing(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s, _ := oauthSession(t, goodToken())
	var emitted []string
	res, err := h.ResolveStream(context.Background(), s, "https://drive.google.com/drive/folders/"+idAPIRoot, func(fi hosters.FileInfo) {
		emitted = append(emitted, fi.RelPath)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "Shared Course" || !res.IsFolder {
		t.Fatalf("res = %+v", res)
	}
	want := []string{"intro.mp4", "Syllabus.docx", "slides link.pdf", "Week 1/lesson_1.mkv"}
	var got []string
	for _, fi := range res.Files {
		got = append(got, fi.RelPath)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") || strings.Join(emitted, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v emitted = %v", got, emitted)
	}
	intro := res.Files[0]
	if intro.Size != 1048576 || intro.Checksum == nil || intro.Checksum.Algo != hosters.AlgoMD5 || intro.Checksum.Value != "0cc175b9c0f1b6a831c399e269772661" {
		t.Fatalf("intro = %+v", intro)
	}
	if res.Files[2].ID != idTarget || res.Files[2].Size != 2048 {
		t.Fatalf("shortcut = %+v", res.Files[2])
	}
	if res.Files[3].Meta[metaResourceKey] != "0-RKlesson" {
		t.Fatalf("lesson = %+v", res.Files[3])
	}
	for _, a := range f.authz {
		if a != "Bearer GOODACCESS" {
			t.Fatalf("authorization = %q", a)
		}
	}
}

func TestAPIPrepareAndDocExport(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s, _ := oauthSession(t, goodToken())
	ctx := context.Background()
	spec, err := h.Prepare(ctx, s, hosters.FileInfo{ID: idAPIFile, Name: "intro.mp4", Link: "https://drive.google.com/file/d/" + idAPIFile + "/view"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Client == nil || !spec.Resumable || spec.Size != 1048576 || spec.Checksum == nil ||
		!strings.Contains(spec.URL, "/drive/v3/files/"+idAPIFile+"?") || !strings.Contains(spec.URL, "alt=media") {
		t.Fatalf("spec = %+v", spec)
	}
	// The spec client must authenticate requests.
	resp, err := spec.Client.Get(spec.URL)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("download with spec client: %v %v", err, resp)
	}
	resp.Body.Close()

	spec, err = h.Prepare(ctx, s, hosters.FileInfo{ID: idAPIDoc, Link: "https://docs.google.com/document/d/" + idAPIDoc + "/edit", Meta: map[string]string{metaDocType: "document", metaExport: "pdf"}})
	if err != nil || spec.URL != f.srv.URL+"/export/pdf" || spec.Resumable || spec.Filename != "Syllabus.pdf" {
		t.Fatalf("export spec: %v %+v", err, spec)
	}
	_, err = h.Prepare(ctx, s, hosters.FileInfo{ID: idMissing})
	if !hosters.IsNotFound(err) {
		t.Fatalf("missing: %v", err)
	}
}

func TestAPIAbusiveFile(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s, _ := oauthSession(t, goodToken())
	spec, err := h.Prepare(context.Background(), s, hosters.FileInfo{ID: idAbuse})
	if err != nil || !strings.Contains(spec.URL, "acknowledgeAbuse=true") {
		t.Fatalf("abuse: %v %+v", err, spec)
	}
}

func TestAPIKeyMode(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, map[string]string{"api_key": "SECRETKEY"})
	ctx := context.Background()
	spec, err := h.Prepare(ctx, anon(), hosters.FileInfo{ID: idAPIFile})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(spec.URL, "SECRETKEY") || spec.Header.Get(headerAPIKey) != "" || spec.Client == nil {
		t.Fatalf("key handling: %+v", spec)
	}
	for _, k := range f.apiKeys {
		if k != "SECRETKEY" {
			t.Fatalf("api key header = %q", k)
		}
	}
	// No copy workaround without OAuth.
	_, err = h.Prepare(ctx, anon(), hosters.FileInfo{ID: idQuota})
	if he, ok := hosters.AsError(err); !ok || he.Scope != hosters.ScopeFile || !strings.Contains(err.Error(), "connect a Google Drive account") {
		t.Fatalf("quota with api key: %v", err)
	}
	if f.copies != 0 {
		t.Fatal("copy attempted without OAuth")
	}
}

func TestOAuthRefreshPersisted(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, map[string]string{"client_id": "cid", "client_secret": "csecret"})
	expired := &oauth2.Token{AccessToken: "OLDACCESS", RefreshToken: "REFRESH", TokenType: "Bearer", Expiry: time.Now().Add(-time.Hour)}
	s, store := oauthSession(t, expired)
	if _, err := h.Prepare(context.Background(), s, hosters.FileInfo{ID: idAPIFile}); err != nil {
		t.Fatal(err)
	}
	if f.refreshes != 1 || f.tokenSeen[0] != "REFRESH" {
		t.Fatalf("refreshes = %d %v", f.refreshes, f.tokenSeen)
	}
	sec, err := store.Get(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	if sec.OAuthToken == nil || sec.OAuthToken.AccessToken != "NEWACCESS" || sec.OAuthToken.RefreshToken != "REFRESH" || sec.Extra["keep"] != "me" {
		t.Fatalf("persisted token = %+v extra=%v", sec.OAuthToken, sec.Extra)
	}
	for _, a := range f.authz {
		if a != "Bearer NEWACCESS" {
			t.Fatalf("authorization = %q", a)
		}
	}
}

func TestOAuthFailures(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	expired := &oauth2.Token{AccessToken: "OLDACCESS", RefreshToken: "REFRESH", Expiry: time.Now().Add(-time.Hour)}

	// No client credentials: cannot refresh.
	s, _ := oauthSession(t, expired)
	if _, err := f.hoster(t, nil).Prepare(ctx, s, hosters.FileInfo{ID: idAPIFile}); !hosters.IsNeedsAuth(err) {
		t.Fatalf("no client: %v", err)
	}
	// Revoked refresh token.
	revoked := &oauth2.Token{AccessToken: "OLDACCESS", RefreshToken: "REVOKED", Expiry: time.Now().Add(-time.Hour)}
	s, _ = oauthSession(t, revoked)
	_, err := f.hoster(t, map[string]string{"client_id": "cid", "client_secret": "csecret"}).Prepare(ctx, s, hosters.FileInfo{ID: idAPIFile})
	if !hosters.IsNeedsAuth(err) {
		t.Fatalf("revoked: %v", err)
	}
	if strings.Contains(err.Error(), "REVOKED") || strings.Contains(err.Error(), "OLDACCESS") {
		t.Fatalf("token leaked in error: %v", err)
	}
	// Client credentials from Secret.Extra.
	acct := &accounts.Account{ID: "g2", Hoster: Name, Kind: accounts.KindOAuth}
	s = hosters.NewSession(netx.NewDirect(), acct, nil)
	store := accounts.NewMemoryStore()
	store.Set(ctx, "g2", accounts.Secret{OAuthToken: expired, Extra: map[string]string{"client_id": "cid", "client_secret": "csecret"}})
	s.Secrets = store
	if _, err := f.hoster(t, nil).Prepare(ctx, s, hosters.FileInfo{ID: idAPIFile}); err != nil {
		t.Fatalf("extra client creds: %v", err)
	}
}

func TestCopyWorkaround(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s, _ := oauthSession(t, goodToken())
	ctx := context.Background()
	fi := hosters.FileInfo{ID: idQuota, Name: "popular.bin", Link: "https://drive.google.com/file/d/" + idQuota + "/view"}

	if !h.CanBypassFileQuota(s) || h.CanBypassFileQuota(anon()) {
		t.Fatal("CanBypassFileQuota")
	}
	spec, err := h.Prepare(ctx, s, fi)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.URL, "/files/"+idCopy+"?") || spec.Client == nil || spec.Checksum == nil || spec.Checksum.Value != "abc" {
		t.Fatalf("spec = %+v", spec)
	}
	// Prepare again (expired link, resume): reuse the copy.
	if _, err := h.Prepare(ctx, s, fi); err != nil {
		t.Fatal(err)
	}
	// A new session (restart) finds the copy by its appProperties tag.
	s2, _ := oauthSession(t, goodToken())
	if _, err := h.Prepare(ctx, s2, fi); err != nil {
		t.Fatal(err)
	}
	if f.copies != 1 {
		t.Fatalf("copies = %d, want 1", f.copies)
	}
	if err := h.Cleanup(ctx, s2, fi); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != idCopy {
		t.Fatalf("deleted = %v", f.deleted)
	}
	// Second cleanup: nothing left, no error.
	if err := h.Cleanup(ctx, s, fi); err != nil {
		t.Fatal(err)
	}
	// Cleanup on anonymous sessions is a no-op.
	if err := h.Cleanup(ctx, anon(), fi); err != nil {
		t.Fatal(err)
	}

	// copy_on_quota = false: plain file quota.
	h2 := f.hoster(t, map[string]string{"copy_on_quota": "false"})
	s3, _ := oauthSession(t, goodToken())
	_, err = h2.Prepare(ctx, s3, fi)
	if he, ok := hosters.AsError(err); !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeFile {
		t.Fatalf("copy disabled: %v", err)
	}
	// delete_copy = false keeps copies.
	h3 := f.hoster(t, map[string]string{"delete_copy": "false"})
	if _, err := h3.Prepare(ctx, s3, fi); err != nil {
		t.Fatal(err)
	}
	if err := h3.Cleanup(ctx, s3, fi); err != nil || len(f.deleted) != 1 {
		t.Fatalf("delete_copy=false: %v %v", err, f.deleted)
	}
}

func TestCheckAccount(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s, _ := oauthSession(t, goodToken())
	res, err := h.CheckAccount(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != accounts.StatusOK || res.Message != "user@example.com" || res.QuotaTotal != 16106127360 || res.QuotaUsed != 1073741824 {
		t.Fatalf("check = %+v", res)
	}
	bad, _ := oauthSession(t, &oauth2.Token{AccessToken: "WRONG", Expiry: time.Now().Add(time.Hour)})
	res, err = h.CheckAccount(context.Background(), bad)
	if err != nil || res.Status != accounts.StatusExpired {
		t.Fatalf("bad token: %v %+v", err, res)
	}
}

func TestClassify(t *testing.T) {
	h := &Hoster{}
	mk := func(code int, host, ct string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}, Request: &http.Request{URL: &url.URL{Scheme: "https", Host: host, Path: "/x"}}}
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return r
	}
	attach := mk(200, "drive.usercontent.google.com", "text/html")
	attach.Header.Set("Content-Disposition", `attachment; filename="page.html"`)
	cases := []struct {
		name  string
		resp  *http.Response
		body  []byte
		err   error
		kind  hosters.Kind
		scope hosters.QuotaScope
	}{
		{"ok", mk(206, "www.googleapis.com", "application/octet-stream"), nil, nil, hosters.KindOK, 0},
		{"html attachment ok", attach, []byte("<html></html>"), nil, hosters.KindOK, 0},
		{"api quota", mk(403, "www.googleapis.com", "application/json"), fixture(t, "api_error_quota.json"), nil, hosters.KindQuota, hosters.ScopeFile},
		{"api rate", mk(403, "www.googleapis.com", "application/json"), fixture(t, "api_error_ratelimit.json"), nil, hosters.KindTemporary, 0},
		{"api abuse", mk(403, "www.googleapis.com", "application/json"), fixture(t, "api_error_abusive.json"), nil, hosters.KindExpired, 0},
		{"api notfound", mk(404, "www.googleapis.com", "application/json"), fixture(t, "api_error_notfound.json"), nil, hosters.KindNotFound, 0},
		{"quota page", mk(200, "drive.usercontent.google.com", "text/html"), fixture(t, "quota_exceeded.html"), nil, hosters.KindQuota, hosters.ScopeFile},
		{"quota page 403", mk(403, "drive.usercontent.google.com", "text/html"), fixture(t, "quota_exceeded.html"), nil, hosters.KindQuota, hosters.ScopeFile},
		{"virus page", mk(200, "drive.usercontent.google.com", "text/html"), fixture(t, "virus_scan.html"), nil, hosters.KindExpired, 0},
		{"not found page", mk(404, "drive.usercontent.google.com", "text/html"), fixture(t, "not_found.html"), nil, hosters.KindNotFound, 0},
		{"login redirect", mk(200, "accounts.google.com", "text/html"), []byte("<html>sign in</html>"), nil, hosters.KindNeedsAuth, 0},
		{"other html", mk(200, "drive.usercontent.google.com", "text/html"), []byte("<html>?</html>"), nil, hosters.KindExpired, 0},
		{"blocked", mk(429, "drive.usercontent.google.com", "text/html"), []byte("<html>Our systems have detected unusual traffic</html>"), nil, hosters.KindQuota, hosters.ScopeIP},
		{"403 bare", mk(403, "drive.usercontent.google.com", ""), nil, nil, hosters.KindExpired, 0},
		{"5xx", mk(503, "www.googleapis.com", ""), nil, nil, hosters.KindTemporary, 0},
		{"oauth refresh", nil, nil, &url.Error{Op: "Get", URL: "u", Err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}}, hosters.KindNeedsAuth, 0},
		{"transport", nil, nil, errors.New("reset"), hosters.KindTemporary, 0},
		{"canceled", nil, nil, context.Canceled, hosters.KindFatal, 0},
	}
	for _, c := range cases {
		err := h.Classify(c.resp, c.body, c.err)
		if k := hosters.KindOf(err); k != c.kind {
			t.Errorf("%s: kind %v (%v), want %v", c.name, k, err, c.kind)
			continue
		}
		if c.kind == hosters.KindQuota {
			if he, _ := hosters.AsError(err); he.Scope != c.scope {
				t.Errorf("%s: scope %v, want %v", c.name, he.Scope, c.scope)
			}
		}
	}
}

func TestDownloadFormParsing(t *testing.T) {
	base, _ := url.Parse("https://drive.usercontent.google.com/download?id=" + idBig)
	u, ok := downloadForm(fixture(t, "virus_scan.html"), base)
	if !ok {
		t.Fatal("form not found")
	}
	pu, _ := url.Parse(u)
	if pu.Host != "drive.usercontent.google.com" || pu.Path != "/download" || pu.Query().Get("export") != "download" {
		t.Fatalf("url = %s", u)
	}
	if _, ok := downloadForm(fixture(t, "quota_exceeded.html"), base); ok {
		t.Fatal("quota page has no form")
	}
}

func TestRegistered(t *testing.T) {
	for _, n := range hosters.FactoryNames() {
		if n == Name {
			return
		}
	}
	t.Fatal("factory not registered")
}

func TestClassifyRedactsURLs(t *testing.T) {
	h := &Hoster{}
	err := h.Classify(nil, nil, &url.Error{Op: "Get", URL: "https://drive.usercontent.google.com/download?id=x&uuid=SECRETUUID", Err: errors.New("reset")})
	if strings.Contains(err.Error(), "SECRETUUID") || !hosters.IsTemporary(err) {
		t.Fatalf("err = %v", err)
	}
}
