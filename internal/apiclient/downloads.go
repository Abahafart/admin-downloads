package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"admindescargas/internal/api"
	"admindescargas/internal/core"
)

// Actions accepted by DownloadAction / PackageAction.
const (
	ActionPause   = "pause"
	ActionResume  = "resume"
	ActionRestart = "restart"
	ActionRetry   = "retry"
	ActionDelete  = "delete"
)

// AddLinks calls POST /links.
func (c *Client) AddLinks(ctx context.Context, req core.AddLinksRequest) ([]core.GrabberEntry, error) {
	var out []core.GrabberEntry
	return out, c.Do(ctx, http.MethodPost, "/links", nil, req, &out)
}

// LinkGrabber calls GET /linkgrabber.
func (c *Client) LinkGrabber(ctx context.Context) ([]core.GrabberEntry, error) {
	var out []core.GrabberEntry
	return out, c.Do(ctx, http.MethodGet, "/linkgrabber", nil, nil, &out)
}

// ConfirmLinkGrabber calls POST /linkgrabber/confirm.
func (c *Client) ConfirmLinkGrabber(ctx context.Context, req core.ConfirmRequest) ([]core.Package, error) {
	var out []core.Package
	return out, c.Do(ctx, http.MethodPost, "/linkgrabber/confirm", nil, req, &out)
}

// RemoveLinkGrabber calls POST /linkgrabber/remove.
func (c *Client) RemoveLinkGrabber(ctx context.Context, ids []int64) error {
	return c.Do(ctx, http.MethodPost, "/linkgrabber/remove", nil, api.IDsRequest{IDs: ids}, nil)
}

// DownloadQuery filters Downloads. Zero value = everything.
type DownloadQuery struct {
	States    []core.State
	PackageID int64
	Hoster    string
	// Q is a case-insensitive substring of the name, path or URL.
	Q string
}

func (q DownloadQuery) values() url.Values {
	v := url.Values{}
	if len(q.States) > 0 {
		s := make([]string, len(q.States))
		for i, st := range q.States {
			s[i] = string(st)
		}
		v.Set("state", strings.Join(s, ","))
	}
	if q.PackageID > 0 {
		v.Set("package_id", strconv.FormatInt(q.PackageID, 10))
	}
	if q.Hoster != "" {
		v.Set("hoster", q.Hoster)
	}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	return v
}

// Downloads calls GET /downloads.
func (c *Client) Downloads(ctx context.Context, q DownloadQuery) ([]core.Download, error) {
	var out []core.Download
	return out, c.Do(ctx, http.MethodGet, "/downloads", q.values(), nil, &out)
}

// Download calls GET /downloads/{id}.
func (c *Client) Download(ctx context.Context, id int64) (core.Download, error) {
	var out core.Download
	return out, c.Do(ctx, http.MethodGet, fmt.Sprintf("/downloads/%d", id), nil, nil, &out)
}

// DownloadAction calls POST /downloads/{action} (pause, resume, restart,
// retry or delete; deleteFiles only applies to delete).
func (c *Client) DownloadAction(ctx context.Context, action string, ids []int64, deleteFiles bool) error {
	return c.Do(ctx, http.MethodPost, "/downloads/"+url.PathEscape(action), nil, api.IDsRequest{IDs: ids, DeleteFiles: deleteFiles}, nil)
}

// PauseDownloads pauses downloads.
func (c *Client) PauseDownloads(ctx context.Context, ids []int64) error {
	return c.DownloadAction(ctx, ActionPause, ids, false)
}

// ResumeDownloads resumes downloads.
func (c *Client) ResumeDownloads(ctx context.Context, ids []int64) error {
	return c.DownloadAction(ctx, ActionResume, ids, false)
}

// RestartDownloads restarts downloads from zero.
func (c *Client) RestartDownloads(ctx context.Context, ids []int64) error {
	return c.DownloadAction(ctx, ActionRestart, ids, false)
}

// RetryDownloads retries failed downloads.
func (c *Client) RetryDownloads(ctx context.Context, ids []int64) error {
	return c.DownloadAction(ctx, ActionRetry, ids, false)
}

// DeleteDownloads removes downloads (and their files with deleteFiles).
func (c *Client) DeleteDownloads(ctx context.Context, ids []int64, deleteFiles bool) error {
	return c.DownloadAction(ctx, ActionDelete, ids, deleteFiles)
}

// RetryFailed calls POST /downloads/retry-failed.
func (c *Client) RetryFailed(ctx context.Context) error {
	return c.Do(ctx, http.MethodPost, "/downloads/retry-failed", nil, nil, nil)
}

// Packages calls GET /packages.
func (c *Client) Packages(ctx context.Context) ([]core.Package, error) {
	var out []core.Package
	return out, c.Do(ctx, http.MethodGet, "/packages", nil, nil, &out)
}

// Package calls GET /packages/{id}.
func (c *Client) Package(ctx context.Context, id int64) (core.Package, []core.Download, error) {
	var out api.PackageDetail
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/packages/%d", id), nil, nil, &out)
	return out.Package, out.Downloads, err
}

// PackageAction calls POST /packages/{action}.
func (c *Client) PackageAction(ctx context.Context, action string, ids []int64, deleteFiles bool) error {
	return c.Do(ctx, http.MethodPost, "/packages/"+url.PathEscape(action), nil, api.IDsRequest{IDs: ids, DeleteFiles: deleteFiles}, nil)
}

// RescanPackage calls POST /packages/{id}/rescan and returns the new
// downloads.
func (c *Client) RescanPackage(ctx context.Context, id int64) ([]core.Download, error) {
	var out []core.Download
	return out, c.Do(ctx, http.MethodPost, fmt.Sprintf("/packages/%d/rescan", id), nil, nil, &out)
}

// PauseAll calls POST /pause-all.
func (c *Client) PauseAll(ctx context.Context) error {
	return c.Do(ctx, http.MethodPost, "/pause-all", nil, nil, nil)
}

// ResumeAll calls POST /resume-all.
func (c *Client) ResumeAll(ctx context.Context) error {
	return c.Do(ctx, http.MethodPost, "/resume-all", nil, nil, nil)
}

// Stats calls GET /stats.
func (c *Client) Stats(ctx context.Context) (core.Stats, error) {
	var out core.Stats
	return out, c.Do(ctx, http.MethodGet, "/stats", nil, nil, &out)
}
