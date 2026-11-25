package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/app"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
)

func TestParseSchemeURL(t *testing.T) {
	tests := []struct {
		raw   string
		links []string
		text  string
		ok    bool
	}{
		{"adesc://add?url=https%3A%2F%2Fmega.nz%2Ffile%2Fx%23k", []string{"https://mega.nz/file/x#k"}, "", true},
		{"adesc://add?url=https://a.io/1&url=http://b.io/2", []string{"https://a.io/1", "http://b.io/2"}, "", true},
		{"ADESC:add?url=https://a.io/1", []string{"https://a.io/1"}, "", true},
		{"adesc:///add?url=https://a.io/1&text=hola", []string{"https://a.io/1"}, "hola", true},
		{"adesc://add?url=file:///etc/passwd&url=javascript:alert(1)", nil, "", true},
		{"adesc://delete?url=https://a.io/1", nil, "", true},
		{"https://mega.nz/file/x", nil, "", false},
		{"", nil, "", false},
	}
	for _, tt := range tests {
		links, text, ok := ParseSchemeURL(tt.raw)
		if ok != tt.ok || !slices.Equal(links, tt.links) || text != tt.text {
			t.Errorf("ParseSchemeURL(%q) = %q, %q, %v; want %q, %q, %v", tt.raw, links, text, ok, tt.links, tt.text, tt.ok)
		}
	}
}

func TestLinksFromArgs(t *testing.T) {
	links, text := LinksFromArgs([]string{"--hidden", "-config", "/x/config.toml", "https://a.io/1", "adesc://add?url=https://b.io/2&text=t", "notaurl", "ftp://c.io"})
	if !slices.Equal(links, []string{"https://a.io/1", "https://b.io/2"}) || text != "t" {
		t.Fatalf("got %q %q", links, text)
	}
}

func TestBackground(t *testing.T) {
	tests := []struct {
		a    Appearance
		sys  bool
		want RGB
	}{
		{Appearance{"grafito", TemaOscuro}, false, RGB{0x0e, 0x0f, 0x11}},
		{Appearance{"grafito", TemaClaro}, true, RGB{0xf6, 0xf7, 0xf8}},
		{Appearance{"terminal", TemaSistema}, true, RGB{0x0a, 0x0a, 0x09}},
		{Appearance{"enfoque", TemaSistema}, false, RGB{0xfa, 0xf8, 0xf5}},
		{Appearance{"monocromo", TemaOscuro}, false, RGB{0, 0, 0}},
		{Appearance{"medianoche", TemaClaro}, false, RGB{0xf4, 0xf5, 0xfb}},
		{Appearance{"desconocido", ""}, true, RGB{0x0e, 0x0f, 0x11}},
	}
	for _, tt := range tests {
		if got := tt.a.Background(tt.sys); got != tt.want {
			t.Errorf("%+v sys=%v: got %v want %v", tt.a, tt.sys, got, tt.want)
		}
	}
}

func TestAppearanceFromSettingsAndState(t *testing.T) {
	a, err := AppearanceFromSettings([]byte(`{"appearance":{"estilo":"terminal","tema":"claro","acento":"#9a5a00"},"downloads":{}}`))
	if err != nil || a != (Appearance{"terminal", "claro"}) {
		t.Fatalf("got %+v %v", a, err)
	}
	a, err = AppearanceFromSettings([]byte(`{}`))
	if err != nil || a != (Appearance{DefaultEstilo, TemaSistema}) {
		t.Fatalf("defaults: got %+v %v", a, err)
	}
	dir := t.TempDir()
	if st := LoadState(dir); st.Appearance != (Appearance{DefaultEstilo, TemaSistema}) {
		t.Fatalf("missing state: %+v", st)
	}
	if err := SaveState(dir, State{Appearance: Appearance{"enfoque", "oscuro"}}); err != nil {
		t.Fatal(err)
	}
	if st := LoadState(dir); st.Appearance != (Appearance{"enfoque", "oscuro"}) {
		t.Fatalf("round trip: %+v", st)
	}
}

