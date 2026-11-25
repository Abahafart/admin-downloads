package apiclient

import (
	"context"
	"net/http"

	"admindescargas/internal/accounts"
	"admindescargas/internal/api"
)

// SecretsStatus calls GET /secrets (backend and lock state of the secret
// store).
func (c *Client) SecretsStatus(ctx context.Context) (accounts.StoreStatus, error) {
	var out accounts.StoreStatus
	return out, c.Do(ctx, http.MethodGet, "/secrets", nil, nil, &out)
}

// UnlockSecrets calls POST /secrets/unlock. The first unlock of a new vault
// sets its master password.
func (c *Client) UnlockSecrets(ctx context.Context, password string) (accounts.StoreStatus, error) {
	var out accounts.StoreStatus
	return out, c.Do(ctx, http.MethodPost, "/secrets/unlock", nil, api.UnlockRequest{Password: password}, &out)
}

// LockSecrets calls POST /secrets/lock.
func (c *Client) LockSecrets(ctx context.Context) (accounts.StoreStatus, error) {
	var out accounts.StoreStatus
	return out, c.Do(ctx, http.MethodPost, "/secrets/lock", nil, nil, &out)
}

// ChangeMasterPassword calls POST /secrets/password.
func (c *Client) ChangeMasterPassword(ctx context.Context, oldPassword, newPassword string) error {
	return c.Do(ctx, http.MethodPost, "/secrets/password", nil,
		api.ChangePasswordRequest{OldPassword: oldPassword, NewPassword: newPassword}, nil)
}

// GoogleOAuthComplete calls POST /oauth/google/complete with the address
// the browser showed after authorizing (when it could not reach the
// daemon's callback).
func (c *Client) GoogleOAuthComplete(ctx context.Context, pastedURL string) error {
	return c.Do(ctx, http.MethodPost, "/oauth/google/complete", nil, api.OAuthCompleteRequest{URL: pastedURL}, nil)
}

// KnownLinks calls POST /links/known and returns the (normalized) links
// already queued, downloaded or in the LinkGrabber.
func (c *Client) KnownLinks(ctx context.Context, urls []string) ([]string, error) {
	var out api.KnownLinksResponse
	err := c.Do(ctx, http.MethodPost, "/links/known", nil, api.KnownLinksRequest{URLs: urls}, &out)
	return out.Known, err
}
