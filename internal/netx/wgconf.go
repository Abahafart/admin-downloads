package netx

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// WGKey is a WireGuard Curve25519 key.
type WGKey [32]byte

// ParseWGKey decodes a base64 key as written in wg-quick files.
func ParseWGKey(s string) (WGKey, error) {
	var k WGKey
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(k) {
		return k, errors.New("invalid key (want 32 bytes, base64)")
	}
	copy(k[:], b)
	return k, nil
}

// IsZero reports whether the key is unset.
func (k WGKey) IsZero() bool { return k == WGKey{} }

// Base64 encodes the key as in wg-quick files.
func (k WGKey) Base64() string { return base64.StdEncoding.EncodeToString(k[:]) }

// hex encodes the key for the UAPI (IpcSet).
func (k WGKey) hex() string { return hex.EncodeToString(k[:]) }

// WGPrivateKey is a private key; it never prints itself.
type WGPrivateKey WGKey

// String implements fmt.Stringer without revealing the key.
func (WGPrivateKey) String() string { return "[REDACTED]" }

// GoString implements fmt.GoStringer without revealing the key.
func (WGPrivateKey) GoString() string { return "[REDACTED]" }

// LogValue implements slog.LogValuer without revealing the key.
func (WGPrivateKey) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// PublicKey derives the public key.
func (k WGPrivateKey) PublicKey() WGKey {
	var pub WGKey
	out, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err == nil {
		copy(pub[:], out)
	}
	return pub
}

// WGPeer is a [Peer] section.
type WGPeer struct {
	PublicKey WGKey
	// PresharedKey is optional; zero = none. Secret: never log it.
	PresharedKey        WGPrivateKey
	Endpoint            string // host:port (host may be a name)
	AllowedIPs          []netip.Prefix
	PersistentKeepalive int // seconds, 0 = off
}

// WGConfig is a parsed wg-quick .conf. Only the keys meaningful for a
// userspace tunnel are kept; wg-quick hooks (PreUp, PostUp...), Table,
// FwMark and SaveConfig are ignored and never executed.
type WGConfig struct {
	Name       string // file name / user label (not in the file)
	PrivateKey WGPrivateKey
	Addresses  []netip.Prefix
	DNS        []netip.Addr
	MTU        int // 0 = DefaultWGMTU
	ListenPort int
	Peers      []WGPeer
}

// DefaultWGMTU is used when the .conf has no MTU.
const DefaultWGMTU = 1420

