package core

import (
	"context"
	"testing"
	"time"
)

func TestBus(t *testing.T) {
	b := NewBus(2)
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(ctx)

	b.Publish(Event{Type: EventNotice, Message: "a"})
	b.Publish(Event{Type: EventNotice, Message: "b"})
	b.Publish(Event{Type: EventNotice, Message: "dropped"}) // buffer full

	e1, e2 := <-ch, <-ch
	if e1.Seq != 1 || e2.Seq != 2 || e1.Time.IsZero() {
		t.Fatalf("seq %d %d", e1.Seq, e2.Seq)
	}
	b.Publish(Event{Type: EventNotice, Message: "c"})
	if e := <-ch; e.Seq != 4 || e.Message != "c" {
		t.Fatalf("expected gap to seq 4, got %d %q", e.Seq, e.Message)
	}

	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if b.Subscribers() != 0 {
					t.Fatal("subscriber not removed")
				}
				b.Publish(Event{Type: EventNotice}) // must not panic
				return
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel")
		}
	}
}

func TestStates(t *testing.T) {
	for _, s := range AllStates {
		if !s.Valid() {
			t.Fatalf("%s invalid", s)
		}
	}
	if State("bogus").Valid() {
		t.Fatal("bogus valid")
	}
	if !StateDownloading.Active() || StatePaused.Active() || !StateFailed.Terminal() {
		t.Fatal("state predicates")
	}
}
