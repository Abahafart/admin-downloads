package rotation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/store"
)

type nopSocksLog struct{}

func (nopSocksLog) Errorf(string, ...any) {}

// startProxy runs a SOCKS5 proxy named name whose outgoing connections are
// registered in ids (so the test hoster sees a distinct "source IP").
func startProxy(t *testing.T, ids *identities, name string, creds socks5.CredentialStore) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opts := []socks5.Option{
		socks5.WithLogger(nopSocksLog{}),
		socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ids.dial(ctx, name, network, addr)
		}),
	}
	if creds != nil {
		opts = append(opts, socks5.WithCredential(creds))
	}
	go socks5.NewServer(opts...).Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// quotaHoster serves data with Range support and answers 509 once a source
// identity has downloaded more than budget bytes.
type quotaHoster struct {
	ids    *identities
	data   []byte
	budget int
	mu     sync.Mutex
	served map[string]int
}

func (h *quotaHoster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	who := h.ids.who(r)
	h.mu.Lock()
	used := h.served[who]
	h.mu.Unlock()
	if used >= h.budget {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "bandwidth limit exceeded", 509)
		return
	}
	start, end := 0, len(h.data)-1
	if rg := r.Header.Get("Range"); rg != "" {
		spec := strings.TrimPrefix(rg, "bytes=")
		a, b, _ := strings.Cut(spec, "-")
		start, _ = strconv.Atoi(a)
		if b != "" {
			end, _ = strconv.Atoi(b)
		}
		end = min(end, len(h.data)-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(h.data)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
	}
	n, _ := w.Write(h.data[start : end+1])
	h.mu.Lock()
	h.served[who] += n
	h.mu.Unlock()
}

