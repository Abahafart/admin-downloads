package mega

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

// fakeMEGA emulates the parts of g.api.mega.co.nz and the storage servers
// the plugin uses.
type fakeMEGA struct {
	t   *testing.T
	srv *httptest.Server
	v   vectors

	mu        sync.Mutex
	files     map[string][]byte // public handle -> ciphertext
	attrs     map[string]string
	folderRaw json.RawMessage
	quota     bool // g:1 answers EOVERQUOTA
	mfaCode   string
	hashcash  bool // first us0 gets a 402 challenge
	logins    int
	sids      []string // sid seen per request ("" = none)
	lastQuery map[string]string

	masterKey []byte
	privk     string
	csid      string
	validSID  string
}

func newFakeMEGA(t *testing.T) *fakeMEGA {
	t.Helper()
	f := &fakeMEGA{t: t, v: loadVectors(t), files: map[string][]byte{}, attrs: map[string]string{}}
	raw, err := os.ReadFile(filepath.Join(testdataDir, "folder_listing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatal(err)
	}
	f.folderRaw = arr[0]
	// Public file: vector 3 (1.5 MiB, crosses several MAC chunks).
	vf := f.v.Files[3]
	k, _ := parseFileKey(vf.LinkKey)
	pt := testPlaintext(vf.Size)
	ct := make([]byte, len(pt))
	s, _ := newCTRAt(k, 0)
	s.XORKeyStream(ct, pt)
	f.files["PubFile1"] = ct
	f.attrs["PubFile1"] = vf.Attr
	f.setupLogin()
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func mpi(b []byte) []byte {
	n := new(big.Int).SetBytes(b)
	bits := n.BitLen()
	return append([]byte{byte(bits >> 8), byte(bits)}, n.Bytes()...)
}

// setupLogin builds a v2 account: master key encrypted with the derived
// password key, an RSA key encrypted with the master key, and a csid.
func (f *fakeMEGA) setupLogin() {
	f.masterKey = bytes.Repeat([]byte{0x42}, 16)
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		f.t.Fatal(err)
	}
	p, q := priv.Primes[0], priv.Primes[1]
	u := new(big.Int).ModInverse(q, p)
	pk := append(append(append(mpi(p.Bytes()), mpi(q.Bytes())...), mpi(priv.D.Bytes())...), mpi(u.Bytes())...)
	pk = padZero(pk, 16)
	enc, _ := ecbEncrypt(f.masterKey, pk)
	f.privk = b64Encode(enc)
	sidPlain := make([]byte, 64)
	_, _ = rand.Read(sidPlain)
	sidPlain[0] = 0x5a
	c := new(big.Int).Exp(new(big.Int).SetBytes(sidPlain), big.NewInt(int64(priv.E)), priv.N)
	f.csid = b64Encode(mpi(c.Bytes()))
	f.validSID = b64Encode(sidPlain[:43])
}

func (f *fakeMEGA) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeMEGA) lastN() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastQuery["n"]
}

