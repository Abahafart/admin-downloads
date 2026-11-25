package netx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// DefaultIPEchoServices are plain-text "what is my IP" endpoints queried by
// PublicIP, in order. config.Rotation.IPCheckServices overrides them.
var DefaultIPEchoServices = []string{
	"https://api.ipify.org",
	"https://icanhazip.com",
	"https://ifconfig.me/ip",
	"https://checkip.amazonaws.com",
	"https://ipinfo.io/ip",
}

// PublicIP returns the public IP seen through e using DefaultIPEchoServices.
func PublicIP(ctx context.Context, e Egress) (netip.Addr, error) {
	return PublicIPWith(ctx, NewHTTPClient(e, nil), DefaultIPEchoServices)
}

// PublicIPWith queries services in order with client and returns the first
// valid IP address. Each service gets at most 10 seconds. It returns an
// error wrapping ErrNoPublicIP (and the individual failures) if none works.
func PublicIPWith(ctx context.Context, client *http.Client, services []string) (netip.Addr, error) {
	var errs []error
	for _, svc := range services {
		ip, err := queryEcho(ctx, client, svc)
		if err == nil {
			return ip, nil
		}
		if ctx.Err() != nil {
			return netip.Addr{}, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", svc, err))
	}
	return netip.Addr{}, fmt.Errorf("%w: %w", ErrNoPublicIP, errors.Join(errs...))
}

func queryEcho(ctx context.Context, client *http.Client, svc string) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "curl/8.5.0") // some services return HTML to browsers
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid answer: %w", err)
	}
	return ip.Unmap(), nil
}
