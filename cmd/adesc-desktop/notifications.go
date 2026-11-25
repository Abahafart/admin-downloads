package main

import (
	"context"
	"fmt"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"admindescargas/internal/desktop"
	"admindescargas/internal/desktop/clipboard"
)

// Notification category and actions of the clipboard prompt.
const (
	clipboardCategory = "adesc-clipboard"
	actionAdd         = "add"
	actionIgnore      = "ignore"
	// Default actions (click on the notification body).
	actionDefaultWails = "DEFAULT_ACTION"
	actionDefaultMac   = "com.apple.UNNotificationDefaultActionIdentifier"
)

// askTimeout is how long a clipboard prompt waits for an answer.
const askTimeout = 2 * time.Minute

// notifications shows system notifications through the Wails v2 runtime
// (UNUserNotificationCenter, Windows toasts, org.freedesktop.Notifications)
// and implements clipboard.Prompter with "Añadir" / "Ignorar" actions. If
// notifications are unavailable the prompt falls back to a dialog.
type notifications struct {
	d *desktopApp

	ready     chan struct{}
	readyOnce sync.Once
	ok        atomic.Bool
	seq       atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan bool
}

func (n *notifications) readyCh() chan struct{} {
	n.readyOnce.Do(func() { n.ready = make(chan struct{}) })
	return n.ready
}

func (n *notifications) init() {
	defer close(n.readyCh())
	ctx := n.d.ctx
	if err := wruntime.InitializeNotifications(ctx); err != nil {
		n.d.log.Info("notifications unavailable", "err", err)
		return
	}
	if !wruntime.IsNotificationAvailable(ctx) {
		n.d.log.Info("notifications unavailable")
		return
	}
	if goruntime.GOOS == "darwin" {
		if ok, err := wruntime.RequestNotificationAuthorization(ctx); err != nil || !ok {
			n.d.log.Info("notifications not authorized", "err", err)
			return
		}
	}
	if err := wruntime.RegisterNotificationCategory(ctx, wruntime.NotificationCategory{
		ID: clipboardCategory,
		Actions: []wruntime.NotificationAction{
			{ID: actionAdd, Title: "Añadir"},
			{ID: actionIgnore, Title: "Ignorar"},
		},
	}); err != nil {
		n.d.log.Warn("notification category", "err", err)
	}
	wruntime.OnNotificationResponse(ctx, n.onResponse)
	n.ok.Store(true)
}

// available waits briefly for init and reports whether notifications work.
func (n *notifications) available() bool {
	select {
	case <-n.readyCh():
	case <-time.After(5 * time.Second):
	}
	return n.ok.Load()
}

func (n *notifications) cleanup() {
	if n.ok.Load() && n.d.ctx != nil {
		wruntime.CleanupNotifications(n.d.ctx)
	}
}

func (n *notifications) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n.seq.Add(1))
}

// show displays a notification (logged and dropped when unavailable).
func (n *notifications) show(no desktop.Notification) {
	if !n.available() {
		n.d.log.Debug("notification dropped", "kind", no.Kind)
		return
	}
	err := wruntime.SendNotification(n.d.ctx, wruntime.NotificationOptions{
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
func (n *notifications) Ask(ctx context.Context, det clipboard.Detection) (bool, error) {
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
	err := wruntime.SendNotificationWithActions(n.d.ctx, wruntime.NotificationOptions{
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

func (n *notifications) onResponse(res wruntime.NotificationResult) {
	if res.Error != nil {
		n.d.log.Debug("notification response", "err", res.Error)
		return
	}
	r := res.Response
	var yes, open bool
	switch r.ActionIdentifier {
	case actionAdd:
		yes = true
	case actionDefaultWails, actionDefaultMac:
		yes, open = true, true
	}
	n.mu.Lock()
	ch := n.pending[r.ID]
	n.mu.Unlock()
	if ch == nil {
		// A click on a download notification: open the window.
		if r.ActionIdentifier == actionDefaultWails || r.ActionIdentifier == actionDefaultMac {
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
