// Package apiclient is the Go client of the REST API (/api/v1) used by the
// CLI and by the desktop app in remote mode. This file holds the transport
// (base URL, bearer token, JSON helpers); the typed methods per endpoint
// live in downloads.go, accounts.go, network.go, system.go and events.go
// (SSE reader).
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"admindescargas/internal/config"
)

// APIPrefix is the path prefix of every endpoint.
const APIPrefix = "/api/v1"

// Client talks to a running daemon (or the desktop app's embedded API).
type Client struct {
	// BaseURL is the server root, e.g. http://127.0.0.1:7979 (no /api/v1).
	BaseURL *url.URL
	// Token is sent as "Authorization: Bearer <Token>".
	Token string
	// HTTP is the underlying client. Its Timeout must stay 0 for SSE; use
	// contexts for per-call deadlines.
	HTTP *http.Client
}

// New returns a client for baseURL using token.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("apiclient: base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("apiclient: base URL %q must be http(s)://host[:port]", baseURL)
	}
	return &Client{BaseURL: u, Token: token, HTTP: &http.Client{}}, nil
}

// FromConfig builds a client for the local daemon described by cfg
// (server.listen + server.token). Wildcard listen addresses are reached via
// loopback. TLS is assumed when server.tls_cert is set.
func FromConfig(cfg *config.Config) (*Client, error) {
	host, port, err := net.SplitHostPort(cfg.Server.Listen)
	if err != nil {
		return nil, fmt.Errorf("apiclient: server.listen: %w", err)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	scheme := "http"
	if cfg.Server.TLSCert != "" {
		scheme = "https"
	}
	return New(scheme+"://"+net.JoinHostPort(host, port), cfg.Server.Token)
}

// Error is a non-2xx answer from the API. The server sends
// {"error": {"code": "...", "message": "..."}} bodies (older servers sent
// {"error": "<message>"}; both are understood).
type Error struct {
	Status  int
	Code    string
	Message string
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("api: %d %s: %s", e.Status, http.StatusText(e.Status), e.Message)
}

// IsCode reports whether err is an *Error with the given error code.
func IsCode(err error, code string) bool {
	var ae *Error
	return errors.As(err, &ae) && ae.Code == code
}

// IsStatus reports whether err is an *Error with the given status code.
func IsStatus(err error, status int) bool {
	var ae *Error
	return errors.As(err, &ae) && ae.Status == status
}

// NewRequest builds an authenticated request for path (relative to
// APIPrefix, e.g. "/downloads"). body is JSON-encoded when non-nil.
func (c *Client) NewRequest(ctx context.Context, method, path string, query url.Values, body any) (*http.Request, error) {
	u := *c.BaseURL
	u.Path = strings.TrimRight(u.Path, "/") + APIPrefix + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("apiclient: encode body: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

// Do sends a JSON request and decodes a JSON response into out (if non-nil).
// Non-2xx responses become *Error.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	req, err := c.NewRequest(ctx, method, path, query, in)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("apiclient: decode %s %s: %w", method, path, err)
	}
	return nil
}

func decodeError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	e := &Error{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	if json.Unmarshal(b, &body) == nil && len(body.Error) > 0 {
		var detail struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		var plain string
		switch {
		case json.Unmarshal(body.Error, &detail) == nil && detail.Message != "":
			e.Code, e.Message = detail.Code, detail.Message
		case json.Unmarshal(body.Error, &plain) == nil && plain != "":
			e.Message = plain
		}
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}

// Health calls GET /api/v1/health; it succeeds when the daemon is up.
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Do(ctx, http.MethodGet, "/health", nil, nil, nil)
}
