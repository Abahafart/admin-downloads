package apiclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"admindescargas/internal/core"
)

// maxEventSize bounds one SSE line (a LinkGrabber entry of a huge folder
// can be large).
const maxEventSize = 16 << 20

// EventStream reads GET /api/v1/events. It is not safe for concurrent use.
type EventStream struct {
	body io.ReadCloser
	sc   *bufio.Scanner
	// LastID is the id of the last event returned by Next (send it as
	// Last-Event-ID when reconnecting).
	LastID string
}

// Events opens the SSE stream. lastEventID ("" = none) asks the server to
// replay what was missed; if it cannot, the first event has type
// api.EventResync ("resync") and the caller should refetch its lists.
func (c *Client) Events(ctx context.Context, lastEventID string) (*EventStream, error) {
	req, err := c.NewRequest(ctx, http.MethodGet, "/events", nil, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: GET /events: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxEventSize)
	return &EventStream{body: resp.Body, sc: sc, LastID: lastEventID}, nil
}

// Next returns the next event, skipping comments (heartbeats). It returns
// io.EOF when the server closes the stream.
func (s *EventStream) Next() (core.Event, error) {
	var (
		id, typ string
		data    []string
		hasID   bool
	)
	for s.sc.Scan() {
		line := s.sc.Text()
		if line == "" {
			if len(data) == 0 {
				id, typ, hasID = "", "", false
				continue // comment-only or empty block
			}
			var ev core.Event
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &ev); err != nil {
				return core.Event{}, fmt.Errorf("apiclient: SSE data: %w", err)
			}
			if ev.Type == "" {
				ev.Type = core.EventType(typ)
			}
			if hasID {
				s.LastID = id
			}
			return ev, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id, hasID = value, true
		case "event":
			typ = value
		case "data":
			data = append(data, value)
		}
	}
	if err := s.sc.Err(); err != nil {
		return core.Event{}, err
	}
	return core.Event{}, io.EOF
}

// Close ends the stream.
func (s *EventStream) Close() error { return s.body.Close() }

// Subscribe calls fn for every event until ctx is done or fn returns an
// error, reconnecting (with Last-Event-ID and a capped backoff) when the
// connection drops. lastID ("" = start live) resumes a previous stream.
// Authentication errors are returned immediately.
func (c *Client) Subscribe(ctx context.Context, lastID string, fn func(core.Event) error) error {
	backoff := 500 * time.Millisecond
	for {
		st, err := c.Events(ctx, lastID)
		if err == nil {
			backoff = 500 * time.Millisecond
			for {
				ev, nerr := st.Next()
				if nerr != nil {
					err = nerr
					break
				}
				lastID = st.LastID
				if ferr := fn(ev); ferr != nil {
					st.Close()
					return ferr
				}
			}
			st.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ae *Error
		if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden || ae.Status == http.StatusNotImplemented) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}
