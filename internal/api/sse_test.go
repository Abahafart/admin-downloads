package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"admindescargas/internal/core"
)

type sseMsg struct {
	id, event, data, comment string
}

type sseConn struct {
	t      *testing.T
	resp   *http.Response
	cancel context.CancelFunc
	msgs   chan sseMsg
}

func (h *harness) openSSE(t *testing.T, lastID string, opts ...reqOpt) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		cancel()
		t.Fatalf("events = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	c := &sseConn{t: t, resp: resp, cancel: cancel, msgs: make(chan sseMsg, 100)}
	go func() {
		defer close(c.msgs)
		sc := bufio.NewScanner(resp.Body)
		var m sseMsg
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if m != (sseMsg{}) {
					c.msgs <- m
				}
				m = sseMsg{}
			case strings.HasPrefix(line, ":"):
				m.comment = strings.TrimSpace(line[1:])
			case strings.HasPrefix(line, "id: "):
				m.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				m.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				m.data = line[6:]
			}
		}
	}()
	t.Cleanup(c.close)
	// The first message is the "connected" comment (after registration).
	if m := c.next(); m.comment != "connected" {
		t.Fatalf("first message = %+v", m)
	}
	return c
}

func (c *sseConn) close() { c.cancel(); c.resp.Body.Close() }

func (c *sseConn) next() sseMsg {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatal("stream closed")
		}
		return m
	case <-time.After(3 * time.Second):
		c.t.Fatal("timeout waiting for SSE message")
	}
	return sseMsg{}
}

// nextEvent skips heartbeats.
func (c *sseConn) nextEvent() sseMsg {
	c.t.Helper()
	for {
		if m := c.next(); m.event != "" {
			return m
		}
	}
}

func TestSSEDeliveryAndHeartbeat(t *testing.T) {
	h := newHarness(t)
	c := h.openSSE(t, "")
	h.engine.bus.Publish(core.Event{Type: core.EventDownloadAdded, Download: &core.Download{ID: 3, Name: "a.bin"}})
	h.engine.bus.Publish(core.Event{Type: core.EventProgress, Progress: []core.Progress{{ID: 3, Done: 10}}})
	m := c.nextEvent()
	if m.id != "1" || m.event != "download.added" {
		t.Fatalf("msg = %+v", m)
	}
	var ev core.Event
	if err := json.Unmarshal([]byte(m.data), &ev); err != nil || ev.Download == nil || ev.Download.Name != "a.bin" || ev.Seq != 1 {
		t.Fatalf("data = %s (%v)", m.data, err)
	}
	if m := c.nextEvent(); m.id != "2" || m.event != "progress" {
		t.Fatalf("msg2 = %+v", m)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m := <-c.msgs:
			if m.comment == "ping" {
				return
			}
		case <-deadline:
			t.Fatal("no heartbeat")
		}
	}
}

func TestSSELastEventIDReplay(t *testing.T) {
	h := newHarness(t)
	c := h.openSSE(t, "")
	for i := 0; i < 3; i++ {
		h.engine.bus.Publish(core.Event{Type: core.EventNotice, Message: "n" + strconv.Itoa(i)})
	}
	for i := 1; i <= 3; i++ {
		if m := c.nextEvent(); m.id != strconv.Itoa(i) {
			t.Fatalf("live %d = %+v", i, m)
		}
	}
	c.close()

	c2 := h.openSSE(t, "1")
	if m := c2.nextEvent(); m.id != "2" {
		t.Fatalf("replay first = %+v", m)
	}
	if m := c2.nextEvent(); m.id != "3" {
		t.Fatalf("replay second = %+v", m)
	}
	h.engine.bus.Publish(core.Event{Type: core.EventNotice})
	if m := c2.nextEvent(); m.id != "4" {
		t.Fatalf("live after replay = %+v", m)
	}

	// An id from the future (daemon restarted) triggers a resync.
	c3 := h.openSSE(t, "999")
	m := c3.nextEvent()
	if m.event != string(EventResync) || m.id != "4" {
		t.Fatalf("resync = %+v", m)
	}
}

func TestSSEFallbackWithoutContext(t *testing.T) {
	h := newHarness(t, func(d *Deps) { d.Context = nil })
	c := h.openSSE(t, "5")
	if m := c.nextEvent(); m.event != "resync" {
		t.Fatalf("expected resync, got %+v", m)
	}
	// Wait for the subscription, then publish.
	for h.engine.bus.Subscribers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	h.engine.bus.Publish(core.Event{Type: core.EventStats, Stats: &core.Stats{Speed: 1}})
	if m := c.nextEvent(); m.event != "stats" {
		t.Fatalf("msg = %+v", m)
	}
}

func TestSSERequiresAuth(t *testing.T) {
	h := newHarness(t)
	if status, _, _ := h.do("GET", "/api/v1/events", "", noAuth()); status != 401 {
		t.Fatalf("status = %d", status)
	}
	cookie, _ := h.login(t, "correct horse")
	h.openSSE(t, "", withCookie(cookie)) // fetch streaming with the cookie works too
}

func TestHubGapResync(t *testing.T) {
	h := &hub{ring: make([]core.Event, 2), size: 2, clients: map[chan core.Event]struct{}{}}
	ch := make(chan core.Event, 10)
	go h.run(ch)
	for i := uint64(1); i <= 5; i++ {
		ch <- core.Event{Seq: i}
	}
	close(ch)
	for {
		h.mu.Lock()
		done := h.closed
		h.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.closed = false
	h.clients = map[chan core.Event]struct{}{}
	_, replay, resync, latest, _ := h.register(1, true) // 2 and 3 are gone
	if !resync || latest != 5 || replay != nil {
		t.Fatalf("resync=%v latest=%d replay=%v", resync, latest, replay)
	}
	_, replay, resync, _, _ = h.register(3, true)
	if resync || len(replay) != 2 || replay[0].Seq != 4 {
		t.Fatalf("replay=%v resync=%v", replay, resync)
	}
}