func (f *fakeMEGA) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeMEGA) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		f.serveStorage(w, r)
		return
	}
	if r.URL.Path != "/cs" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if q.Get("id") == "" {
		http.Error(w, "no id", http.StatusBadRequest)
		return
	}
	var cmds []map[string]any
	if err := json.NewDecoder(r.Body).Decode(&cmds); err != nil || len(cmds) != 1 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	cmd := cmds[0]
	sid := q.Get("sid")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sids = append(f.sids, sid)
	f.lastQuery = map[string]string{"n": q.Get("n"), "a": fmt.Sprint(cmd["a"])}
	if sid != "" && sid != f.validSID {
		f.reply(w, -15) // request-level ESID
		return
	}
	switch cmd["a"] {
	case "us0":
		if f.hashcash {
			if h := r.Header.Get("X-Hashcash"); h == "" || !validHashcash(f.t, h, "dG9rZW4tdG9rZW4tdG9rZW4tdG9rZW4", 255) {
				w.Header().Set("X-Hashcash", "1:255:1700000000:dG9rZW4tdG9rZW4tdG9rZW4tdG9rZW4")
				w.WriteHeader(http.StatusPaymentRequired)
				return
			}
		}
		if cmd["user"] != strings.ToLower(f.v.Login.Email) {
			f.reply(w, []any{map[string]any{"v": 1}})
			return
		}
		f.reply(w, []any{map[string]any{"v": 2, "s": f.v.Login.V2Salt}})
	case "us":
		if cmd["uh"] != f.v.Login.V2UH || cmd["user"] != strings.ToLower(f.v.Login.Email) {
			f.reply(w, []any{-9})
			return
		}
		if f.mfaCode != "" && cmd["mfa"] != f.mfaCode {
			f.reply(w, []any{-26})
			return
		}
		pass, _ := hex.DecodeString(f.v.Login.V2PassKeyHex)
		k, _ := ecbEncrypt(pass, f.masterKey)
		f.logins++
		f.reply(w, []any{map[string]any{"k": b64Encode(k), "privk": f.privk, "csid": f.csid, "u": "UserHndl"}})
	case "uq":
		if sid == "" {
			f.reply(w, []any{-11})
			return
		}
		f.reply(w, []any{map[string]any{"utype": 2, "suntil": 1893456000, "mxfer": 1000, "caxfer": 300, "csxfer": 50, "mstrg": 1 << 40, "cstrg": 1 << 30}})
	case "g":
		f.serveG(w, r, cmd, q.Get("n"))
	case "f":
		if q.Get("n") != f.v.Folder.Handle {
			f.reply(w, []any{-9})
			return
		}
		f.reply(w, []any{f.folderRaw})
	default:
		f.reply(w, []any{-2})
	}
}

func (f *fakeMEGA) serveG(w http.ResponseWriter, r *http.Request, cmd map[string]any, n string) {
	var h, attr string
	var size int
	switch {
	case cmd["p"] != nil:
		h = fmt.Sprint(cmd["p"])
		ct, ok := f.files[h]
		if !ok {
			f.reply(w, []any{-9})
			return
		}
		attr, size = f.attrs[h], len(ct)
	case cmd["n"] != nil && n == f.v.Folder.Handle:
		h = fmt.Sprint(cmd["n"])
		if h != f.v.Folder.FileHandle {
			f.reply(w, []any{-9})
			return
		}
		size = 1000
	default:
		f.reply(w, []any{-2})
		return
	}
	res := map[string]any{"s": size, "at": attr}
	if cmd["g"] != nil {
		if f.quota {
			f.reply(w, []any{map[string]any{"e": -17, "tl": 1234}})
			return
		}
		res["g"] = f.srv.URL + "/dl/" + h
	}
	f.reply(w, []any{res})
}

func (f *fakeMEGA) serveStorage(w http.ResponseWriter, r *http.Request) {
	h := strings.TrimPrefix(r.URL.Path, "/dl/")
	if h == "quota" {
		w.Header().Set("X-MEGA-Time-Left", "777")
		w.WriteHeader(509)
		return
	}
	f.mu.Lock()
	ct, ok := f.files[h]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(ct))
}

func newTestPlugin(t *testing.T, f *fakeMEGA, cfg config.HosterConfig) *Plugin {
	t.Helper()
	if cfg.Options == nil {
		cfg.Options = map[string]string{}
	}
	cfg.Options[OptAPIURL] = f.srv.URL
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Plugin)
}

func anonSession() *hosters.Session {
	s := hosters.NewSession(netx.NewDirect(), nil, nil)
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return s
}

