package desktop

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"admindescargas/internal/core"
)

// NotificationKind classifies a desktop notification.
type NotificationKind string

// Notification kinds.
const (
	NotifyCompleted NotificationKind = "completed"
	NotifyFailed    NotificationKind = "failed"
	NotifyWaiting   NotificationKind = "waiting" // waiting for quota
	NotifyNotice    NotificationKind = "notice"  // core.EventNotice
)

// Notification is a system notification to show.
type Notification struct {
	Kind  NotificationKind
	Title string
	Body  string
	// IDs are the downloads involved (empty for notices).
	IDs []int64
	// Level is set for notices.
	Level core.NoticeLevel
}

// Notifier turns the event stream into system notifications: a download
// that becomes completed, failed or waiting (quota), plus core notices.
// Notifications of the same kind arriving within Window are merged ("12
// descargas completadas") so a big folder does not flood the desktop.
type Notifier struct {
	// Emit shows a notification. Required.
	Emit func(Notification)
	// OnStats receives every stats event (tray speed). Optional.
	OnStats func(core.Stats)
	// Window is the coalescing delay (default 3s).
	Window time.Duration
	// Now overrides time.Now (tests).
	Now func() time.Time

	mu      sync.Mutex
	states  map[int64]core.State
	pending map[NotificationKind][]core.Download
	timers  map[NotificationKind]*time.Timer
}

// Handle processes one event; safe for concurrent use.
func (n *Notifier) Handle(ev core.Event) {
	switch ev.Type {
	case core.EventStats:
		if ev.Stats != nil && n.OnStats != nil {
			n.OnStats(*ev.Stats)
		}
	case core.EventNotice:
		// Notices with an ID repeat a download transition already
		// notified from download.updated below.
		if ev.Message != "" && ev.ID == 0 {
			n.Emit(Notification{Kind: NotifyNotice, Title: "AdminDescargas", Body: ev.Message, Level: ev.Level})
		}
	case core.EventDownloadAdded:
		if ev.Download != nil {
			n.setState(ev.Download.ID, ev.Download.State)
		}
	case core.EventDownloadRemoved:
		n.mu.Lock()
		delete(n.states, ev.ID)
		n.mu.Unlock()
	case core.EventDownloadUpdated:
		if ev.Download == nil {
			return
		}
		d := *ev.Download
		prev, known := n.setState(d.ID, d.State)
		if known && prev == d.State {
			return
		}
		switch d.State {
		case core.StateCompleted:
			n.queue(NotifyCompleted, d)
		case core.StateFailed:
			n.queue(NotifyFailed, d)
		case core.StateWaiting:
			n.queue(NotifyWaiting, d)
		}
	}
}

func (n *Notifier) setState(id int64, s core.State) (prev core.State, known bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.states == nil {
		n.states = map[int64]core.State{}
	}
	prev, known = n.states[id]
	n.states[id] = s
	return prev, known
}

func (n *Notifier) queue(kind NotificationKind, d core.Download) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pending == nil {
		n.pending = map[NotificationKind][]core.Download{}
		n.timers = map[NotificationKind]*time.Timer{}
	}
	n.pending[kind] = append(n.pending[kind], d)
	if n.timers[kind] == nil {
		w := n.Window
		if w <= 0 {
			w = 3 * time.Second
		}
		n.timers[kind] = time.AfterFunc(w, func() { n.flushKind(kind) })
	}
}

// Flush emits everything pending now (shutdown, tests).
func (n *Notifier) Flush() {
	for _, k := range []NotificationKind{NotifyCompleted, NotifyFailed, NotifyWaiting} {
		n.flushKind(k)
	}
}

func (n *Notifier) flushKind(kind NotificationKind) {
	n.mu.Lock()
	items := n.pending[kind]
	delete(n.pending, kind)
	if t := n.timers[kind]; t != nil {
		t.Stop()
		delete(n.timers, kind)
	}
	n.mu.Unlock()
	if len(items) == 0 {
		return
	}
	n.Emit(n.build(kind, items))
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) build(kind NotificationKind, items []core.Download) Notification {
	no := Notification{Kind: kind}
	for _, d := range items {
		no.IDs = append(no.IDs, d.ID)
	}
	if len(items) == 1 {
		d := items[0]
		switch kind {
		case NotifyCompleted:
			no.Title, no.Body = "Descarga completada", displayName(d)
		case NotifyFailed:
			no.Title = "Descarga fallida"
			no.Body = displayName(d)
			if d.Error != "" {
				no.Body += ": " + d.Error
			}
		case NotifyWaiting:
			no.Title = "En espera por cuota"
			no.Body = displayName(d) + waitingSuffix(d, n.now())
		}
		return no
	}
	switch kind {
	case NotifyCompleted:
		no.Title = fmt.Sprintf("%d descargas completadas", len(items))
	case NotifyFailed:
		no.Title = fmt.Sprintf("%d descargas fallidas", len(items))
	case NotifyWaiting:
		no.Title = fmt.Sprintf("%d descargas en espera por cuota", len(items))
	}
	const maxNames = 3
	names := make([]string, 0, maxNames)
	for i, d := range items {
		if i == maxNames {
			break
		}
		names = append(names, displayName(d))
	}
	no.Body = strings.Join(names, ", ")
	if len(items) > maxNames {
		no.Body += fmt.Sprintf(" y %d más", len(items)-maxNames)
	}
	return no
}

func displayName(d core.Download) string {
	if d.Name != "" {
		return d.Name
	}
	if d.RelPath != "" {
		return d.RelPath
	}
	return fmt.Sprintf("Descarga %d", d.ID)
}

func waitingSuffix(d core.Download, now time.Time) string {
	if d.WaitingUntil.IsZero() || !d.WaitingUntil.After(now) {
		return ""
	}
	left := d.WaitingUntil.Sub(now).Round(time.Minute)
	if left < time.Minute {
		return " · se reanuda en menos de un minuto"
	}
	return " · se reanuda en " + formatDuration(left)
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case h > 0:
		return fmt.Sprintf("%d h", h)
	default:
		return fmt.Sprintf("%d min", m)
	}
}

// FormatSpeed renders bytes/second the way the UI does, with a decimal
// comma: "0 B/s", "850 B/s", "1,5 KB/s", "12 MB/s".
func FormatSpeed(bps int64) string {
	if bps < 0 {
		bps = 0
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}
	v := float64(bps)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", bps, units[0])
	}
	if v < 10 {
		return strings.Replace(fmt.Sprintf("%.1f %s", v, units[i]), ".", ",", 1)
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}