// download fetches url in chunks like the engine would: on 509 it reports
// the quota, releases the egress and acquires another one, resuming from
// the current offset.
func download(t *testing.T, m Manager, url string, size, chunk int) ([]byte, []string) {
	t.Helper()
	ctx := context.Background()
	var out bytes.Buffer
	var used []string
	req := Request{Hoster: "fakehoster", AllowRotation: true, QuotaScope: hosters.ScopeIP}
	e, err := m.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	used = append(used, e.ID())
	client := netx.NewHTTPClient(e, nil)
	for out.Len() < size {
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		hreq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", out.Len(), out.Len()+chunk-1))
		resp, err := client.Do(hreq)
		if err != nil {
			t.Fatalf("GET via %s: %v", e.ID(), err)
		}
		if resp.StatusCode == 509 {
			resp.Body.Close()
			m.ReportQuota(ctx, "fakehoster", e.ID(), hosters.ScopeIP, hosters.ParseRetryAfter(resp.Header, time.Now()))
			m.Release("fakehoster", e.ID())
			client.CloseIdleConnections()
			req.PreferEgress = e.ID()
			e, err = m.Acquire(ctx, req)
			if err != nil {
				return out.Bytes(), append(used, "error:"+err.Error())
			}
			used = append(used, e.ID())
			client = netx.NewHTTPClient(e, nil)
			continue
		}
		_, err = io.Copy(&out, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	m.Release("fakehoster", e.ID())
	client.CloseIdleConnections()
	return out.Bytes(), used
}

func socksSpec(addr, user, pass string) string {
	if user == "" {
		return "socks5://" + addr
	}
	return "socks5://" + user + ":" + pass + "@" + addr
}

func testConfig(t *testing.T, echoURL string, proxies ...string) *config.Config {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Egress.Direct = false
	cfg.Egress.HealthInterval = config.D(0)
	cfg.Egress.Proxies.List = proxies
	cfg.Rotation.IPCheckServices = []string{echoURL}
	cfg.Rotation.Strategy = config.StrategyRoundRobin
	return cfg
}

// TestEndToEndQuotaRotation is the local rotation test of PLAN.md section
// 14: a fake hoster returning 509 after N bytes per source IP, SOCKS5
// proxies, and a download that switches egress and finishes intact; the
// exhausted marks survive a restart.
func TestEndToEndQuotaRotation(t *testing.T) {
	ids := newIdentities()
	echo := ids.echo(t)
	data := make([]byte, 250_000)
	rand.Read(data)
	h := &quotaHoster{ids: ids, data: data, budget: 100_000, served: map[string]int{}}
	hoster := httptest.NewServer(h)
	defer hoster.Close()

	p1 := startProxy(t, ids, "p1", socks5.StaticCredentials{"u1": "pw-one"})
	p2 := startProxy(t, ids, "p2", nil)
	p3 := startProxy(t, ids, "p3", nil)
	for i, n := range []string{"p1", "p2", "p3"} {
		ids.setIP(n, "198.51.100."+strconv.Itoa(i+1))
	}

	dbPath := filepath.Join(t.TempDir(), "adesc.db")
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	secrets := accounts.NewMemoryStore()
	cfg := testConfig(t, echo.URL, socksSpec(p1, "u1", "pw-one"), socksSpec(p2, "", ""), socksSpec(p3, "", ""))
	m, err := NewFromConfig(ctx, cfg, st, secrets, slog.New(slog.DiscardHandler), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	m.CheckNow(ctx)
	for _, s := range m.Egresses() {
		if !s.Healthy || s.PublicIP == "" {
			t.Fatalf("egress not healthy: %+v", s)
		}
	}

	url := hoster.URL + "/file.bin"
	got, used := download(t, m, url, len(data), 50_000)
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("checksum mismatch (got %d bytes, used %v)", len(got), used)
	}
	want := []string{"socks5:" + p1 + "@" + userHash("u1"), "socks5:" + p2, "socks5:" + p3}
	if strings.Join(used, ",") != strings.Join(want, ",") {
		t.Fatalf("egresses used = %v", used)
	}
	// Two egresses are exhausted for the hoster, with the hoster's
	// Retry-After (1h).
	exhausted := 0
	for _, s := range m.Egresses() {
		if until, ok := s.Exhausted["fakehoster"]; ok {
			exhausted++
			if d := time.Until(until); d < 50*time.Minute || d > 61*time.Minute {
				t.Fatalf("Exhausted until %s (in %s)", until, d)
			}
		}
	}
	if exhausted != 2 {
		t.Fatalf("exhausted = %d", exhausted)
	}
	// No password in SQLite.
	assertNoSecret(t, st.DB(), "pw-one")
	m.Close()
	st.Close()

	// Restart: marks are still there; a second download only has p3 left
	// (50 kB of budget) and ends in WaitError.
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	m2, err := NewFromConfig(ctx, cfg, st2, secrets, nil, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	got2, used2 := download(t, m2, url, len(data), 50_000)
	if len(got2) != 50_000 || len(used2) != 2 || used2[0] != used[2] || !strings.HasPrefix(used2[1], "error:") {
		t.Fatalf("after restart got %d bytes via %v", len(got2), used2)
	}
	_, err = m2.Acquire(ctx, Request{Hoster: "fakehoster", AllowRotation: true, QuotaScope: hosters.ScopeIP})
	we, ok := AsWait(err)
	if !ok || time.Until(we.Until) < 50*time.Minute {
		t.Fatalf("err = %v", err)
	}
	// Other hosters still work.
	if e, err := m2.Acquire(ctx, Request{Hoster: "other", AllowRotation: true}); err != nil || e == nil {
		t.Fatalf("other hoster: %v", err)
	}
}

func userHash(u string) string {
	sum := sha256.Sum256([]byte(u))
	return fmt.Sprintf("%x", sum[:4])
}

// assertNoSecret scans every text column of the egresses and settings
// tables for s.
func assertNoSecret(t *testing.T, db *sql.DB, s string) {
	t.Helper()
	rows, err := db.Query(`SELECT id || label || address || settings || secret_ref || last_error FROM egresses`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(v, s) {
			t.Fatalf("secret stored in SQLite: %q", v)
		}
	}
}

func TestImportAndReload(t *testing.T) {
	ids := newIdentities()
	echo := ids.echo(t)
	pa := startProxy(t, ids, "pa", socks5.StaticCredentials{"alice": "imported-pw"})
	ids.setIP("pa", "203.0.113.10")
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "adesc.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secrets := accounts.NewMemoryStore()
	cfg := testConfig(t, echo.URL)
	cfg.Egress.Direct = true
	m, err := NewFromConfig(ctx, cfg, st, secrets, nil, Hooks{})
	if err != nil {
		t.Fatal(err)
	}

	text := "# imported\n" + socksSpec(pa, "alice", "imported-pw") + "\nnot a proxy\n" + socksSpec(pa, "alice", "imported-pw") + "\n"
	res, err := m.ImportProxies(ctx, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || len(res.Errors) != 1 {
		t.Fatalf("import = %+v", res)
	}
	id := res.Added[0]
	res2, _ := m.ImportProxies(ctx, socksSpec(pa, "alice", "imported-pw"))
	if len(res2.Added) != 0 || len(res2.Duplicates) != 1 {
		t.Fatalf("re-import = %+v", res2)
	}
	st1, err := m.TestEgress(ctx, id)
	if err != nil || !st1.Healthy || st1.PublicIP != "203.0.113.10" {
		t.Fatalf("TestEgress = %+v, %v", st1, err)
	}

	// WireGuard import (the tunnel itself is covered in netx).
	k := make([]byte, 32)
	rand.Read(k)
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.7.0.2/32\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:9\nAllowedIPs = 0.0.0.0/0\n",
		b64(k), b64(k))
	ws, err := m.AddWireGuard(ctx, "Proton NL#1", conf)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ID != "wireguard:proton-nl-1" || ws.Kind != netx.KindWireGuard {
		t.Fatalf("wg status = %+v", ws)
	}
	if _, err := m.AddWireGuard(ctx, "Proton NL#1", conf); err == nil {
		t.Fatal("duplicate WireGuard accepted")
	}
	assertNoSecret(t, st.DB(), "imported-pw")
	assertNoSecret(t, st.DB(), b64(k))
	if err := m.RemoveEgress(ctx, netx.DirectID); !errors.Is(err, ErrNotImported) {
		t.Fatalf("remove direct: %v", err)
	}
	m.Close()

	// Reload from the same store and secret store.
	m2, err := NewFromConfig(ctx, cfg, st, secrets, nil, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if m2.Get(id) == nil || m2.Get(ws.ID) == nil {
		t.Fatalf("imported egresses lost: %v", m2.Egresses())
	}
	// The password came back from the secret store: the proxy works.
	st2, err := m2.TestEgress(ctx, id)
	if err != nil || !st2.Healthy {
		t.Fatalf("reloaded proxy = %+v, %v", st2, err)
	}
	if err := m2.RemoveEgress(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := m2.RemoveEgress(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if m2.Get(id) != nil {
		t.Fatal("still present")
	}
	if _, err := secrets.Get(ctx, "egress:"+strings.SplitN(id, "#", 2)[0]); !errors.Is(err, accounts.ErrSecretNotFound) {
		t.Fatalf("secret not deleted: %v", err)
	}
	rows, _ := st.ListEgresses(ctx, store.EgressSourceImport)
	if len(rows) != 0 {
		t.Fatalf("rows left: %+v", rows)
	}
}

func b64(b []byte) string {
	k := netx.WGKey{}
	copy(k[:], b)
	return k.Base64()
}