func accountSession(t *testing.T, f *fakeMEGA, sec accounts.Secret) (*hosters.Session, *accounts.MemoryStore) {
	t.Helper()
	acct := &accounts.Account{ID: "acc1", Hoster: Name, Kind: accounts.KindPassword, Username: f.v.Login.Email, Enabled: true}
	s := hosters.NewSession(netx.NewDirect(), acct, nil)
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	store := accounts.NewMemoryStore()
	if err := store.Set(context.Background(), acct.ID, sec); err != nil {
		t.Fatal(err)
	}
	s.Secrets = store
	return s, store
}

func TestResolveAndDownloadPublicFile(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{MaxConnsPerFile: 3})
	s := anonSession()
	vf := f.v.Files[3]
	ctx := t.Context()

	res, err := p.Resolve(ctx, s, "https://mega.nz/#!PubFile1!"+vf.LinkKey)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 {
		t.Fatalf("unexpected result %+v", res)
	}
	fi := res.Files[0]
	if fi.Name != vf.Name || fi.Size != int64(vf.Size) || fi.ID != "PubFile1" || fi.RelPath != "" {
		t.Fatalf("file info %+v", fi)
	}
	if fi.Checksum == nil || fi.Checksum.Algo != hosters.AlgoMEGAMAC || fi.Link != "https://mega.nz/file/PubFile1#"+vf.LinkKey {
		t.Fatalf("checksum/link %+v %s", fi.Checksum, fi.Link)
	}

	spec, err := p.Prepare(ctx, s, fi)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Resumable || spec.MaxConns != 3 || spec.Size != int64(vf.Size) || spec.Decryptor == nil {
		t.Fatalf("spec %+v", spec)
	}
	pt := testPlaintext(vf.Size)
	// Download three segments out of order, each decrypted from its offset.
	for _, seg := range [][2]int{{700001, 900000}, {0, 12345}, {131070, vf.Size - 1}} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", seg[0], seg[1]))
		resp, err := s.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status %d", resp.StatusCode)
		}
		stream, err := spec.Decryptor(int64(seg[0]))
		if err != nil {
			t.Fatal(err)
		}
		stream.XORKeyStream(body, body)
		if !bytes.Equal(body, pt[seg[0]:seg[1]+1]) {
			t.Fatalf("segment %v decrypted wrong", seg)
		}
	}
	if err := VerifyMAC(bytes.NewReader(pt), spec.Checksum.Value); err != nil {
		t.Fatal(err)
	}

	// Prepare must also work from the link alone (FileInfo without Meta).
	spec2, err := p.Prepare(ctx, s, hosters.FileInfo{Link: fi.Link, Size: -1})
	if err != nil || spec2.URL == "" {
		t.Fatalf("prepare from link: %v", err)
	}
}

func TestResolveErrors(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{})
	s := anonSession()
	ctx := t.Context()
	vf := f.v.Files[3]
	cases := []struct {
		link string
		kind hosters.Kind
	}{
		{"https://mega.nz/file/Missing1#" + vf.LinkKey, hosters.KindNotFound},
		{"https://mega.nz/file/PubFile1", hosters.KindFatal},                         // no key
		{"https://mega.nz/file/PubFile1#" + f.v.Files[0].LinkKey, hosters.KindFatal}, // wrong key
		{"https://mega.nz/file/PubFile1#AAAA", hosters.KindFatal},                    // short key
		{"https://mega.nz/#P!AgAAAAAAAAAA", hosters.KindFatal},
		{"https://example.com/file/x", hosters.KindFatal},
		{"https://mega.nz/folder/NoSuchFo#" + f.v.Folder.Key, hosters.KindNotFound},
	}
	for _, c := range cases {
		_, err := p.Resolve(ctx, s, c.link)
		if hosters.KindOf(err) != c.kind {
			t.Errorf("%s: err = %v, want kind %v", c.link, err, c.kind)
		}
	}
}

