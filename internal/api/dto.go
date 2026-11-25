package api

import (
	"time"

	"admindescargas/internal/core"
	"admindescargas/internal/netx"
)

// Request and response bodies of the REST API that are not plain core /
// accounts / rotation DTOs. apiclient and the web UI use the same JSON.

// ErrorBody is the JSON body of every non-2xx answer.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is a machine-readable code plus a human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes sent in ErrorDetail.Code.
const (
	CodeNotFound        = "not_found"
	CodeInvalidArgument = "invalid_argument"
	CodeInvalidState    = "invalid_state"
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeCSRF            = "csrf"
	CodeBadHost         = "forbidden_host"
	CodeBadOrigin       = "forbidden_origin"
	CodeRateLimited     = "rate_limited"
	CodeLocked          = "locked"
	CodeWrongPassword   = "wrong_password"
	CodeNotImplemented  = "not_implemented"
	CodeUnavailable     = "unavailable"
	CodeInternal        = "internal"
)

// OKResponse is the body of action endpoints that return nothing else.
type OKResponse struct {
	OK bool `json:"ok"`
}

// Health is the body of GET /api/v1/health.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Engine  bool   `json:"engine"`
}

// IDsRequest is the body of the download/package action endpoints and of
// POST /linkgrabber/remove.
type IDsRequest struct {
	IDs         []int64 `json:"ids"`
	DeleteFiles bool    `json:"delete_files,omitempty"`
}

// PackageDetail is the body of GET /api/v1/packages/{id}.
type PackageDetail struct {
	Package   core.Package    `json:"package"`
	Downloads []core.Download `json:"downloads"`
}

// AccountCreateRequest is the body of POST /api/v1/accounts. The secret
// fields (password, cookies, api_key) are write-only: they are passed to
// the account service and never returned.
type AccountCreateRequest struct {
	Hoster   string `json:"hoster"`
	Label    string `json:"label,omitempty"`
	Kind     string `json:"kind"`
	Username string `json:"username,omitempty"`
	// Enabled defaults to true when omitted.
	Enabled       *bool  `json:"enabled,omitempty"`
	AllowRotation bool   `json:"allow_rotation,omitempty"`
	Password      string `json:"password,omitempty"`
	Cookies       string `json:"cookies,omitempty"`
	APIKey        string `json:"api_key,omitempty"`
}

// AccountPatch is the body of PATCH /api/v1/accounts/{id}: nil fields are
// left unchanged. Secret fields are write-only.
type AccountPatch struct {
	Label         *string `json:"label,omitempty"`
	Username      *string `json:"username,omitempty"`
	Enabled       *bool   `json:"enabled,omitempty"`
	AllowRotation *bool   `json:"allow_rotation,omitempty"`
	Password      *string `json:"password,omitempty"`
	Cookies       *string `json:"cookies,omitempty"`
	APIKey        *string `json:"api_key,omitempty"`
}

// TwoFARequest is the body of POST /api/v1/accounts/{id}/2fa.
type TwoFARequest struct {
	Code string `json:"code"`
}

// ImportCookiesRequest is the body of POST /api/v1/accounts/import-cookies.
// Cookies is a Netscape cookies.txt or a JSON export.
type ImportCookiesRequest struct {
	Hoster  string `json:"hoster"`
	Label   string `json:"label,omitempty"`
	Cookies string `json:"cookies"`
}

// URLResponse is the body of GET /api/v1/oauth/google/start.
type URLResponse struct {
	URL string `json:"url"`
}

// ImportTextRequest is the body of POST /api/v1/egress/import: one proxy
// per line (scheme://user:pass@host:port).
type ImportTextRequest struct {
	Text string `json:"text"`
}

// WireGuardImportRequest is the body of POST /api/v1/egress/wireguard.
type WireGuardImportRequest struct {
	Name string `json:"name"`
	Conf string `json:"conf"`
}

// ImportResult reports an egress import. Error messages never contain the
// imported line (it may hold proxy credentials), only its number.
type ImportResult struct {
	Added   int               `json:"added"`
	Skipped int               `json:"skipped"`
	Errors  []ImportLineError `json:"errors,omitempty"`
	// IDs are the egress ids created.
	IDs []string `json:"ids,omitempty"`
}

// ImportLineError is a rejected line of an import (1-based line number;
// 0 = the whole input).
type ImportLineError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// RotateRequest is the body of POST /api/v1/rotation/rotate ("" = global).
type RotateRequest struct {
	EgressID string `json:"egress_id"`
}

// EgressIP is one entry of GET /api/v1/ip.
type EgressIP struct {
	ID       string    `json:"id"`
	Kind     netx.Kind `json:"kind"`
	PublicIP string    `json:"public_ip"`
	Error    string    `json:"error,omitempty"`
}

// IPResponse is the body of GET /api/v1/ip.
type IPResponse struct {
	Egresses []EgressIP `json:"egresses"`
}

// SelfTestResult is the body of POST /api/v1/selftest/{hoster}. A failing
// plugin is reported with OK false (HTTP 200).
type SelfTestResult struct {
	Hoster     string `json:"hoster"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// LoginRequest is the body of POST /api/v1/auth/login.
type LoginRequest struct {
	Password string `json:"password"`
}

// LoginResponse is the body of a successful login; the session itself is
// the HttpOnly cookie. CSRF must be sent as X-CSRF-Token on every non-GET
// request authenticated with the cookie.
type LoginResponse struct {
	CSRF      string    `json:"csrf"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionInfo is the body of GET /api/v1/auth/session (no auth required).
type SessionInfo struct {
	Authenticated bool `json:"authenticated"`
	// Method is "token", "cookie" or "" (not authenticated).
	Method string `json:"method,omitempty"`
	// CSRF is set for cookie sessions (lets the UI recover it after reload).
	CSRF      string    `json:"csrf,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// PasswordRequired is false when no Web UI password is configured
	// (loopback only): POST /auth/login with an empty password then opens a
	// session for local clients.
	PasswordRequired bool `json:"password_required"`
}

// EventResync is the SSE event type sent when the server cannot replay
// the events after Last-Event-ID (too old, or the daemon restarted):
// clients must refetch their lists. Its data is a core.Event with only
// type, seq and time set.
const EventResync core.EventType = "resync"
