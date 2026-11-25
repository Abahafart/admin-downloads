package rotation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"admindescargas/internal/netx"
)

func TestStatic(t *testing.T) {
	m := NewStatic(nil)
	e, err := m.Acquire(context.Background(), Request{Hoster: "mega"})
	if err != nil || e.ID() != netx.DirectID {
		t.Fatalf("Acquire = %v, %v", e, err)
	}
	m.Release("mega", e.ID())
	if m.Get(netx.DirectID) == nil || m.Get("x") != nil {
		t.Fatal("Get")
	}
	if st := m.Egresses(); len(st) != 1 || st[0].Kind != netx.KindDirect {
		t.Fatalf("Egresses = %+v", st)
	}
	if err := m.Rotate(context.Background(), ""); !errors.Is(err, netx.ErrRotateUnsupported) {
		t.Fatalf("Rotate = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Acquire(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Acquire = %v", err)
	}
}

func TestAsWait(t *testing.T) {
	until := time.Now().Add(time.Minute)
	err := fmt.Errorf("x: %w", &WaitError{Until: until, Reason: "all exhausted"})
	we, ok := AsWait(err)
	if !ok || !we.Until.Equal(until) {
		t.Fatal("AsWait")
	}
	if _, ok := AsWait(errors.New("other")); ok {
		t.Fatal("AsWait on unrelated error")
	}
}