func TestFolderResolve(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{})
	s := anonSession()
	ctx := t.Context()
	fv := f.v.Folder
	link := "https://mega.nz/folder/" + fv.Handle + "#" + fv.Key

	var emitted []hosters.FileInfo
	res, err := p.ResolveStream(ctx, s, link, func(fi hosters.FileInfo) { emitted = append(emitted, fi) })
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Mi Carpeta" {
		t.Fatalf("result %q folder=%v", res.Name, res.IsFolder)
	}
	var got []string
	for _, fi := range res.Files {
		got = append(got, fi.RelPath)
		if fi.Meta[metaRoot] != fv.Handle || fi.Meta[metaKey] == "" || !strings.HasPrefix(fi.Link, link+"/file/") {
			t.Errorf("%s: meta/link %v %s", fi.RelPath, fi.Meta, fi.Link)
		}
		if !strings.HasSuffix(fi.RelPath, fi.Name) {
			t.Errorf("RelPath %q does not end with Name %q", fi.RelPath, fi.Name)
		}
	}
	want := []string{
		".._.._etc_passwd",
		fv.BadAttrHandle, // undecryptable name falls back to the handle
		"Temporada 1/Extras/making of.mp4",
		"Temporada 1/ep01.mkv",
		"Temporada 1/ep02.mkv",
		"readme.txt",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("paths:\n got %q\nwant %q", got, want)
	}
	if len(emitted) != len(res.Files) {
		t.Fatalf("emitted %d of %d", len(emitted), len(res.Files))
	}
	for _, fi := range res.Files {
		if fi.ID == fv.OrphanHandle {
			t.Fatal("orphan node listed")
		}
		if fi.ID == fv.FileHandle && (fi.Meta[metaKey] != fv.Ep01Key || fi.Size != 1000) {
			t.Fatalf("ep01 key/size wrong: %+v", fi)
		}
	}

	// Subfolder link: only Temporada 1, paths relative to it.
	sub, err := p.Resolve(ctx, s, link+"/folder/"+fv.SubHandle)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Name != "Temporada 1" || len(sub.Files) != 3 || sub.Files[0].RelPath != "Extras/making of.mp4" {
		t.Fatalf("subfolder %+v", sub)
	}
	// Legacy subfolder syntax gives the same.
	sub2, err := p.Resolve(ctx, s, "https://mega.nz/#F!"+fv.Handle+"!"+fv.Key+"!"+fv.SubHandle)
	if err != nil || len(sub2.Files) != 3 {
		t.Fatalf("legacy subfolder: %v", err)
	}

	// File inside the folder.
	one, err := p.Resolve(ctx, s, link+"/file/"+fv.FileHandle)
	if err != nil {
		t.Fatal(err)
	}
	if one.IsFolder || len(one.Files) != 1 || one.Files[0].Name != "ep01.mkv" || one.Files[0].RelPath != "" {
		t.Fatalf("file in folder %+v", one)
	}
	spec, err := p.Prepare(ctx, s, one.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if f.lastN() != fv.Handle || !strings.HasSuffix(spec.URL, "/dl/"+fv.FileHandle) {
		t.Fatalf("folder file prepared with n=%q", f.lastN())
	}
	// And from the link only.
	if _, err := p.Prepare(ctx, s, hosters.FileInfo{Link: one.Files[0].Link}); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{link + "/file/NoSuchFi", link + "/folder/NoSuchFo", link + "/file/" + fv.SubHandle} {
		if _, err := p.Resolve(ctx, s, bad); !hosters.IsNotFound(err) {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
	// Wrong folder key: names fall back to handles, files undecryptable.
	wrong := "https://mega.nz/folder/" + fv.Handle + "#" + b64Encode(make([]byte, 16))
	if _, err := p.Resolve(ctx, s, wrong); !hosters.IsFatal(err) {
		t.Fatalf("wrong folder key: %v", err)
	}
}

func TestQuotaOutcomes(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{RetryAfter: config.D(42 * time.Minute)})
	s := anonSession()
	ctx := t.Context()
	vf := f.v.Files[3]
	res, err := p.Resolve(ctx, s, "https://mega.nz/file/PubFile1#"+vf.LinkKey)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.quota = true
	f.mu.Unlock()
	_, err = p.Prepare(ctx, s, res.Files[0])
	he, ok := hosters.AsError(err)
	if !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeIP || he.RetryAfter != 1234*time.Second {
		t.Fatalf("EOVERQUOTA: %v", err)
	}

	// 509 from storage with and without a time-left header.
	resp, err := s.Client.Get(f.srv.URL + "/dl/quota")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	he, _ = hosters.AsError(p.Classify(resp, nil, nil))
	if he == nil || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeIP || he.RetryAfter != 777*time.Second {
		t.Fatalf("509: %v", he)
	}
	resp.Header.Del("X-MEGA-Time-Left")
	he, _ = hosters.AsError(p.Classify(resp, nil, nil))
	if he == nil || he.RetryAfter != 42*time.Minute {
		t.Fatalf("509 default retry: %v", he)
	}
	if p.Caps().DefaultRetryAfter != 42*time.Minute || p.Caps().QuotaScope != hosters.ScopeIP {
		t.Fatalf("caps %+v", p.Caps())
	}
}

func TestClassify(t *testing.T) {
	p := &Plugin{retryAfter: time.Hour}
	apiReq, _ := http.NewRequest(http.MethodPost, "https://g.api.mega.co.nz/cs?id=1&sid=SECRET", nil)
	dlReq, _ := http.NewRequest(http.MethodGet, "https://gfs1.userstorage.mega.co.nz/dl/SIGNED", nil)
	mk := func(code int, req *http.Request) *http.Response {
		return &http.Response{StatusCode: code, Header: http.Header{}, Request: req}
	}
	cases := []struct {
		resp *http.Response
		body string
		err  error
		want hosters.Kind
	}{
		{mk(200, apiReq), "-17", nil, hosters.KindQuota},
		{mk(200, apiReq), "[-4]", nil, hosters.KindTemporary},
		{mk(200, apiReq), "[-3]", nil, hosters.KindTemporary},
		{mk(200, apiReq), "-9", nil, hosters.KindNotFound},
		{mk(200, apiReq), "[-16]", nil, hosters.KindNotFound},
		{mk(200, apiReq), "[-11]", nil, hosters.KindFatal},
		{mk(200, apiReq), "-15", nil, hosters.KindNeedsAuth},
		{mk(200, apiReq), `[{"e":-17}]`, nil, hosters.KindQuota},
		{mk(200, apiReq), `[{"s":1}]`, nil, hosters.KindOK},
		{mk(200, dlReq), "-17", nil, hosters.KindOK}, // ciphertext, not an API error
		{mk(509, dlReq), "", nil, hosters.KindQuota},
		{mk(403, dlReq), "", nil, hosters.KindExpired},
		{mk(404, dlReq), "", nil, hosters.KindNotFound},
		{mk(503, dlReq), "", nil, hosters.KindTemporary},
		{nil, "", context.Canceled, hosters.KindFatal},
	}
	for i, c := range cases {
		if got := hosters.KindOf(p.Classify(c.resp, []byte(c.body), c.err)); got != c.want {
			t.Errorf("case %d (%s): kind %v, want %v", i, c.body, got, c.want)
		}
	}
	// Transport errors must not leak the URL (sid / signed URL).
	_, err := http.DefaultClient.Do(mustReq(t, "http://127.0.0.1:1/cs?sid=SECRETSID"))
	got := p.Classify(nil, nil, err)
	if got == nil || strings.Contains(got.Error(), "SECRETSID") {
		t.Fatalf("classified transport error leaks URL: %v", got)
	}
}

func mustReq(t *testing.T, u string) *http.Request {
	r, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoginAndCheckAccount(t *testing.T) {
	f := newFakeMEGA(t)
	f.hashcash = true
	p := newTestPlugin(t, f, config.HosterConfig{})
	s, store := accountSession(t, f, accounts.Secret{Password: f.v.Login.Password})
	ctx := t.Context()

	r, err := p.CheckAccount(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != accounts.StatusOK || !r.Premium || r.QuotaTotal != 1000 || r.QuotaUsed != 350 || r.PremiumUntil.Year() != 2030 || r.Message != "MEGA Pro II" {
		t.Fatalf("check result %+v", r)
	}
	sec, _ := store.Get(ctx, "acc1")
	if sec.Session != f.validSID || sec.Password == "" {
		t.Fatal("sid not persisted in Secret.Session")
	}
	// Premium accounts report EOVERQUOTA as account-scoped.
	if p.quotaScope(s) != hosters.ScopeAccount {
		t.Fatal("premium session should have account quota scope")
	}
	// Downloads use the sid.
	vf := f.v.Files[3]
	if _, err := p.Resolve(ctx, s, "https://mega.nz/file/PubFile1#"+vf.LinkKey); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.sids[len(f.sids)-1]
	logins := f.logins
	f.mu.Unlock()
	if last != f.validSID || logins != 1 {
		t.Fatalf("sid on requests = %q, logins = %d", last, logins)
	}

	// A new session reuses the stored sid without logging in again.
	s2, _ := accountSession(t, f, sec)
	if _, err := p.CheckAccount(ctx, s2); err != nil {
		t.Fatal(err)
	}
	if f.loginCount() != 1 {
		t.Fatalf("logged in again: %d", f.loginCount())
	}
}

func TestExpiredSessionRelogin(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{})
	s, store := accountSession(t, f, accounts.Secret{Password: f.v.Login.Password, Session: "stale-sid"})
	ctx := t.Context()
	r, err := p.CheckAccount(ctx, s)
	if err != nil || r.Status != accounts.StatusOK {
		t.Fatalf("check: %+v %v", r, err)
	}
	sec, _ := store.Get(ctx, "acc1")
	if sec.Session != f.validSID || f.loginCount() != 1 {
		t.Fatalf("session not refreshed (logins=%d)", f.loginCount())
	}

	// Without a password an expired sid is NeedsAuth.
	s3, _ := accountSession(t, f, accounts.Secret{Session: "stale-sid"})
	r, err = p.CheckAccount(ctx, s3)
	if err != nil || r.Status != accounts.StatusInvalid {
		t.Fatalf("no password: %+v %v", r, err)
	}
	vf := f.v.Files[3]
	s4, _ := accountSession(t, f, accounts.Secret{Session: "stale-sid"})
	if _, err := p.Resolve(ctx, s4, "https://mega.nz/file/PubFile1#"+vf.LinkKey); !hosters.IsNeedsAuth(err) {
		t.Fatalf("resolve with dead sid: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{})
	s, _ := accountSession(t, f, accounts.Secret{Password: "nope"})
	r, err := p.CheckAccount(t.Context(), s)
	if err != nil || r.Status != accounts.StatusInvalid || !strings.Contains(r.Message, "wrong e-mail or password") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestTwoFactor(t *testing.T) {
	f := newFakeMEGA(t)
	f.mfaCode = "123456"
	p := newTestPlugin(t, f, config.HosterConfig{})
	ctx := t.Context()
	s, store := accountSession(t, f, accounts.Secret{Password: f.v.Login.Password})

	r, err := p.CheckAccount(ctx, s)
	if err != nil || r.Status != accounts.StatusNeeds2FA {
		t.Fatalf("no code: %+v %v", r, err)
	}
	// Downloads report NeedsAuth wrapping ErrMFARequired.
	_, err = p.Resolve(ctx, s, "https://mega.nz/file/PubFile1#"+f.v.Files[3].LinkKey)
	if !hosters.IsNeedsAuth(err) || !errors.Is(err, ErrMFARequired) {
		t.Fatalf("resolve without code: %v", err)
	}

	// Wrong code: still needs 2FA, code consumed.
	sec, _ := store.Get(ctx, "acc1")
	sec.Extra = map[string]string{SecretExtraMFA: "000000"}
	_ = store.Set(ctx, "acc1", sec)
	r, err = p.CheckAccount(ctx, s)
	if err != nil || r.Status != accounts.StatusNeeds2FA || !strings.Contains(r.Message, "rejected") {
		t.Fatalf("wrong code: %+v %v", r, err)
	}
	sec, _ = store.Get(ctx, "acc1")
	if _, ok := sec.Extra[SecretExtraMFA]; ok {
		t.Fatal("used 2FA code not cleared")
	}

	// Right code: logged in, sid stored, code removed.
	sec.Extra = map[string]string{SecretExtraMFA: "123456"}
	_ = store.Set(ctx, "acc1", sec)
	r, err = p.CheckAccount(ctx, s)
	if err != nil || r.Status != accounts.StatusOK {
		t.Fatalf("right code: %+v %v", r, err)
	}
	sec, _ = store.Get(ctx, "acc1")
	if sec.Session != f.validSID || sec.Extra[SecretExtraMFA] != "" {
		t.Fatal("2FA login did not persist sid / clear code")
	}
}

func TestAPIRetriesAndErrors(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		switch n {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
		case 2:
			_, _ = w.Write([]byte("-3"))
		default:
			_, _ = w.Write([]byte(`[{"s":5,"at":"x"}]`))
		}
	}))
	defer srv.Close()
	h, _ := New(config.HosterConfig{Options: map[string]string{OptAPIURL: srv.URL}})
	p := h.(*Plugin)
	res, err := p.apiCall(t.Context(), anonSession(), "", nil, map[string]any{"a": "g"})
	mu.Lock()
	n := calls
	mu.Unlock()
	if err != nil || !bytes.Contains(res, []byte(`"s":5`)) || n != 3 {
		t.Fatalf("res %s err %v calls %d", res, err, n)
	}
	if _, err := New(config.HosterConfig{Options: map[string]string{OptAPIURL: "::bad"}}); err == nil {
		t.Fatal("bad api_url accepted")
	}
	if _, err := parseAPIResponse([]byte("<html>")); !hosters.IsTemporary(err) {
		t.Fatalf("html body: %v", err)
	}
	if got, err := parseAPIResponse([]byte("[0]")); err != nil || string(got) != "0" {
		t.Fatalf("[0]: %s %v", got, err)
	}
}

func TestSelfTest(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{Options: map[string]string{OptSelfTestLink: "https://mega.nz/file/PubFile1#" + f.v.Files[3].LinkKey}})
	if err := p.SelfTest(t.Context(), anonSession()); err != nil {
		t.Fatal(err)
	}
	p2 := newTestPlugin(t, f, config.HosterConfig{})
	if err := p2.SelfTest(t.Context(), anonSession()); err == nil {
		t.Fatal("selftest without link should fail")
	}
}

func TestRegistered(t *testing.T) {
	found := false
	for _, n := range hosters.FactoryNames() {
		found = found || n == Name
	}
	if !found {
		t.Fatal("mega factory not registered")
	}
}

func TestProAccountQuotaIsAccountScoped(t *testing.T) {
	f := newFakeMEGA(t)
	p := newTestPlugin(t, f, config.HosterConfig{})
	s, _ := accountSession(t, f, accounts.Secret{Password: f.v.Login.Password})
	ctx := t.Context()
	res, err := p.Resolve(ctx, s, "https://mega.nz/file/PubFile1#"+f.v.Files[3].LinkKey)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.quota = true
	f.mu.Unlock()
	_, err = p.Prepare(ctx, s, res.Files[0])
	he, ok := hosters.AsError(err)
	if !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeAccount {
		t.Fatalf("pro quota: %v", err)
	}
}
