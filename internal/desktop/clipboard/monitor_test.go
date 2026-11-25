package clipboard

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"admindescargas/internal/config"
)

// fakeSource feeds clips from a channel.
type fakeSource struct{ ch chan Clip }

func newFakeSource() *fakeSource { return &fakeSource{ch: make(chan Clip)} }

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) Watch(ctx context.Context) (<-chan Clip, error) {
	out := make(chan Clip)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-f.ch:
				select {
				case out <- c:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

type call struct {
	kind  string // queue | grabber
	links []string
}

type fakeSink struct {
	mu    sync.Mutex
	calls []call
	err   error
	got   chan call
}

func newFakeSink() *fakeSink { return &fakeSink{got: make(chan call, 16)} }

func (s *fakeSink) record(kind string, links []string) error {
	s.mu.Lock()
	err := s.err
	c := call{kind, slices.Clone(links)}
	s.calls = append(s.calls, c)
	s.mu.Unlock()
	s.got <- c
	return err
}

func (s *fakeSink) AddToQueue(_ context.Context, links []string) error {
	return s.record("queue", links)
}

func (s *fakeSink) AddToGrabber(_ context.Context, links []string) error {
	return s.record("grabber", links)
}

type fakePrompter struct {
	answer bool
	asked  chan Detection
}

func (p *fakePrompter) Ask(_ context.Context, d Detection) (bool, error) {
	if p.asked != nil {
		p.asked <- d
	}
	return p.answer, nil
}

// testMatcher recognizes MEGA (old and new formats) and MediaFire; every
// other URL (the generic catch-all) is not a hoster link.
func testMatcher(link string) (string, bool) {
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	switch strings.TrimPrefix(u.Hostname(), "www.") {
	case "mega.nz", "mega.co.nz":
		return "mega", true
	case "mediafire.com":
		return "mediafire", true
	}
	return "", false
}

func newTestMonitor(t *testing.T, cfg config.ClipboardConfig, sink Sink, p Prompter, known KnownFilter) *Monitor {
	t.Helper()
	m, err := New(Options{
		Source:   newFakeSource(),
		Extract:  SimpleExtract,
		Match:    testMatcher,
		Known:    known,
		Sink:     sink,
		Prompter: p,
		Config:   cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func baseConfig(action string) config.ClipboardConfig {
	return config.ClipboardConfig{Enabled: true, Action: action, IgnoreKnown: true}
}

const mixedText = `Hola! Aquí van los archivos:
parte 1: https://mega.nz/file/AbCd1234#keyKEYkey-_x y la parte 2
(https://www.mediafire.com/file/xyz123/video.mkv/file).
El antiguo: https://mega.nz/#!OLDid!OLDkey, y una web cualquiera https://example.com/page.`

func TestDetectMixedText(t *testing.T) {
	m := newTestMonitor(t, baseConfig(config.ClipboardAsk), newFakeSink(), nil, nil)
	d, ok := m.Detect(context.Background(), Clip{Text: mixedText})
	if !ok {
		t.Fatal("no detection")
	}
	want := []string{
		"https://mega.nz/file/AbCd1234#keyKEYkey-_x",
		"https://www.mediafire.com/file/xyz123/video.mkv/file",
		"https://mega.nz/#!OLDid!OLDkey",
	}
	if !slices.Equal(d.Links, want) {
		t.Fatalf("links = %q, want %q", d.Links, want)
	}
	if !slices.Equal(d.Hosters, []string{"mega", "mediafire"}) || d.Counts["mega"] != 2 || d.Counts["mediafire"] != 1 {
		t.Fatalf("hosters = %v counts = %v", d.Hosters, d.Counts)
	}
	if got := d.Summary(); got != "3 enlaces detectados (MEGA, MediaFire)" {
		t.Fatalf("summary = %q", got)
	}
}

func TestDetectFilters(t *testing.T) {
	tests := []struct {
		name string
		cfg  func(*config.ClipboardConfig)
		clip Clip
		want []string // nil = no detection
	}{
		{"no links", nil, Clip{Text: "solo texto, sin enlaces"}, nil},
		{"only generic urls", nil, Clip{Text: "https://example.com/a.zip https://foo.org"}, nil},
		{"sensitive", nil, Clip{Text: "https://mega.nz/file/x#y", Sensitive: true}, nil},
		{"too large", nil, Clip{Text: "https://mega.nz/file/x#y " + strings.Repeat("a", MaxTextSize)}, nil},
		{"paused", func(c *config.ClipboardConfig) { c.Enabled = false }, Clip{Text: "https://mega.nz/file/x#y"}, nil},
		{"hoster not enabled", func(c *config.ClipboardConfig) { c.Hosters = []string{"mediafire"} }, Clip{Text: "https://mega.nz/file/x#y"}, nil},
		{"hoster enabled", func(c *config.ClipboardConfig) { c.Hosters = []string{"mediafire"} },
			Clip{Text: "https://mega.nz/file/x#y https://mediafire.com/file/a/b"}, []string{"https://mediafire.com/file/a/b"}},
		{"same link twice in one copy", nil, Clip{Text: "https://mega.nz/file/x#y\nhttps://mega.nz/file/x#y"}, []string{"https://mega.nz/file/x#y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(config.ClipboardAsk)
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			m := newTestMonitor(t, cfg, newFakeSink(), nil, nil)
			d, ok := m.Detect(context.Background(), tt.clip)
			if tt.want == nil {
				if ok {
					t.Fatalf("unexpected detection %q", d.Links)
				}
				return
			}
			if !ok || !slices.Equal(d.Links, tt.want) {
				t.Fatalf("got %q (ok=%v), want %q", d.Links, ok, tt.want)
			}
		})
	}
}

func TestDetectDedupe(t *testing.T) {
	m := newTestMonitor(t, baseConfig(config.ClipboardAsk), newFakeSink(), nil, nil)
	ctx := context.Background()
	text := "mira https://mega.nz/file/one#k"
	if _, ok := m.Detect(ctx, Clip{Text: text}); !ok {
		t.Fatal("first copy not detected")
	}
	if _, ok := m.Detect(ctx, Clip{Text: text}); ok {
		t.Fatal("same text twice detected again")
	}
	// Different text, same link: already seen.
	if _, ok := m.Detect(ctx, Clip{Text: "otra vez https://mega.nz/file/one#k"}); ok {
		t.Fatal("seen link detected again")
	}
	// Different text with one new link: only the new one.
	d, ok := m.Detect(ctx, Clip{Text: "https://mega.nz/file/one#k https://mega.nz/file/two#k"})
	if !ok || !slices.Equal(d.Links, []string{"https://mega.nz/file/two#k"}) {
		t.Fatalf("got %q ok=%v", d.Links, ok)
	}
}

func TestDetectKnownFilter(t *testing.T) {
	queued := map[string]bool{"https://mega.nz/file/queued#k": true}
	var calls int
	known := func(_ context.Context, links []string) ([]string, error) {
		calls++
		var out []string
		for _, l := range links {
			if !queued[l] {
				out = append(out, l)
			}
		}
		return out, nil
	}
	cfg := baseConfig(config.ClipboardAsk)
	m := newTestMonitor(t, cfg, newFakeSink(), nil, known)
	ctx := context.Background()
	d, ok := m.Detect(ctx, Clip{Text: "https://mega.nz/file/queued#k https://mega.nz/file/new#k"})
	if !ok || !slices.Equal(d.Links, []string{"https://mega.nz/file/new#k"}) {
		t.Fatalf("got %q ok=%v", d.Links, ok)
	}
	if _, ok := m.Detect(ctx, Clip{Text: "https://mega.nz/file/queued#k"}); ok || calls != 1 {
		t.Fatalf("known link: ok=%v calls=%d (want remembered without asking the core again)", ok, calls)
	}

	// IgnoreKnown off: the filter is not consulted.
	cfg.IgnoreKnown = false
	m2 := newTestMonitor(t, cfg, newFakeSink(), nil, known)
	if d, ok := m2.Detect(ctx, Clip{Text: "https://mega.nz/file/queued#k"}); !ok || len(d.Links) != 1 {
		t.Fatalf("IgnoreKnown=false: got %q ok=%v", d.Links, ok)
	}

	// A failing filter must not lose links.
	failing := func(context.Context, []string) ([]string, error) { return nil, errors.New("api down") }
	m3 := newTestMonitor(t, baseConfig(config.ClipboardAsk), newFakeSink(), nil, failing)
	if d, ok := m3.Detect(ctx, Clip{Text: "https://mega.nz/file/queued#k"}); !ok || len(d.Links) != 1 {
		t.Fatalf("failing filter: got %q ok=%v", d.Links, ok)
	}
}

func runMonitor(t *testing.T, m *Monitor) (*fakeSource, func()) {
	t.Helper()
	src := m.src.(*fakeSource)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	return src, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	}
}

func waitCall(t *testing.T, s *fakeSink) call {
	t.Helper()
	select {
	case c := <-s.got:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no sink call")
		return call{}
	}
}

func expectNoCall(t *testing.T, s *fakeSink) {
	t.Helper()
	select {
	case c := <-s.got:
		t.Fatalf("unexpected sink call %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunActions(t *testing.T) {
	link := "https://mediafire.com/file/a/b"
	tests := []struct {
		name     string
		action   string
		answer   bool
		wantKind string // "" = nothing delivered
	}{
		{"auto add", config.ClipboardAdd, false, "queue"},
		{"grabber only", config.ClipboardLinkGrabber, false, "grabber"},
		{"ask accepted", config.ClipboardAsk, true, "grabber"},
		{"ask ignored", config.ClipboardAsk, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := newFakeSink()
			p := &fakePrompter{answer: tt.answer, asked: make(chan Detection, 1)}
			m := newTestMonitor(t, baseConfig(tt.action), sink, p, nil)
			src, stop := runMonitor(t, m)
			defer stop()
			src.ch <- Clip{Text: "copiado: " + link}
			if tt.action == config.ClipboardAsk {
				select {
				case d := <-p.asked:
					if d.Summary() != "1 enlace de MediaFire detectado" {
						t.Errorf("summary = %q", d.Summary())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("not asked")
				}
			}
			if tt.wantKind == "" {
				expectNoCall(t, sink)
				return
			}
			c := waitCall(t, sink)
			if c.kind != tt.wantKind || !slices.Equal(c.links, []string{link}) {
				t.Fatalf("call = %+v", c)
			}
		})
	}
}

func TestRunPauseResume(t *testing.T) {
	sink := newFakeSink()
	m := newTestMonitor(t, baseConfig(config.ClipboardLinkGrabber), sink, nil, nil)
	src, stop := runMonitor(t, m)
	defer stop()

	m.SetEnabled(false)
	src.ch <- Clip{Text: "https://mega.nz/file/p#k"}
	expectNoCall(t, sink)

	m.SetEnabled(true)
	src.ch <- Clip{Text: "https://mega.nz/file/r#k"}
	if c := waitCall(t, sink); !slices.Equal(c.links, []string{"https://mega.nz/file/r#k"}) {
		t.Fatalf("call = %+v", c)
	}

	// Switching the action at runtime.
	cfg := m.Config()
	cfg.Action = config.ClipboardAdd
	m.SetConfig(cfg)
	src.ch <- Clip{Text: "https://mega.nz/file/s#k"}
	if c := waitCall(t, sink); c.kind != "queue" {
		t.Fatalf("call = %+v", c)
	}
}

func TestDeliveryErrorAllowsRetry(t *testing.T) {
	sink := newFakeSink()
	sink.err = errors.New("api down")
	m := newTestMonitor(t, baseConfig(config.ClipboardLinkGrabber), sink, nil, nil)
	ctx := context.Background()
	text := "https://mega.nz/file/x#k"
	d, ok := m.Detect(ctx, Clip{Text: text})
	if !ok {
		t.Fatal("no detection")
	}
	if err := m.Handle(ctx, d); err == nil {
		t.Fatal("want delivery error")
	}
	<-sink.got
	if _, ok := m.Detect(ctx, Clip{Text: text}); !ok {
		t.Fatal("after a failed delivery copying the same text again must retry")
	}
}

// TestNeverLogsClipboardText checks the privacy rule: nothing the user
// copied reaches the logs, even at debug level and on errors.
func TestNeverLogsClipboardText(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := newFakeSink()
	sink.err = errors.New("api down")
	m, err := New(Options{
		Source: newFakeSource(), Extract: SimpleExtract, Match: testMatcher, Sink: sink,
		Config: baseConfig(config.ClipboardLinkGrabber), Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	src, stop := runMonitor(t, m)
	secret := "contraseña-secreta-123 https://mega.nz/file/SECRETID#SECRETKEY"
	src.ch <- Clip{Text: secret}
	waitCall(t, sink)
	src.ch <- Clip{Text: "nada que ver: correo@example.com"}
	time.Sleep(20 * time.Millisecond)
	stop()
	for _, s := range []string{"contraseña", "SECRETID", "SECRETKEY", "correo@"} {
		if strings.Contains(buf.String(), s) {
			t.Fatalf("log contains clipboard content %q:\n%s", s, buf.String())
		}
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("want error for empty options")
	}
}

func TestSimpleExtract(t *testing.T) {
	got := SimpleExtract(`a (https://x.io/a_(b)) "https://y.io/q?a=1&b=2". <https://z.io/>; HTTPS://UP.io/X!`)
	want := []string{"https://x.io/a_(b)", "https://y.io/q?a=1&b=2", "https://z.io/", "HTTPS://UP.io/X"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := SimpleExtract("sin enlaces http:// aquí"); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestLRU(t *testing.T) {
	l := newLRU(2)
	l.add("a")
	l.add("b")
	l.add("c")
	if l.contains("a") || !l.contains("b") || !l.contains("c") {
		t.Fatal("eviction order")
	}
	l.remove("b")
	if l.contains("b") || len(l.order) != 1 {
		t.Fatal("remove")
	}
}
