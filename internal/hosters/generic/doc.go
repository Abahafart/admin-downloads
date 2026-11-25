// Package generic is the fallback plugin for direct HTTP and HTTPS links.
//
// It matches every absolute http(s) URL with a host and is registered with
// Caps.Fallback, so the registry only picks it when no specific plugin
// (MEGA, MediaFire, ...) claims the link. Resolve probes the link with HEAD
// (falling back to a one-byte ranged GET) to learn the file name
// (Content-Disposition per RFC 6266, else the URL path, else the host), the
// size and whether Range requests work. Prepare needs no I/O: the link
// itself is the direct download.
//
// FTP links are not supported (there is no FTP client in go.mod); Match
// returns false for them.
package generic
