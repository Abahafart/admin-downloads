package main

import (
	"context"
	"fmt"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"time"

	wnotify "github.com/wailsapp/wails/v3/pkg/services/notifications"

	"admindescargas/internal/desktop"
	"admindescargas/internal/desktop/clipboard"
)

// Notification category and actions of the clipboard prompt.
const (
	clipboardCategory = "adesc-clipboard"
	actionAdd         = "add"
	actionIgnore      = "ignore"
	// Default action (click on the notification body).
	actionDefault = wnotify.DefaultActionIdentifier
)

// askTimeout is how long a clipboard prompt waits for an answer.
const askTimeout = 2 * time.Minute

// desktopNotifications shows system notifications through the Wails v3 notification
// service (UNUserNotificationCenter, Windows toasts, org.freedesktop.Notifications)
// and implements clipboard.Prompter with "Añadir" / "Ignorar" actions. If
// notifications are unavailable the prompt falls back to a dialog.
type desktopNotifications struct {
	d   *desktopApp
	svc *wnotify.NotificationService

	ready     chan struct{}
	readyOnce sync.Once
	ok        atomic.Bool
	seq       atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan bool
}

func (n *desktopNotifications) readyCh() chan struct{} {
	n.readyOnce.Do(func() { n.ready = make(chan struct{}) })
	return n.ready
}

func (n *desktopNotifications) init() {
	defer close(n.readyCh())
	if n.svc == nil {
		n.d.log.Info("notifications service unavailable")
		return
	}
	if goruntime.GOOS == "darwin" {
		if ok, err := n.svc.RequestNotificationAuthorization(); err != nil || !ok {
			n.d.log.Info("notifications not authorized", "err", err)
			return
		}
	}
	if err := n.svc.RegisterNotificationCategory(wnotify.NotificationCategory{
		ID: clipboardCategory,
		Actions: []wnotify.NotificationAction{
			{ID: actionAdd, Title: "Añadir"},
			{ID: actionIgnore, Title: "Ignorar"},
		},
	}); err != nil {
		n.d.log.Warn("notification category", "err", err)
	}
	n.svc.OnNotificationResponse(n.onResponse)
	n.ok.Store(true)
}

// available waits briefly for init and reports whether notifications work.
func (n *desktopNotifications) available() bool {
	select {
	case <-n.readyCh():
	case <-time.After(5 * time.Second):
	}
	return n.ok.Load()
}

func (n *desktopNotifications) cleanup() {}

func (n *desktopNotifications) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n.seq.Add(1))
}

// show displays a notification (logged and dropped when unavailable).
func (n *desktopNotifications) show(no desktop.Notification) {
	if !n.available() {
		n.d.log.Debug("notification dropped", "kind", no.Kind)
		return
	}
	err := n.svc.SendNotification(wnotify.NotificationOptions{
		ID:    n.nextID(string(no.Kind)),
		Title: no.Title,
		Body:  no.Body,
	})
	if err != nil {
		n.d.log.Warn("notification", "err", err)
	}
}

// Ask implements clipboard.Prompter: a notification "3 enlaces de MEGA
// detectados" with Añadir / Ignorar. Clicking the notification also adds
// them and opens the LinkGrabber. No answer within askTimeout = ignore.
func (n *desktopNotifications) Ask(ctx context.Context, det clipboard.Detection) (bool, error) {
	if !n.available() {
		return n.d.askYesNo("Enlaces detectados", det.Summary()+".\n\n¿Añadirlos al LinkGrabber?"), nil
	}
	id := n.nextID("clipboard")
	ch := make(chan bool, 1)
	n.mu.Lock()
	if n.pending == nil {
		n.pending = map[string]chan bool{}
	}
	n.pending[id] = ch
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.pending, id)
		n.mu.Unlock()
	}()
	err := n.svc.SendNotificationWithActions(wnotify.NotificationOptions{
		ID:         id,
		Title:      det.Summary(),
		Body:       "Añadir al LinkGrabber o ignorar.",
		CategoryID: clipboardCategory,
	})
	if err != nil {
		return false, err
	}
	select {
	case yes := <-ch:
		return yes, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(askTimeout):
		return false, nil
	}
}

func (n *desktopNotifications) onResponse(res wnotify.NotificationResult) {
	if res.Error != nil {
		n.d.log.Debug("notification response", "err", res.Error)
		return
	}
	r := res.Response
	var yes, open bool
	switch r.ActionIdentifier {
	case actionAdd:
		yes = true
	case actionDefault:
		yes, open = true, true
	}
	n.mu.Lock()
	ch := n.pending[r.ID]
	n.mu.Unlock()
	if ch == nil {
		// A click on a download notification: open the window.
		if r.ActionIdentifier == actionDefault {
			n.d.showWindow()
		}
		return
	}
	select {
	case ch <- yes:
	default:
	}
	if open {
		n.d.showWindow()
		n.d.navigate("linkgrabber")
	}
}
