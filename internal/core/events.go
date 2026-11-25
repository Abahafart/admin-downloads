package core

import (
	"context"

	"sync"
	"time"

	"admindescargas/internal/accounts"
)

// EventType names an event; it is used as the SSE "event:" field.
type EventType string

// Event types.
const (
	EventDownloadAdded   EventType = "download.added"
	EventDownloadUpdated EventType = "download.updated" // state/error/egress change
	EventDownloadRemoved EventType = "download.removed"
	// EventProgress carries a batch of Progress, at most every ~500 ms.
	EventProgress EventType = "progress"

	EventPackageAdded   EventType = "package.added"
	EventPackageUpdated EventType = "package.updated"
	EventPackageRemoved EventType = "package.removed"

	EventGrabberAdded   EventType = "linkgrabber.added"
	EventGrabberUpdated EventType = "linkgrabber.updated" // resolving progress / ready / error
	EventGrabberRemoved EventType = "linkgrabber.removed"

	EventEgressUpdated EventType = "egress.updated"
	// EventAccountUpdated carries the account (created, edited, checked,
	// over quota); never secrets.
	EventAccountUpdated EventType = "account.updated"
	// EventAccountRemoved carries AccountID.
	EventAccountRemoved EventType = "account.removed"
	// EventSecretsUpdated: the secret store was locked or unlocked
	// (refetch GET /api/v1/secrets).
	EventSecretsUpdated EventType = "secrets.updated"
	// EventRotation reports a global rotation (Message: started/finished).
	EventRotation EventType = "rotation"
	// EventStats carries Stats, sent with every progress batch.
	EventStats EventType = "stats"
	// EventNotice is a user-facing message for desktop notifications
	// (download finished/failed/waiting for quota, links detected...).
	EventNotice EventType = "notice"
)

// Progress is the compact live state of one download.
type Progress struct {
	ID           int64     `json:"id"`
	PackageID    int64     `json:"package_id"`
	State        State     `json:"state"`
	Done         int64     `json:"done"`
	Size         int64     `json:"size"`
	Speed        int64     `json:"speed"`
	ETA          int64     `json:"eta"`
	Connections  int       `json:"connections"`
	EgressID     string    `json:"egress_id,omitempty"`
	PublicIP     string    `json:"public_ip,omitempty"`
	WaitingUntil time.Time `json:"waiting_until,omitzero"`
	// QuotaScope is "ip", "account" or "file" while waiting on a quota.
	QuotaScope string `json:"quota_scope,omitempty"`
}

// NoticeLevel is the severity of an EventNotice.
type NoticeLevel string

// Notice levels.
const (
	NoticeInfo    NoticeLevel = "info"
	NoticeSuccess NoticeLevel = "success"
	NoticeWarning NoticeLevel = "warning"
	NoticeError   NoticeLevel = "error"
)

// Event is one message on the event stream. Only the fields relevant to
// Type are set.
type Event struct {
	// Seq increases by one per published event (per process).
	Seq  uint64    `json:"seq"`
	Type EventType `json:"type"`
	Time time.Time `json:"time"`
	// ID is the affected object id for *.removed events, and the download
	// id for notices about a download's state change (completed, failed,
	// waiting for quota), which clients may skip if they already react to
	// download.updated.
	ID       int64         `json:"id,omitempty"`
	Download *Download     `json:"download,omitempty"`
	Package  *Package      `json:"package,omitempty"`
	Grabber  *GrabberEntry `json:"linkgrabber,omitempty"`
	Progress []Progress    `json:"progress,omitempty"`
	Stats    *Stats        `json:"stats,omitempty"`
	// Account / AccountID are set for account.* events.
	Account   *accounts.Account `json:"account,omitempty"`
	AccountID string            `json:"account_id,omitempty"`
	// EgressID is set for egress.updated / rotation.
	EgressID string      `json:"egress_id,omitempty"`
	Level    NoticeLevel `json:"level,omitempty"`
	Message  string      `json:"message,omitempty"`
}

// Bus is a fan-out event broadcaster for Engine implementations: Publish
// never blocks; subscribers whose buffer is full miss events (visible as a
// gap in Seq). The zero value is not usable; call NewBus.
type Bus struct {
	mu     sync.Mutex
	seq    uint64
	subs   map[chan Event]struct{}
	buffer int
	now    func() time.Time
}

// NewBus returns a Bus whose subscribers buffer up to buffer events.
func NewBus(buffer int) *Bus {
	if buffer < 1 {
		buffer = 1
	}
	return &Bus{subs: map[chan Event]struct{}{}, buffer: buffer, now: time.Now}
}

// Publish assigns Seq (and Time if zero) and delivers ev to every
// subscriber that has room.
func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	ev.Seq = b.seq
	if ev.Time.IsZero() {
		ev.Time = b.now()
	}
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop
		}
	}
}

// Subscribe returns a channel that receives events until ctx is done; the
// channel is then closed.
func (b *Bus) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, b.buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}

// Subscribers returns the number of active subscribers.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
