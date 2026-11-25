package netx

import (
	"net"
	"net/http"
	"time"
)

// NewTransport returns an http.Transport whose connections go through e.
// If e implements HTTPProxier with a non-nil URL, the transport uses it as an
// HTTP proxy; otherwise every connection is dialed with e.DialContext.
//
// The transport has sane timeouts for handshakes and response headers but no
// overall deadline (downloads may last hours): use contexts for that.
func NewTransport(e Egress) *http.Transport {
	t := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if p, ok := e.(HTTPProxier); ok {
		if u := p.ProxyURL(); u != nil {
			t.Proxy = http.ProxyURL(u)
			if pd, ok := e.(ProxyDialer); ok {
				t.DialContext = pd.DialProxy
			} else {
				d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
				t.DialContext = d.DialContext
			}
			return t
		}
	}
	// Never consult HTTP(S)_PROXY env vars: the egress decides the route.
	t.Proxy = nil
	t.DialContext = e.DialContext
	return t
}

// NewHTTPClient returns an http.Client routed through e and using jar (which
// may be nil). Use one client (and one jar) per (hoster, egress, account) so
// identities never mix. The client has no Timeout; use request contexts.
func NewHTTPClient(e Egress, jar http.CookieJar) *http.Client {
	return &http.Client{Transport: NewTransport(e), Jar: jar}
}
