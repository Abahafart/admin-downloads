package apiclient

import (
	"context"
	"net/http"
	"net/url"

	"admindescargas/internal/api"
	"admindescargas/internal/rotation"
)

// Egresses calls GET /egress.
func (c *Client) Egresses(ctx context.Context) ([]rotation.EgressStatus, error) {
	var out []rotation.EgressStatus
	return out, c.Do(ctx, http.MethodGet, "/egress", nil, nil, &out)
}

// ImportProxies calls POST /egress/import with one proxy per line.
func (c *Client) ImportProxies(ctx context.Context, text string) (api.ImportResult, error) {
	var out api.ImportResult
	return out, c.Do(ctx, http.MethodPost, "/egress/import", nil, api.ImportTextRequest{Text: text}, &out)
}

// ImportWireGuard calls POST /egress/wireguard with a wg-quick .conf.
func (c *Client) ImportWireGuard(ctx context.Context, name, conf string) (api.ImportResult, error) {
	var out api.ImportResult
	return out, c.Do(ctx, http.MethodPost, "/egress/wireguard", nil, api.WireGuardImportRequest{Name: name, Conf: conf}, &out)
}

// TestEgress calls POST /egress/{id}/test.
func (c *Client) TestEgress(ctx context.Context, id string) (rotation.EgressStatus, error) {
	var out rotation.EgressStatus
	return out, c.Do(ctx, http.MethodPost, "/egress/"+url.PathEscape(id)+"/test", nil, nil, &out)
}

// Rotate calls POST /rotation/rotate ("" = global rotation).
func (c *Client) Rotate(ctx context.Context, egressID string) error {
	return c.Do(ctx, http.MethodPost, "/rotation/rotate", nil, api.RotateRequest{EgressID: egressID}, nil)
}

// PublicIPs calls GET /ip.
func (c *Client) PublicIPs(ctx context.Context) ([]api.EgressIP, error) {
	var out api.IPResponse
	err := c.Do(ctx, http.MethodGet, "/ip", nil, nil, &out)
	return out.Egresses, err
}
