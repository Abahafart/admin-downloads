package hosters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/netx"
)

type fakeHoster struct {
	name     string
	host     string
	fallback bool
}

func (f fakeHoster) Name() string { return f.name }
func (f fakeHoster) Match(u *url.URL) bool {
	return f.host == "*" || strings.EqualFold(u.Hostname(), f.host)
}
func (f fakeHoster) Resolve(context.Context, *Session, string) (*ResolveResult, error) {
	return nil, nil
}
func (f fakeHoster) Prepare(context.Context, *Session, FileInfo) (*DownloadSpec, error) {
	return nil, nil
}
func (f fakeHoster) Classify(r *http.Response, b []byte, err error) error {
	return DefaultClassify(r, b, err)
}
func (f fakeHoster) Caps() Caps { return Caps{Fallback: f.fallback} }

func TestRegistryMatchOrder(t *testing.T) {
	r := NewRegistry()
	// Register the fallback first: it must still lose against specific ones.
	if err := r.Register(fakeHoster{name: "generic", host: "*", fallback: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeHoster{name: "mega", host: "mega.nz"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeHoster{name: "mega", host: "x"}); err == nil {
		t.Fatal("duplicate must fail")
	}
	if h := r.MatchString("https://MEGA.nz/file/abc#key"); h == nil || h.Name() != "mega" {
		t.Fatalf("got %v", h)
	}
	if h := r.MatchString("https://example.com/a.zip"); h == nil || h.Name() != "generic" {
		t.Fatalf("got %v", h)
	}
	if h := r.MatchString("not a url"); h != nil {
		t.Fatalf("got %v", h)
	}
	if got := r.All(); len(got) != 2 || got[0].Name() != "mega" {
		t.Fatalf("All = %v", got)
	}
	if r.Get("mega") == nil || r.Get("nope") != nil {
		t.Fatal("Get")
	}
}

func TestErrorHelpers(t *testing.T) {
	q := fmt.Errorf("wrapped: %w", NewQuota(ScopeIP, time.Minute, errors.New("509")))
	if !IsQuota(q) || IsTemporary(q) || KindOf(q) != KindQuota {
		t.Fatal("quota classification")
	}
	he, ok := AsError(q)
	if !ok || he.Scope != ScopeIP || he.RetryAfter != time.Minute {
		t.Fatalf("AsError = %+v", he)
	}
	if !strings.Contains(q.Error(), "quota (ip, retry after 1m0s)") {
		t.Fatalf("message %q", q.Error())
	}
	if KindOf(nil) != KindOK || IsQuota(nil) {
		t.Fatal("nil")
	}
	if !IsTemporary(errors.New("conn reset")) {
		t.Fatal("untyped errors are temporary")
	}
	if !IsFatal(context.Canceled) {
		t.Fatal("cancellation is fatal")
	}
	for _, c := range []struct {
		err  error
		kind Kind
	}{
		{NewNeedsAuth(nil), KindNeedsAuth},
		{NewNotFound(nil), KindNotFound},
		{NewExpired(nil), KindExpired},
		{NewTemporary(0, nil), KindTemporary},
		{NewFatal(nil), KindFatal},
	} {
		if KindOf(c.err) != c.kind {
			t.Errorf("%v: kind %v", c.err, KindOf(c.err))
		}
	}
	if s, err := ParseQuotaScope("file"); err != nil || s != ScopeFile {
		t.Fatal("ParseQuotaScope")
	}
	if _, err := ParseQuotaScope(""); err == nil {
		t.Fatal("empty scope must fail")
	}
}

func TestDefaultClassify(t *testing.T) {
	resp := func(code int, h ...string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		if len(h) == 2 {
			r.Header.Set(h[0], h[1])
		}
		return r
	}
	cases := []struct {
		r    *http.Response
		err  error
		kind Kind
	}{
		{resp(200), nil, KindOK},
		{resp(206), nil, KindOK},
		{resp(401), nil, KindNeedsAuth},
		{resp(403), nil, KindExpired},
		{resp(404), nil, KindNotFound},
		{resp(410), nil, KindExpired},
		{resp(429), nil, KindQuota},
		{resp(509), nil, KindQuota},
		{resp(503), nil, KindTemporary},
		{resp(400), nil, KindFatal},
		{nil, errors.New("dial tcp: refused"), KindTemporary},
		{nil, context.Canceled, KindFatal},
	}
	for _, c := range cases {
		if got := KindOf(DefaultClassify(c.r, nil, c.err)); got != c.kind {
			code := 0
			if c.r != nil {
				code = c.r.StatusCode
			}
			t.Errorf("code %d err %v: kind %v, want %v", code, c.err, got, c.kind)
		}
	}
	he, _ := AsError(DefaultClassify(resp(429, "Retry-After", "120"), nil, nil))
	if he.RetryAfter != 2*time.Minute || he.Scope != ScopeIP {
		t.Fatalf("retry-after = %v scope %v", he.RetryAfter, he.Scope)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat))
	if d := ParseRetryAfter(h, now); d != 90*time.Second {
		t.Fatalf("date form: %v", d)
	}
	h.Set("Retry-After", "garbage")
	if d := ParseRetryAfter(h, now); d != 0 {
		t.Fatalf("garbage: %v", d)
	}
}

func TestSession(t *testing.T) {
	ctx := context.Background()
	anon := NewSession(netx.NewDirect(), nil, nil)
	if anon.Jar == nil || anon.Client == nil || anon.Client.Jar != anon.Jar || !anon.Anonymous() {
		t.Fatal("anonymous session not wired")
	}
	if _, err := anon.Secret(ctx); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("err = %v", err)
	}

	acct := &accounts.Account{ID: "a1", Hoster: "mega", Kind: accounts.KindPassword}
	s := NewSession(netx.NewDirect(), acct, nil)
	s.Secrets = accounts.NewMemoryStore()
	if err := s.SetSecret(ctx, accounts.Secret{Session: "sid"}); err != nil {
		t.Fatal(err)
	}
	sec, err := s.Secret(ctx)
	if err != nil || sec.Session != "sid" {
		t.Fatalf("secret = %v, %v", sec.Session, err)
	}
	s.SetValue("mega.sid", "x")
	if v, ok := s.Value("mega.sid"); !ok || v != "x" {
		t.Fatal("Value")
	}
	s.DeleteValue("mega.sid")
	if _, ok := s.Value("mega.sid"); ok {
		t.Fatal("DeleteValue")
	}
}
