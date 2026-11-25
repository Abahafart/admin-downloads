package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/core"
	"admindescargas/internal/linkgrabber"
)

// API is the small set of REST calls the native shell needs (tray,
// clipboard, dialogs). It works the same against the in-process core and a
// remote daemon: the desktop app always goes through HTTP, like the UI.
// It implements clipboard.Sink.
type API struct {
	C *apiclient.Client
}

// NewAPI returns an API for baseURL authenticated with token.
func NewAPI(baseURL, token string) (*API, error) {
	c, err := apiclient.New(baseURL, token)
	if err != nil {
		return nil, err
	}
	return &API{C: c}, nil
}

// Health checks the server is up.
func (a *API) Health(ctx context.Context) error { return a.C.Health(ctx) }

// AddLinks sends links (and free text) to the LinkGrabber; autoConfirm
// queues them as soon as they resolve. source is informational
// (clipboard, desktop...).
func (a *API) AddLinks(ctx context.Context, links []string, text, source string, autoConfirm bool) error {
	req := core.AddLinksRequest{URLs: links, Text: text, Source: source, AutoConfirm: autoConfirm}
	return a.C.Do(ctx, http.MethodPost, "/links", nil, req, nil)
}

// UnknownLinks returns the links that are not queued, downloaded or in the
// LinkGrabber yet (clipboard.KnownFilter, via POST /api/v1/links/known).
func (a *API) UnknownLinks(ctx context.Context, links []string) ([]string, error) {
	known, err := a.C.KnownLinks(ctx, links)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(known))
	for _, k := range known {
		set[k] = true
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		if !set[linkgrabber.NormalizeURL(l)] {
			out = append(out, l)
		}
	}
	return out, nil
}

// AddToQueue implements clipboard.Sink (action "add").
func (a *API) AddToQueue(ctx context.Context, links []string) error {
	return a.AddLinks(ctx, links, "", "clipboard", true)
}

// AddToGrabber implements clipboard.Sink (action "linkgrabber" / "ask").
func (a *API) AddToGrabber(ctx context.Context, links []string) error {
	return a.AddLinks(ctx, links, "", "clipboard", false)
}

// PauseAll pauses every transfer (tray action).
func (a *API) PauseAll(ctx context.Context) error {
	return a.C.Do(ctx, http.MethodPost, "/pause-all", nil, nil, nil)
}

// ResumeAll resumes everything paused by PauseAll (tray action).
func (a *API) ResumeAll(ctx context.Context) error {
	return a.C.Do(ctx, http.MethodPost, "/resume-all", nil, nil, nil)
}

// Stats returns the global counters (total speed for the tray).
func (a *API) Stats(ctx context.Context) (core.Stats, error) {
	var st core.Stats
	err := a.C.Do(ctx, http.MethodGet, "/stats", nil, nil, &st)
	return st, err
}

// Appearance reads settings.appearance.
func (a *API) Appearance(ctx context.Context) (Appearance, error) {
	req, err := a.C.NewRequest(ctx, http.MethodGet, "/settings", nil, nil)
	if err != nil {
		return Appearance{}, err
	}
	resp, err := a.C.HTTP.Do(req)
	if err != nil {
		return Appearance{}, fmt.Errorf("settings: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Appearance{}, fmt.Errorf("settings: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Appearance{}, &apiclient.Error{Status: resp.StatusCode, Message: string(b)}
	}
	return AppearanceFromSettings(b)
}

// ImportProxies posts a proxy list (one entry per line).
func (a *API) ImportProxies(ctx context.Context, text string) (json.RawMessage, error) {
	var out json.RawMessage
	err := a.C.Do(ctx, http.MethodPost, "/egress/import", nil, map[string]string{"text": text}, &out)
	return out, err
}

// ImportWireGuard posts a wg-quick .conf.
func (a *API) ImportWireGuard(ctx context.Context, name, conf string) (json.RawMessage, error) {
	var out json.RawMessage
	err := a.C.Do(ctx, http.MethodPost, "/egress/wireguard", nil, map[string]string{"name": name, "conf": conf}, &out)
	return out, err
}

// ImportCookies posts a cookies.txt (Netscape) or JSON export for hoster.
func (a *API) ImportCookies(ctx context.Context, hoster, label, cookies string) (json.RawMessage, error) {
	var out json.RawMessage
	body := map[string]string{"hoster": hoster, "label": label, "cookies": cookies}
	err := a.C.Do(ctx, http.MethodPost, "/accounts/import-cookies", nil, body, &out)
	return out, err
}
