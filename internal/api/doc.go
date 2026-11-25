// Package api serves the REST API under /api/v1, the SSE stream
// (GET /api/v1/events, fed by core.Engine.Subscribe) and the embedded Web UI
// (web.Dist), PLAN.md section 7. Build it with New(Deps); every dependency
// may be nil and the endpoints that need it then answer 501.
//
// Authentication: "Authorization: Bearer <server.token>" (CLI, extension,
// desktop) or the HttpOnly SameSite=Strict session cookie obtained with
// POST /api/v1/auth/login, which also returns a CSRF token that must be
// sent as X-CSRF-Token on every non-GET request. Public endpoints: health,
// auth/*, the OAuth callback (protected by the OAuth state) and the static
// UI. Host and Origin are validated against DNS rebinding (loopback names,
// IP literals, the listen host, server.allowed_hosts; the server's own
// origin, server.allowed_origins and Deps.ExtraOrigins, which also get CORS
// headers). Commands (router script, system VPN) are never editable
// through the API.
//
// Browser extensions (Origin chrome-extension://, moz-extension://,
// safari-web-extension://, any id) are accepted cross-origin with the Bearer
// token only: their cookies are stripped and they cannot log in.
//
// The secret vault is managed with GET /secrets and POST /secrets/unlock
// (rate limited per client like the login), /secrets/lock and
// /secrets/password; POST /oauth/google/complete finishes a Google OAuth
// flow from a pasted redirect URL; POST /links/known filters links already
// queued (desktop clipboard monitor).
//
// Errors are JSON {"error":{"code","message"}}; core.ErrNotFound -> 404,
// core.ErrInvalidArgument -> 400, core.ErrInvalidState -> 409,
// accounts.ErrLocked -> 423, accounts.ErrWrongPassword -> 403
// (code wrong_password), rotation.ErrUnknownEgress -> 404,
// rotation.ErrExists / ErrNotImported -> 409, rotation.ErrNoEgress -> 503, missing/invalid credentials -> 401, bad
// CSRF/Host/Origin -> 403, login rate limit -> 429, missing dependency ->
// 501.
//
// SSE: each message is "id: <seq>\nevent: <type>\ndata: <core.Event JSON>";
// a ": ping" comment is sent every 15 s. Reconnecting with Last-Event-ID
// replays buffered events, or sends an EventResync ("resync") event when
// they are gone so the client refetches its lists.
package api
