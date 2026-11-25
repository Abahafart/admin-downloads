// Package e2e holds end-to-end tests of the whole core (internal/app with
// the real wiring) driven through the REST API with apiclient. Every
// "hoster" is a local httptest server and the proxies are local SOCKS5
// servers: no external network is used.
package e2e
