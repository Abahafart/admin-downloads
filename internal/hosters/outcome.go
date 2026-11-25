package hosters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind classifies the outcome of a hoster operation.
type Kind int

// Outcome kinds. KindOK is only returned by KindOf(nil).
const (
	KindOK        Kind = iota
	KindQuota          // quota/limit reached; see Error.Scope and RetryAfter
	KindNeedsAuth      // login/cookie/premium required, or credentials rejected
	KindNotFound       // file/folder deleted or never existed
	KindExpired        // direct link expired: call Prepare again
	KindTemporary      // transient (network, 5xx): retry with backoff
	KindFatal          // permanent: do not retry
)

var kindNames = [...]string{"ok", "quota", "needs_auth", "not_found", "expired", "temporary", "fatal"}

// String implements fmt.Stringer (also used as downloads.error_kind).
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// QuotaScope tells what a quota is tied to, and therefore what can lift it.
type QuotaScope int

// Quota scopes.
const (
	ScopeNone    QuotaScope = iota // not a quota error
	ScopeIP                        // per source IP: rotate the egress
	ScopeAccount                   // per account: switch account
	ScopeFile                      // per file (Drive): copy trick or wait
)

var scopeNames = [...]string{"", "ip", "account", "file"}

// String implements fmt.Stringer (also stored in quota_marks.scope).
func (s QuotaScope) String() string {
	if int(s) < len(scopeNames) {
		return scopeNames[s]
	}
	return "scope(" + strconv.Itoa(int(s)) + ")"
}

// ParseQuotaScope is the inverse of String.
func ParseQuotaScope(s string) (QuotaScope, error) {
	for i, n := range scopeNames {
		if i > 0 && n == s {
			return QuotaScope(i), nil
		}
	}
	return ScopeNone, fmt.Errorf("hosters: unknown quota scope %q", s)
}

// Error is the typed error returned by Classify, Resolve and Prepare to tell
// the engine what to do next.
type Error struct {
	Kind Kind
	// Scope is set for KindQuota.
	Scope QuotaScope
	// RetryAfter is the wait advertised by the hoster (0 = unknown, use the
	// configured default). Also used as a hint for KindTemporary.
	RetryAfter time.Duration
	// Err is the underlying cause (may be nil).
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("hoster: ")
	b.WriteString(e.Kind.String())
	if e.Kind == KindQuota {
		b.WriteString(" (")
		b.WriteString(e.Scope.String())
		if e.RetryAfter > 0 {
			b.WriteString(", retry after ")
			b.WriteString(e.RetryAfter.String())
		}
		b.WriteString(")")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// NewQuota returns a KindQuota error.
func NewQuota(scope QuotaScope, retryAfter time.Duration, err error) *Error {
	return &Error{Kind: KindQuota, Scope: scope, RetryAfter: retryAfter, Err: err}
}

// NewNeedsAuth returns a KindNeedsAuth error.
func NewNeedsAuth(err error) *Error { return &Error{Kind: KindNeedsAuth, Err: err} }

// NewNotFound returns a KindNotFound error.
func NewNotFound(err error) *Error { return &Error{Kind: KindNotFound, Err: err} }

// NewExpired returns a KindExpired error.
func NewExpired(err error) *Error { return &Error{Kind: KindExpired, Err: err} }

// NewTemporary returns a KindTemporary error; retryAfter may be 0.
func NewTemporary(retryAfter time.Duration, err error) *Error {
	return &Error{Kind: KindTemporary, RetryAfter: retryAfter, Err: err}
}

// NewFatal returns a KindFatal error.
func NewFatal(err error) *Error { return &Error{Kind: KindFatal, Err: err} }

// AsError returns the *Error in err's chain, if any.
func AsError(err error) (*Error, bool) {
	var he *Error
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}

// KindOf returns the outcome kind of err: KindOK for nil, the Kind of a
// wrapped *Error, KindFatal for context cancellation (the caller stopped
// the work; do not retry) and KindTemporary for any other error.
func KindOf(err error) Kind {
	if err == nil {
		return KindOK
	}
	if he, ok := AsError(err); ok {
		return he.Kind
	}
	if errors.Is(err, context.Canceled) {
		return KindFatal
	}
	return KindTemporary
}

// IsQuota reports whether err is a quota error.
func IsQuota(err error) bool { return isKind(err, KindQuota) }

// IsNeedsAuth reports whether err is a KindNeedsAuth error.
func IsNeedsAuth(err error) bool { return isKind(err, KindNeedsAuth) }

// IsNotFound reports whether err is a KindNotFound error.
func IsNotFound(err error) bool { return isKind(err, KindNotFound) }

// IsExpired reports whether err is a KindExpired error.
func IsExpired(err error) bool { return isKind(err, KindExpired) }

// IsTemporary reports whether err should be retried with backoff (typed
// KindTemporary or any untyped non-cancellation error).
func IsTemporary(err error) bool { return KindOf(err) == KindTemporary }

// IsFatal reports whether err must not be retried.
func IsFatal(err error) bool { return KindOf(err) == KindFatal }

func isKind(err error, k Kind) bool {
	he, ok := AsError(err)
	return ok && he.Kind == k
}

// DefaultClassify is the generic HTTP classification most plugins fall back
// to after handling hoster-specific bodies:
//
//	transport error        -> Temporary (Fatal if context canceled)
//	2xx, 3xx               -> nil (OK)
//	401                    -> NeedsAuth
//	403, 410               -> Expired (signed link no longer valid)
//	404                    -> NotFound
//	408, 425, 5xx          -> Temporary (Retry-After honored)
//	429, 509               -> Quota{IP} (Retry-After honored)
//	other 4xx              -> Fatal
func DefaultClassify(resp *http.Response, _ []byte, err error) error {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return NewFatal(err)
		}
		return NewTemporary(0, err)
	}
	if resp == nil {
		return NewTemporary(0, errors.New("no response"))
	}
	code := resp.StatusCode
	status := fmt.Errorf("HTTP %d", code)
	switch {
	case code < 400:
		return nil
	case code == http.StatusUnauthorized:
		return NewNeedsAuth(status)
	case code == http.StatusForbidden, code == http.StatusGone:
		return NewExpired(status)
	case code == http.StatusNotFound:
		return NewNotFound(status)
	case code == http.StatusTooManyRequests, code == 509:
		return NewQuota(ScopeIP, ParseRetryAfter(resp.Header, time.Now()), status)
	case code == http.StatusRequestTimeout, code == http.StatusTooEarly, code >= 500:
		return NewTemporary(ParseRetryAfter(resp.Header, time.Now()), status)
	default:
		return NewFatal(status)
	}
}

// ParseRetryAfter parses a Retry-After header (seconds or HTTP date)
// relative to now. It returns 0 if absent or invalid.
func ParseRetryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
