package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/core"
)

// EventStream follows GET /api/v1/events (SSE) with the bearer token and
// reconnects with backoff, sending Last-Event-ID so the server can replay
// what was missed (or send a resync event).
type EventStream struct {
	Client *apiclient.Client
	// OnEvent is called for every event, in order, from the Run goroutine.
	OnEvent func(core.Event)
	// OnState is called with true after each (re)connection and false with
	// the error when the stream drops. Optional.
	OnState func(connected bool, err error)
	// MinBackoff / MaxBackoff bound the reconnect delay (default 1s / 30s).
	MinBackoff, MaxBackoff time.Duration
}

// maxEventSize bounds one SSE event (a big linkgrabber tree).
const maxEventSize = 8 << 20

// Run streams until ctx is done.
func (s *EventStream) Run(ctx context.Context) error {
	minB, maxB := s.MinBackoff, s.MaxBackoff
	if minB <= 0 {
		minB = time.Second
	}
	if maxB < minB {
		maxB = 30 * time.Second
	}
	backoff := minB
	var lastID string
	for {
		connected := false
		retry, err := s.once(ctx, &lastID, func() {
			connected = true
			backoff = minB
			if s.OnState != nil {
				s.OnState(true, nil)
			}
		})
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutdown: the error is just the cancellation
		}
		if s.OnState != nil {
			s.OnState(false, err)
		}
		wait := backoff
		if retry > 0 && connected {
			wait = retry
		}
		// Jitter: +-20%.
		wait = time.Duration(float64(wait) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // jitter, not security
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if !connected {
			backoff = min(backoff*2, maxB)
		}
	}
}

// once runs one connection. It returns the server's retry hint.
func (s *EventStream) once(ctx context.Context, lastID *string, onOpen func()) (time.Duration, error) {
	req, err := s.Client.NewRequest(ctx, http.MethodGet, "/events", nil, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if *lastID != "" {
		req.Header.Set("Last-Event-ID", *lastID)
	}
	resp, err := s.Client.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, &apiclient.Error{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return 0, fmt.Errorf("events: unexpected content type %q", ct)
	}
	onOpen()
	var retry time.Duration
	err = ParseSSE(resp.Body, func(m SSEMessage) {
		if m.ID != "" {
			*lastID = m.ID
		}
		if m.Retry > 0 {
			retry = m.Retry
		}
		if m.Event == "" && m.Data == "" {
			return
		}
		ev := core.Event{Type: core.EventType(m.Event)}
		if m.Data != "" {
			// A body that is not an Event still reaches OnEvent with its
			// type (e.g. a future control message).
			_ = json.Unmarshal([]byte(m.Data), &ev)
			if m.Event != "" {
				ev.Type = core.EventType(m.Event)
			}
		}
		if s.OnEvent != nil {
			s.OnEvent(ev)
		}
	})
	if err == nil {
		err = io.EOF
	}
	return retry, err
}

// SSEMessage is one parsed server-sent event.
type SSEMessage struct {
	ID    string
	Event string
	Data  string
	Retry time.Duration
}

// ParseSSE reads an event stream and calls fn per dispatched message
// (comments are skipped; a message with only retry: is dispatched too so
// the caller sees the hint). It returns the read error (nil at EOF).
func ParseSSE(r io.Reader, fn func(SSEMessage)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxEventSize)
	var m SSEMessage
	var data []string
	dirty := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if dirty {
				m.Data = strings.Join(data, "\n")
				fn(m)
			}
			m, data, dirty = SSEMessage{}, nil, false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			m.ID, dirty = value, true
		case "event":
			m.Event, dirty = value, true
		case "data":
			data, dirty = append(data, value), true
		case "retry":
			if ms, err := strconv.Atoi(value); err == nil && ms > 0 {
				m.Retry, dirty = time.Duration(ms)*time.Millisecond, true
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
