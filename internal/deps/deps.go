//go:build tools

// Package deps pins every third-party package the project will use so that
// `go mod tidy` keeps them in go.mod/go.sum even before the code that uses
// them exists. Feature agents must NOT edit go.mod concurrently: if you need a
// package listed here, just import it. If you need a new module, coordinate
// with the integration owner and add it here.
//
// Build with: go build -tags tools ./internal/deps/
package deps

import (
	// CLI and configuration.
	_ "github.com/adrg/xdg"
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/spf13/cobra"

	// Persistence.
	_ "github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	// Networking.
	_ "golang.org/x/net/html"
	_ "golang.org/x/net/proxy"
	_ "golang.org/x/net/publicsuffix"
	_ "golang.org/x/sync/errgroup"
	_ "golang.org/x/sync/singleflight"
	_ "golang.org/x/time/rate"

	// Parsing and cryptography.
	_ "github.com/PuerkitoBio/goquery"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/crypto/curve25519"
	_ "golang.org/x/crypto/pbkdf2"
	_ "golang.org/x/text/unicode/norm"

	// Google Drive.
	_ "golang.org/x/oauth2"
	_ "golang.org/x/oauth2/google"
	_ "google.golang.org/api/drive/v3"
	_ "google.golang.org/api/option"

	// MEGA (reference / login).
	_ "github.com/t3rm1n4l/go-mega"

	// IP rotation: WireGuard userspace, Tor control, UPnP IGD.
	_ "github.com/cretz/bine/control"
	_ "github.com/cretz/bine/tor"
	_ "github.com/huin/goupnp"
	_ "github.com/huin/goupnp/dcps/internetgateway1"
	_ "github.com/huin/goupnp/dcps/internetgateway2"
	_ "golang.zx2c4.com/wireguard/conn"
	_ "golang.zx2c4.com/wireguard/device"
	_ "golang.zx2c4.com/wireguard/tun/netstack"

	// Credentials and OS service.
	_ "github.com/kardianos/service"
	_ "github.com/zalando/go-keyring"

	// Archive extraction.
	_ "github.com/bodgit/sevenzip"
	_ "github.com/nwaples/rardecode/v2"

	// Tests: fake SOCKS5 proxies.
	_ "github.com/things-go/go-socks5"

	// Desktop: single data-dir lock, autostart, clipboard.
	_ "github.com/emersion/go-autostart"
	_ "github.com/gofrs/flock"
	_ "golang.design/x/clipboard"
)