func TestFormatSpeed(t *testing.T) {
	for in, want := range map[int64]string{
		-1: "0 B/s", 0: "0 B/s", 850: "850 B/s", 1536: "1,5 KB/s",
		12 << 20: "12 MB/s", 3*(1<<30) + (1 << 29): "3,5 GB/s",
	} {
		if got := FormatSpeed(in); got != want {
			t.Errorf("FormatSpeed(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSSE(t *testing.T) {
	stream := "retry: 3000\n: connected\n\nid: 7\nevent: stats\ndata: {\"seq\":7,\n" +
		"data: \"type\":\"stats\"}\n\n: ping\n\nevent: x\ndata:plain\n\n"
	var got []SSEMessage
	if err := ParseSSE(strings.NewReader(stream), func(m SSEMessage) { got = append(got, m) }); err != nil {
		t.Fatal(err)
	}
	want := []SSEMessage{
		{Retry: 3 * time.Second},
		{ID: "7", Event: "stats", Data: "{\"seq\":7,\n\"type\":\"stats\"}"},
		{Event: "x", Data: "plain"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestEventStreamReconnects(t *testing.T) {
	var mu sync.Mutex
	var lastIDs []string
	var auths []string
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		conns++
		n := conns
		lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "retry: 10\n\nid: 1\nevent: stats\ndata: {\"seq\":1,\"type\":\"stats\",\"stats\":{\"speed\":42}}\n\n")
			return // drop the connection
		}
		fmt.Fprint(w, "id: 2\nevent: notice\ndata: {\"seq\":2,\"type\":\"notice\",\"message\":\"hola\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, err := apiclient.New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan core.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	es := &EventStream{Client: c, OnEvent: func(e core.Event) { events <- e }, MinBackoff: 5 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- es.Run(ctx) }()
	for i, want := range []core.EventType{core.EventStats, core.EventNotice} {
		select {
		case e := <-events:
			if e.Type != want {
				t.Fatalf("event %d: %s, want %s", i, e.Type, want)
			}
			if i == 0 && (e.Stats == nil || e.Stats.Speed != 42) {
				t.Fatalf("stats not decoded: %+v", e)
			}
			if i == 1 && e.Message != "hola" {
				t.Fatalf("notice not decoded: %+v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lastIDs) < 2 || lastIDs[0] != "" || lastIDs[1] != "1" {
		t.Fatalf("Last-Event-ID sequence = %q", lastIDs)
	}
	if auths[0] != "Bearer tok" {
		t.Fatalf("Authorization = %q", auths[0])
	}
}

func TestNotifier(t *testing.T) {
	var mu sync.Mutex
	var got []Notification
	var speed int64
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	n := &Notifier{
		Emit:    func(no Notification) { mu.Lock(); got = append(got, no); mu.Unlock() },
		OnStats: func(s core.Stats) { speed = s.Speed },
		Window:  time.Hour, // flushed by hand
		Now:     func() time.Time { return now },
	}
	dl := func(id int64, name string, s core.State) *core.Download {
		return &core.Download{ID: id, Name: name, State: s}
	}
	n.Handle(core.Event{Type: core.EventDownloadAdded, Download: dl(1, "a.zip", core.StateQueued)})
	n.Handle(core.Event{Type: core.EventDownloadUpdated, Download: dl(1, "a.zip", core.StateDownloading)})
	n.Handle(core.Event{Type: core.EventDownloadUpdated, Download: dl(1, "a.zip", core.StateCompleted)})
	n.Handle(core.Event{Type: core.EventDownloadUpdated, Download: dl(1, "a.zip", core.StateCompleted)}) // repeated: ignored
	fail := dl(2, "b.mkv", core.StateFailed)
	fail.Error = "archivo no encontrado"
	n.Handle(core.Event{Type: core.EventDownloadUpdated, Download: fail})
	wait := dl(3, "c.iso", core.StateWaiting)
	wait.WaitingUntil = now.Add(64 * time.Minute)
	n.Handle(core.Event{Type: core.EventDownloadUpdated, Download: wait})
	n.Handle(core.Event{Type: core.EventStats, Stats: &core.Stats{Speed: 99}})
	n.Handle(core.Event{Type: core.EventNotice, Level: core.NoticeInfo, Message: "IP rotada"})
	n.Flush()

	mu.Lock()
	defer mu.Unlock()
	want := []Notification{
		{Kind: NotifyNotice, Title: "AdminDescargas", Body: "IP rotada", Level: core.NoticeInfo},
		{Kind: NotifyCompleted, Title: "Descarga completada", Body: "a.zip", IDs: []int64{1}},
		{Kind: NotifyFailed, Title: "Descarga fallida", Body: "b.mkv: archivo no encontrado", IDs: []int64{2}},
		{Kind: NotifyWaiting, Title: "En espera por cuota", Body: "c.iso · se reanuda en 1 h 4 min", IDs: []int64{3}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d notifications: %+v", len(got), got)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Title != want[i].Title || got[i].Body != want[i].Body || !slices.Equal(got[i].IDs, want[i].IDs) {
			t.Errorf("notification %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if speed != 99 {
		t.Errorf("stats speed = %d", speed)
	}
}

func TestNotifierCoalesces(t *testing.T) {
	emitted := make(chan Notification, 4)
	n := &Notifier{Emit: func(no Notification) { emitted <- no }, Window: 20 * time.Millisecond}
	for i := int64(1); i <= 5; i++ {
		n.Handle(core.Event{Type: core.EventDownloadUpdated, Download: &core.Download{ID: i, Name: fmt.Sprintf("f%d", i), State: core.StateCompleted}})
	}
	select {
	case no := <-emitted:
		if no.Title != "5 descargas completadas" || no.Body != "f1, f2, f3 y 2 más" || len(no.IDs) != 5 {
			t.Fatalf("got %+v", no)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}
	select {
	case no := <-emitted:
		t.Fatalf("extra notification %+v", no)
	case <-time.After(60 * time.Millisecond):
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.DownloadDir = t.TempDir()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.Token = "test-token"
	cfg.Log.Level = "error"
	cfg.Secrets.Backend = config.SecretsMemory // never touch the real OS keyring
	return cfg
}

func TestStartIntegratedAndDetectDaemon(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a, conn, err := StartIntegrated(ctx, cfg, nil, WebviewOrigins())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop(context.Background())
	if conn.Mode != ModeIntegrated || conn.Token != "test-token" || !strings.HasPrefix(conn.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("conn = %+v", conn)
	}
	// The webview origins are accepted by the API but never written to
	// the configuration (the settings endpoint could persist them).
	if got := a.Config().Server.AllowedOrigins; len(got) != 0 {
		t.Fatalf("allowed origins leaked into the config: %q", got)
	}
	for _, o := range append(WebviewOrigins(), "https://evil.example") {
		req, _ := http.NewRequest(http.MethodGet, conn.BaseURL+"/api/v1/stats", nil)
		req.Header.Set("Authorization", "Bearer "+conn.Token)
		req.Header.Set("Origin", o)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := http.StatusOK
		if o == "https://evil.example" {
			want = http.StatusForbidden
		}
		if resp.StatusCode != want || want == 200 && resp.Header.Get("Access-Control-Allow-Origin") != o {
			t.Fatalf("Origin %s: status %d acao %q", o, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}
	}
	if len(cfg.Server.AllowedOrigins) != 0 {
		t.Fatal("the caller's config must not be modified")
	}
	if err := CheckServer(ctx, conn.BaseURL, conn.Token); err != nil {
		t.Fatalf("CheckServer: %v", err)
	}

	// A second core on the same data dir is refused...
	_, _, err = StartIntegrated(ctx, cfg, nil, nil)
	if !errors.Is(err, app.ErrAlreadyRunning) {
		t.Fatalf("second start: %v", err)
	}
	// ...and the running one is found as a local daemon.
	local, err := DetectLocalDaemon(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if local.Mode != ModeLocalDaemon || local.BaseURL != conn.BaseURL || local.Token != "test-token" {
		t.Fatalf("local = %+v, integrated = %+v", local, conn)
	}
}

func TestStartIntegratedPortBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg := testConfig(t)
	cfg.Server.Listen = busy.Addr().String()
	a, conn, err := StartIntegrated(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop(context.Background())
	if strings.HasSuffix(conn.BaseURL, busy.Addr().String()) {
		t.Fatalf("expected a fallback port, got %s", conn.BaseURL)
	}
}

func TestDetectLocalDaemonNone(t *testing.T) {
	if _, err := DetectLocalDaemon(context.Background(), testConfig(t)); err == nil {
		t.Fatal("want error without instance file")
	}
}

func TestCheckServerBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, `{"error":"token no válido"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer srv.Close()
	if err := CheckServer(context.Background(), srv.URL, "bad"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("bad token: %v", err)
	}
	if err := CheckServer(context.Background(), srv.URL, "good"); err != nil {
		t.Fatalf("good token: %v", err)
	}
}

func TestNormalizeRemoteURL(t *testing.T) {
	for in, want := range map[string]string{
		"nas:7979":                "http://nas:7979",
		" https://nas.lan:8443/ ": "https://nas.lan:8443",
		"http://10.0.0.2:7979":    "http://10.0.0.2:7979",
	} {
		got, err := NormalizeRemoteURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRemoteURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://nas", "http://"} {
		if _, err := NormalizeRemoteURL(bad); err == nil {
			t.Errorf("NormalizeRemoteURL(%q): want error", bad)
		}
	}
}

func TestAPISinkBodies(t *testing.T) {
	type rec struct{ path, body string }
	got := make(chan rec, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- rec{r.URL.Path, string(b)}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	api, err := NewAPI(srv.URL, "t")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := api.AddToQueue(ctx, []string{"https://mega.nz/file/x#k"}); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.path != "/api/v1/links" || !strings.Contains(r.body, `"auto_confirm":true`) || !strings.Contains(r.body, `"source":"clipboard"`) {
		t.Fatalf("AddToQueue sent %+v", r)
	}
	if err := api.AddToGrabber(ctx, []string{"https://mega.nz/file/x#k"}); err != nil {
		t.Fatal(err)
	}
	if r := <-got; strings.Contains(r.body, "auto_confirm") {
		t.Fatalf("AddToGrabber sent %+v", r)
	}
	if err := api.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.path != "/api/v1/pause-all" {
		t.Fatalf("PauseAll path %q", r.path)
	}
}
