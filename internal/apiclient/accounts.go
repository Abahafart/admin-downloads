package apiclient

import (
	"context"
	"net/http"
	"net/url"

	"admindescargas/internal/accounts"
	"admindescargas/internal/api"
)

// Accounts calls GET /accounts.
func (c *Client) Accounts(ctx context.Context) ([]accounts.Account, error) {
	var out []accounts.Account
	return out, c.Do(ctx, http.MethodGet, "/accounts", nil, nil, &out)
}

// CreateAccount calls POST /accounts. Secrets in req are sent once and
// never returned.
func (c *Client) CreateAccount(ctx context.Context, req api.AccountCreateRequest) (accounts.Account, error) {
	var out accounts.Account
	return out, c.Do(ctx, http.MethodPost, "/accounts", nil, req, &out)
}

// UpdateAccount calls PATCH /accounts/{id}.
func (c *Client) UpdateAccount(ctx context.Context, id string, p api.AccountPatch) (accounts.Account, error) {
	var out accounts.Account
	return out, c.Do(ctx, http.MethodPatch, "/accounts/"+url.PathEscape(id), nil, p, &out)
}

// DeleteAccount calls DELETE /accounts/{id}.
func (c *Client) DeleteAccount(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(id), nil, nil, nil)
}

// CheckAccount calls POST /accounts/{id}/check.
func (c *Client) CheckAccount(ctx context.Context, id string) (accounts.Account, error) {
	var out accounts.Account
	return out, c.Do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(id)+"/check", nil, nil, &out)
}

// SubmitAccount2FA calls POST /accounts/{id}/2fa.
func (c *Client) SubmitAccount2FA(ctx context.Context, id, code string) (accounts.Account, error) {
	var out accounts.Account
	return out, c.Do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(id)+"/2fa", nil, api.TwoFARequest{Code: code}, &out)
}

// ImportCookies calls POST /accounts/import-cookies (Netscape cookies.txt
// or JSON text).
func (c *Client) ImportCookies(ctx context.Context, hoster, label, cookies string) (accounts.Account, error) {
	var out accounts.Account
	return out, c.Do(ctx, http.MethodPost, "/accounts/import-cookies", nil,
		api.ImportCookiesRequest{Hoster: hoster, Label: label, Cookies: cookies}, &out)
}

// GoogleOAuthStart calls GET /oauth/google/start and returns the consent
// URL to open in a browser.
func (c *Client) GoogleOAuthStart(ctx context.Context, accountID string) (string, error) {
	var out api.URLResponse
	err := c.Do(ctx, http.MethodGet, "/oauth/google/start", url.Values{"account_id": {accountID}}, nil, &out)
	return out.URL, err
}
