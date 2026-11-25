package apiclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"admindescargas/internal/api"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/rotation"
)

// partialEngine implements the few Engine methods the tests use; the
// embedded nil interface panics on anything else.
type partialEngine struct {
	core.Engine
	bus *core.Bus
	mu  sync.Mutex
	got []any
}

func (e *partialEngine) record(v any) { e.mu.Lock(); e.got = append(e.got, v); e.mu.Unlock() }

func (e *partialEngine) AddLinks(_ context.Context, r core.AddLinksRequest) ([]core.GrabberEntry, error) {
	e.record(r)
	return []core.GrabberEntry{{ID: 1, URL: r.URLs[0]}}, nil
}
func (e *partialEngine) ListDownloads(_ context.Context, f core.DownloadFilter) ([]core.Download, error) {
	e.record(f)
	return []core.Download{{ID: 1, Name: "x"}}, nil
}
func (e *partialEngine) GetDownload(_ context.Context, id int64) (core.Download, error) {
	return core.Download{}, fmt.Errorf("download %d: %w", id, core.ErrNotFound)
}
func (e *partialEngine) DeleteDownloads(_ context.Context, ids []int64, del bool) error {
	e.record(del)
	return nil
}
func (e *partialEngine) GetPackage(_ context.Context, id int64) (core.Package, []core.Download, error) {
	return core.Package{ID: id}, []core.Download{{ID: 2}}, nil
}
func (e *partialEngine) Stats(context.Context) (core.Stats, error) { return core.Stats{Active: 2}, nil }
func (e *partialEngine) Subscribe(ctx context.Context) <-chan core.Event {
	return e.bus.Subscribe(ctx)
}

func newAPIServer(t *testing.T) (*Client, *partialEngine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := &partialEngine{bus: core.NewBus(64)}
	h := api.New(api.Deps{
		Engine:   eng,
		Rotation: rotation.NewStatic(nil),
		Server:   config.ServerConfig{Listen: "127.0.0.1:7979", Token: "tok"},
		Context:  ctx,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	return c, eng
}

func TestTypedMethods(t *testing.T) {
	c, eng := newAPIServer(t)
	ctx := context.Background()
	entries, err := c.AddLinks(ctx, core.AddLinksRequest{URLs: []string{"https://example.com/f"}, Source: "cli"})
	if err != nil || len(entries) != 1 || entries[0].URL != "https://example.com/f" {
		t.Fatalf("AddLinks = %v %v", entries, err)
	}
	ds, err := c.Downloads(ctx, DownloadQuery{States: []core.State{core.StateFailed, core.StatePaused}, PackageID: 4})
	if err != nil || len(ds) != 1 {
		t.Fatalf("Downloads = %v %v", ds, err)
	}
	eng.mu.Lock()
	f := eng.got[len(eng.got)-1].(core.DownloadFilter)
	eng.mu.Unlock()
	if f.PackageID != 4 || len(f.States) != 2 {
		t.Fatalf("filter = %+v", f)
	}
	if err := c.DeleteDownloads(ctx, []int64{1}, true); err != nil {
		t.Fatal(err)
	}
	_, err = c.Download(ctx, 99)
	if !IsStatus(err, 404) || !IsCode(err, api.CodeNotFound) {
		t.Fatalf("Download err = %v", err)
	}
	if err := c.PauseDownloads(ctx, nil); !IsCode(err, api.CodeInvalidArgument) {
		t.Fatalf("empty ids err = %v", err)
	}
	p, pds, err := c.Package(ctx, 3)
	if err != nil || p.ID != 3 || len(pds) != 1 {
		t.Fatalf("Package = %v %v %v", p, pds, err)
	}
	if st, err := c.Stats(ctx); err != nil || st.Active != 2 {
		t.Fatalf("Stats = %v %v", st, err)
	}
	if eg, err := c.Egresses(ctx); err != nil || len(eg) != 1 || eg[0].ID != "direct" {
		t.Fatalf("Egresses = %v %v", eg, err)
	}
	// The direct egress cannot rotate: 409, not a 500.
	if err := c.Rotate(ctx, ""); !IsStatus(err, 409) {
		t.Fatalf("Rotate = %v", err)
	}
	if _, err := c.Accounts(ctx); !IsStatus(err, 501) {
		t.Fatalf("Accounts without service = %v", err)
	}
	if info, err := c.Session(ctx); err != nil || info.Method != "token" {
		t.Fatalf("Session = %+v %v", info, err)
	}
	if h, err := c.HealthInfo(ctx); err != nil || h.Status != "ok" {
		t.Fatalf("Health = %+v %v", h, err)
	}
	c.Token = "bad"
	if _, err := c.Stats(ctx); !IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("bad token = %v", err)
	}
}

func TestEventStreamAndSubscribe(t *testing.T) {
	c, eng := newAPIServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := c.Events(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	eng.bus.Publish(core.Event{Type: core.EventNotice, Message: "hola"})
	ev, err := st.Next()
	if err != nil || ev.Message != "hola" || ev.Type != core.EventNotice || st.LastID != "1" {
		t.Fatalf("Next = %+v %v (last %q)", ev, err, st.LastID)
	}
	st.Close()

	// Subscribe resumes with Last-Event-ID: event 2 published while
	// disconnected is replayed.
	eng.bus.Publish(core.Event{Type: core.EventNotice, Message: "dos"})
	var got []string
	err = c.Subscribe(ctx, st.LastID, func(ev core.Event) error {
		got = append(got, ev.Message)
		if len(got) == 2 {
			return io.ErrUnexpectedEOF // stop
		}
		eng.bus.Publish(core.Event{Type: core.EventNotice, Message: "tres"})
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Subscribe err = %v", err)
	}
	if len(got) != 2 || got[0] != "dos" || got[1] != "tres" {
		t.Fatalf("got = %v", got)
	}
}

func TestEventStreamParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "7" {
			http.Error(w, "missing last id", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "retry: 3000\n: connected\n\n: ping\n\nid: 8\nevent: notice\ndata: {\"seq\":8,\ndata: \"type\":\"notice\",\"message\":\"multi\"}\n\nid: 9\nevent: resync\ndata: {\"seq\":9}\n\n")
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	st, err := c.Events(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ev, err := st.Next()
	if err != nil || ev.Seq != 8 || ev.Message != "multi" {
		t.Fatalf("ev = %+v %v", ev, err)
	}
	ev, err = st.Next()
	if err != nil || ev.Type != api.EventResync || st.LastID != "9" {
		t.Fatalf("resync = %+v %v", ev, err)
	}
	if _, err := st.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v", err)
	}
}

func TestDecodeLegacyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, `{"error":"old style"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	err := c.Health(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.Message != "old style" || ae.Code != "" || !strings.Contains(err.Error(), "418") {
		t.Fatalf("err = %v", err)
	}
}
