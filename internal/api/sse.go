package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"admindescargas/internal/core"
)

const sseClientBuffer = 256

// hub keeps one engine subscription, a ring of recent events (for
// Last-Event-ID replay) and fans events out to SSE clients. Slow clients
// lose events; they notice the gap in Seq and refetch (core contract).
type hub struct {
	mu      sync.Mutex
	ring    []core.Event // circular, len == cap once full
	size    int
	next    int // write position
	count   int
	clients map[chan core.Event]struct{}
	closed  bool
}

func newHub(ctx context.Context, e core.Engine, size int) *hub {
	h := &hub{ring: make([]core.Event, size), size: size, clients: map[chan core.Event]struct{}{}}
	ch := e.Subscribe(ctx)
	go h.run(ch)
	return h
}

func (h *hub) run(ch <-chan core.Event) {
	for ev := range ch {
		h.mu.Lock()
		h.ring[h.next] = ev
		h.next = (h.next + 1) % h.size
		if h.count < h.size {
			h.count++
		}
		for c := range h.clients {
			select {
			case c <- ev:
			default: // slow client: it will see a Seq gap
			}
		}
		h.mu.Unlock()
	}
	h.mu.Lock()
	h.closed = true
	for c := range h.clients {
		close(c)
	}
	h.clients = nil
	h.mu.Unlock()
}

// buffered returns the ring content oldest first; callers hold h.mu.
func (h *hub) buffered() []core.Event {
	out := make([]core.Event, 0, h.count)
	start := (h.next - h.count + h.size) % h.size
	for i := 0; i < h.count; i++ {
		out = append(out, h.ring[(start+i)%h.size])
	}
	return out
}

// register adds a client. With hasLast, it returns the buffered events
// after lastID, or resync=true when they are no longer available.
func (h *hub) register(lastID uint64, hasLast bool) (ch chan core.Event, replay []core.Event, resync bool, latest uint64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, false, 0, false
	}
	buf := h.buffered()
	if len(buf) > 0 {
		latest = buf[len(buf)-1].Seq
	}
	if hasLast {
		switch {
		case lastID == latest:
		case lastID > latest: // seq went backwards: the daemon restarted
			resync = true
		case len(buf) == 0 || buf[0].Seq > lastID+1:
			resync = true
		default:
			for _, ev := range buf {
				if ev.Seq > lastID {
					replay = append(replay, ev)
				}
			}
		}
	}
	ch = make(chan core.Event, sseClientBuffer)
	h.clients[ch] = struct{}{}
	return ch, replay, resync, latest, true
}

func (h *hub) unregister(ch chan core.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}

func lastEventID(r *http.Request) (uint64, bool) {
	v := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if v == "" {
		v = r.URL.Query().Get("last_event_id")
	}
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, true // unusable id: force a resync
	}
	return n, true
}

func (s *server) eventsHandler(w http.ResponseWriter, r *http.Request) {
	if s.d.Engine == nil {
		notImplemented(w)
		return
	}
	rc := http.NewResponseController(w)
	ctx := r.Context()
	lastID, hasLast := lastEventID(r)

	var (
		events <-chan core.Event
		replay []core.Event
		resync bool
		latest uint64
	)
	if s.events != nil {
		ch, rep, rs, lt, ok := s.events.register(lastID, hasLast)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "el servidor se está deteniendo")
			return
		}
		defer s.events.unregister(ch)
		events, replay, resync, latest = ch, rep, rs, lt
	} else {
		events = s.d.Engine.Subscribe(ctx)
		resync = hasLast
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.SetWriteDeadline(time.Time{})

	if _, err := fmt.Fprint(w, "retry: 3000\n: connected\n\n"); err != nil {
		return
	}
	if resync {
		if err := writeEvent(w, core.Event{Seq: latest, Type: EventResync, Time: s.now()}); err != nil {
			return
		}
	}
	for _, ev := range replay {
		if err := writeEvent(w, ev); err != nil {
			return
		}
	}
	if rc.Flush() != nil {
		return
	}

	tick := time.NewTicker(s.d.SSEHeartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := writeEvent(w, ev); err != nil {
				return
			}
			// Coalesce whatever is already queued into one flush.
			for drained := false; !drained; {
				select {
				case ev, ok := <-events:
					if !ok {
						_ = rc.Flush()
						return
					}
					if err := writeEvent(w, ev); err != nil {
						return
					}
				default:
					drained = true
				}
			}
			if rc.Flush() != nil {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, ev core.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
	return err
}