// ParseWGConfig parses a wg-quick configuration.
func ParseWGConfig(text string) (*WGConfig, error) {
	c := &WGConfig{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 4096), 256*1024)
	n := 0
	seenInterface := false
	for sc.Scan() {
		n++
		line := sc.Text()
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("wireguard conf line %d: bad section header", n)
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch section {
			case "interface":
				if seenInterface {
					return nil, fmt.Errorf("wireguard conf line %d: duplicate [Interface]", n)
				}
				seenInterface = true
			case "peer":
				c.Peers = append(c.Peers, WGPeer{})
			default:
				return nil, fmt.Errorf("wireguard conf line %d: unknown section [%s]", n, section)
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("wireguard conf line %d: want key = value", n)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		var err error
		switch section {
		case "interface":
			err = c.setInterface(key, val)
		case "peer":
			err = c.Peers[len(c.Peers)-1].set(key, val)
		default:
			err = errors.New("key outside of a section")
		}
		if err != nil {
			return nil, fmt.Errorf("wireguard conf line %d (%s): %w", n, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("wireguard conf: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *WGConfig) setInterface(key, val string) error {
	switch key {
	case "privatekey":
		k, err := ParseWGKey(val)
		if err != nil {
			return err
		}
		c.PrivateKey = WGPrivateKey(k)
	case "address":
		for _, s := range splitList(val) {
			p, err := parsePrefixOrAddr(s)
			if err != nil {
				return err
			}
			c.Addresses = append(c.Addresses, p)
		}
	case "dns":
		for _, s := range splitList(val) {
			if a, err := netip.ParseAddr(s); err == nil {
				c.DNS = append(c.DNS, a.Unmap())
			} // non-IP entries are search domains: not used by netstack
		}
	case "mtu":
		v, err := strconv.Atoi(val)
		if err != nil || v < 576 || v > 65535 {
			return errors.New("invalid MTU")
		}
		c.MTU = v
	case "listenport":
		v, err := strconv.Atoi(val)
		if err != nil || v < 0 || v > 65535 {
			return errors.New("invalid port")
		}
		c.ListenPort = v
	case "table", "fwmark", "preup", "postup", "predown", "postdown", "saveconfig":
		// wg-quick only; ignored on purpose (never executed).
	default:
		return errors.New("unknown key")
	}
	return nil
}

func (p *WGPeer) set(key, val string) error {
	switch key {
	case "publickey":
		k, err := ParseWGKey(val)
		if err != nil {
			return err
		}
		p.PublicKey = k
	case "presharedkey":
		k, err := ParseWGKey(val)
		if err != nil {
			return err
		}
		p.PresharedKey = WGPrivateKey(k)
	case "endpoint":
		host, port, err := net.SplitHostPort(val)
		if err != nil || host == "" {
			return errors.New("want host:port")
		}
		if v, err := strconv.Atoi(port); err != nil || v < 1 || v > 65535 {
			return errors.New("invalid port")
		}
		p.Endpoint = val
	case "allowedips":
		for _, s := range splitList(val) {
			pr, err := parsePrefixOrAddr(s)
			if err != nil {
				return err
			}
			p.AllowedIPs = append(p.AllowedIPs, pr.Masked())
		}
	case "persistentkeepalive":
		if strings.EqualFold(val, "off") {
			p.PersistentKeepalive = 0
			return nil
		}
		v, err := strconv.Atoi(val)
		if err != nil || v < 0 || v > 65535 {
			return errors.New("invalid keepalive")
		}
		p.PersistentKeepalive = v
	default:
		return errors.New("unknown key")
	}
	return nil
}

// Validate checks the fields a userspace client tunnel needs.
func (c *WGConfig) Validate() error {
	switch {
	case WGKey(c.PrivateKey).IsZero():
		return errors.New("wireguard conf: missing [Interface] PrivateKey")
	case len(c.Addresses) == 0:
		return errors.New("wireguard conf: missing [Interface] Address")
	case len(c.Peers) == 0:
		return errors.New("wireguard conf: missing [Peer]")
	}
	for i, p := range c.Peers {
		if p.PublicKey.IsZero() {
			return fmt.Errorf("wireguard conf: peer %d: missing PublicKey", i+1)
		}
		if len(p.AllowedIPs) == 0 {
			return fmt.Errorf("wireguard conf: peer %d: missing AllowedIPs", i+1)
		}
	}
	if c.Peers[0].Endpoint == "" {
		return errors.New("wireguard conf: first peer has no Endpoint")
	}
	return nil
}

// LocalAddrs returns the interface addresses (without prefix length).
func (c *WGConfig) LocalAddrs() []netip.Addr {
	out := make([]netip.Addr, 0, len(c.Addresses))
	for _, p := range c.Addresses {
		out = append(out, p.Addr())
	}
	return out
}

// EffectiveMTU returns MTU or DefaultWGMTU.
func (c *WGConfig) EffectiveMTU() int {
	if c.MTU == 0 {
		return DefaultWGMTU
	}
	return c.MTU
}

// Endpoint returns the first peer's endpoint (for labels).
func (c *WGConfig) Endpoint() string {
	if len(c.Peers) == 0 {
		return ""
	}
	return c.Peers[0].Endpoint
}

// Resolver resolves endpoint host names (net.DefaultResolver satisfies it).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// IpcConfig renders the UAPI configuration for device.IpcSet, resolving
// endpoint names with r (net.DefaultResolver if nil). The result contains
// the private key: never log it.
func (c *WGConfig) IpcConfig(ctx context.Context, r Resolver) (string, error) {
	if r == nil {
		r = net.DefaultResolver
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", WGKey(c.PrivateKey).hex())
	if c.ListenPort > 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", c.ListenPort)
	}
	b.WriteString("replace_peers=true\n")
	for _, p := range c.Peers {
		fmt.Fprintf(&b, "public_key=%s\n", p.PublicKey.hex())
		if !WGKey(p.PresharedKey).IsZero() {
			fmt.Fprintf(&b, "preshared_key=%s\n", WGKey(p.PresharedKey).hex())
		}
		if p.Endpoint != "" {
			ep, err := resolveEndpoint(ctx, r, p.Endpoint)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "endpoint=%s\n", ep)
		}
		if p.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.PersistentKeepalive)
		}
		b.WriteString("replace_allowed_ips=true\n")
		for _, a := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", a)
		}
	}
	return b.String(), nil
}

func resolveEndpoint(ctx context.Context, r Resolver, ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", fmt.Errorf("wireguard endpoint %q: %w", ep, err)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return netip.AddrPortFrom(a.Unmap(), mustPort(port)).String(), nil
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return "", fmt.Errorf("wireguard endpoint %q: cannot resolve: %w", ep, err)
	}
	// Prefer IPv4: many networks lack IPv6 connectivity.
	best := addrs[0].Unmap()
	for _, a := range addrs {
		if a.Unmap().Is4() {
			best = a.Unmap()
			break
		}
	}
	return netip.AddrPortFrom(best, mustPort(port)).String(), nil
}

func mustPort(s string) uint16 {
	v, _ := strconv.ParseUint(s, 10, 16)
	return uint16(v)
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parsePrefixOrAddr(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid prefix %q", s)
		}
		return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid address %q", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}
