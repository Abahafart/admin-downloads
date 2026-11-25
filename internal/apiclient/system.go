package apiclient

import (
	"context"
	"net/http"
	"net/url"

	"admindescargas/internal/api"
)

// HealthInfo calls GET /health and returns its body.
func (c *Client) HealthInfo(ctx context.Context) (api.Health, error) {
	var out api.Health
	return out, c.Do(ctx, http.MethodGet, "/health", nil, nil, &out)
}

// Settings calls GET /settings.
func (c *Client) Settings(ctx context.Context) (api.Settings, error) {
	var out api.Settings
	return out, c.Do(ctx, http.MethodGet, "/settings", nil, nil, &out)
}

// PutSettings calls PUT /settings and returns the resulting view.
func (c *Client) PutSettings(ctx context.Context, s api.Settings) (api.Settings, error) {
	var out api.Settings
	return out, c.Do(ctx, http.MethodPut, "/settings", nil, s, &out)
}

// SelfTest calls POST /selftest/{hoster}. A failing plugin is reported in
// the result (OK false), not as an error.
func (c *Client) SelfTest(ctx context.Context, hoster string) (api.SelfTestResult, error) {
	var out api.SelfTestResult
	return out, c.Do(ctx, http.MethodPost, "/selftest/"+url.PathEscape(hoster), nil, nil, &out)
}

// Session calls GET /auth/session.
func (c *Client) Session(ctx context.Context) (api.SessionInfo, error) {
	var out api.SessionInfo
	return out, c.Do(ctx, http.MethodGet, "/auth/session", nil, nil, &out)
}

// Login calls POST /auth/login. The session cookie is only kept if HTTP has
// a cookie jar; token clients do not need it.
func (c *Client) Login(ctx context.Context, password string) (api.LoginResponse, error) {
	var out api.LoginResponse
	return out, c.Do(ctx, http.MethodPost, "/auth/login", nil, api.LoginRequest{Password: password}, &out)
}

// Logout calls POST /auth/logout.
func (c *Client) Logout(ctx context.Context) error {
	return c.Do(ctx, http.MethodPost, "/auth/logout", nil, nil, nil)
}
