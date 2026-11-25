# Contributing / conventions

This repository is built by several agents working in parallel. The rules
below keep them from stepping on each other. Code, identifiers and comments
are in English; user-facing strings (CLI help, UI, notifications) may be in
Spanish.

## Package ownership

| Package | Owner | Notes |
|---|---|---|
| `internal/hosters` (contract), `fsutil`, `netx/egress.go`, `netx/client.go`, `netx/direct.go`, `accounts/accounts.go`, `rotation/rotation.go`, `core/engine.go`, `core/types.go`, `core/events.go`, `store/store.go`, `store/migrations/00001_init.sql`, `config` | scaffolding (shared contracts) | Change only with coordination: everyone codes against them. Additive changes (new optional fields, new optional interfaces) are fine. |
| `internal/core` (implementation), `internal/downloader` | engine agent | Implements `core.Engine`. |
| `internal/hosters/<name>` | hoster agents | One package per plugin, registered with `hosters.RegisterFactory` in `init`. |
| `internal/linkgrabber` | linkgrabber agent | URL extraction, folder expansion, dedupe. |
| `internal/accounts` (keyring, vault, manager) | accounts agent | Implements `accounts.SecretStore`. |
| `internal/netx` (proxies, Tor, WireGuard, router, VPN), `internal/rotation` (manager) | rotation agent | Implements `netx.Egress` and `rotation.Manager`. |
| `internal/api`, `internal/apiclient` (typed methods), `cmd/adesc/cmd_*.go` | API agent | REST + SSE + auth. |
| `web/` | UI agent | Svelte 5 + Vite 6 + TS. |
| `internal/postprocess` | postprocess agent | Checksums and extraction. |
| `cmd/adesc-desktop`, `internal/desktop` | desktop agent | Wails; cgo allowed only here, behind build tags. |
| `internal/app` | integration agent | Wiring and adapters between packages (`adapters.go`). |
| `internal/e2e` | integration agent | End-to-end tests of the whole core through the API (local servers only). |

## Dependencies

All third-party modules are already in `go.mod` / `go.sum` and pinned by
`internal/deps/deps.go` (`//go:build tools`). **Do not run `go get` or edit
go.mod in parallel work**: just import the packages listed there. If you
really need a new module, ask the integration owner. The toolchain is
Go 1.27 (`go 1.27.3` in both `go.mod` files).

`go get -u ./...` breaks two indirect modules, so `make deps-update` re-pins
them after upgrading:

- `gvisor.dev/gvisor` must stay at the pseudo-version that
  `golang.zx2c4.com/wireguard` requires; gvisor's `master` branch is not a
  valid Go module (packages and tests mix package names).
- `github.com/sethvargo/go-retry` must stay at the version goose requires
  (v0.5.0); v0.7.0 removed `retry.BackoffFunc`.

## Adding code without conflicts

- New files beat edits to shared files. The CLI registers each subcommand
  group from `init()` in its own `cmd/adesc/cmd_<group>.go`.
- Store: add your repository methods in your own file
  `internal/store/<feature>_repo.go` (methods on `*Store`). The full schema is
  already in `00001_init.sql`; if you need a change, add a new migration named
  with a UTC timestamp (`20261124153000_<what>.sql`); out-of-order migrations
  are allowed. Never edit an applied migration.
- Hoster plugins: only add files under `internal/hosters/<name>/`; they are
  already linked through `internal/hosters/all`.

## Coding conventions

- **Logging:** `log/slog` only, structured key/values. The logger comes from
  `internal/app` (or `Session.Log` in plugins).
- **No secrets in logs:** never log passwords, cookies, tokens, API keys,
  OAuth tokens, MEGA keys or signed download URLs. `accounts.Secret` redacts
  itself; plain strings do not. Clipboard text is never logged or stored.
- **Errors:** wrap with `fmt.Errorf("doing x: %w", err)`; compare with
  `errors.Is/As`. Hoster outcomes are `*hosters.Error` (use the `New*`
  constructors and `hosters.IsQuota` & co.); engine errors wrap the `core.Err*`
  sentinels so the API can map them to HTTP codes.
- **Context everywhere:** every blocking or I/O function takes a
  `context.Context` as the first argument; no `context.Background()` outside
  `main`, tests and shutdown paths.
- **Networking:** every HTTP client is built with `netx.NewHTTPClient` (or
  comes from `hosters.Session.Client`) so traffic follows the chosen egress.
  Never read proxy settings from the environment.
- **Paths:** names coming from hosters or users go through
  `fsutil.SanitizeName` / `fsutil.SafeJoin`. Build paths with `filepath`.
- **Commands:** external commands (router script, system VPN) are argv arrays
  from `config.toml`, executed with `exec.CommandContext` and no shell. The
  API must never allow editing them.
- **Cross-platform:** the daemon builds with `CGO_ENABLED=0` for linux,
  windows and darwin (amd64 + arm64). Check `GOOS=windows go build ./...` and
  `GOOS=darwin go build ./...` before handing off.

## Tests

- `go test -race ./...` must pass on Linux, Windows and macOS.
- HTTP behaviour is tested with `net/http/httptest` servers, never against
  real hosters in normal tests.
- Hoster fixtures go in `testdata/<hoster>/` (redact cookies/tokens).
- Tests that hit real services use the `live` build tag
  (`//go:build live`) and read credentials from environment variables.
- Prefer table-driven tests; use `t.TempDir()` for files.

## Web UI build output

`web/dist/index.html` is a committed placeholder so `go build` works without
Node. `make web` overwrites it with the real build; do not commit the built
files (`make web-placeholder` restores the placeholder from
`tools/web-placeholder.html`, no git needed, or use
`git update-index --skip-worktree web/dist/index.html` locally).
