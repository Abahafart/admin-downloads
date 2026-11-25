package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// limitedKeyring is an in-memory keyring that rejects values over limit
// bytes, like Windows Credential Manager.
type limitedKeyring struct {
	mu    sync.Mutex
	limit int
	m     map[string]string
}

func newLimitedKeyring(limit int) *limitedKeyring {
	return &limitedKeyring{limit: limit, m: map[string]string{}}
}

func (l *limitedKeyring) Set(s, u, p string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(p) > l.limit {
		return keyring.ErrSetDataTooBig
	}
	l.m[s+"|"+u] = p
	return nil
}

func (l *limitedKeyring) Get(s, u string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.m[s+"|"+u]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (l *limitedKeyring) Delete(s, u string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.m[s+"|"+u]; !ok {
		return keyring.ErrNotFound
	}
	delete(l.m, s+"|"+u)
	return nil
}

func (l *limitedKeyring) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

func TestKeyringStoreMock(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	k := NewKeyringStore()
	if _, err := k.Get(ctx, "acc1"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	in := Secret{Password: "hunter2", OAuthToken: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}, Extra: map[string]string{"k": "v"}}
	if err := k.Set(ctx, "acc1", in); err != nil {
		t.Fatal(err)
	}
	raw, err := keyring.Get(KeyringService, "acc1")
	if err != nil || !strings.HasPrefix(raw, "{") {
		t.Fatalf("raw entry %q, %v: want JSON under service %q", raw, err, KeyringService)
	}
	got, err := k.Get(ctx, "acc1")
	if err != nil || got.Password != "hunter2" || got.OAuthToken.RefreshToken != "rt" || got.Extra["k"] != "v" {
		t.Fatalf("Get = %+v, %v", got.OAuthToken, err)
	}
	if err := k.Delete(ctx, "acc1"); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete(ctx, "acc1"); err != nil {
		t.Fatalf("deleting a missing secret: %v", err)
	}
	if _, err := k.Get(ctx, "acc1"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestKeyringStoreChunking(t *testing.T) {
	ctx := context.Background()
	kr := newLimitedKeyring(2560)
	k := &KeyringStore{service: KeyringService, kr: kr}

	// ~20 KB of cookies with multi-byte characters.
	big := strings.Repeat("ndus=ñandú-ä€-0123456789abcdef; ", 700)
	if err := k.Set(ctx, "acc", Secret{Cookies: big}); err != nil {
		t.Fatal(err)
	}
	if n := kr.len(); n < 10 {
		t.Fatalf("expected many chunk entries, got %d", n)
	}
	for key, v := range kr.m {
		if len(v) > keyringChunkSize {
			t.Fatalf("entry %s has %d bytes", key, len(v))
		}
	}
	got, err := k.Get(ctx, "acc")
	if err != nil || got.Cookies != big {
		t.Fatalf("chunked round trip failed: %v (len %d vs %d)", err, len(got.Cookies), len(big))
	}

	// Shrinking back to a small value removes the stale chunks.
	if err := k.Set(ctx, "acc", Secret{APIKey: "small"}); err != nil {
		t.Fatal(err)
	}
	if n := kr.len(); n != 1 {
		t.Fatalf("stale chunks left: %d entries", n)
	}
	if got, _ := k.Get(ctx, "acc"); got.APIKey != "small" {
		t.Fatalf("got %q", got.APIKey)
	}

	// Corrupted chunk: checksum mismatch is reported, not garbage.
	if err := k.Set(ctx, "acc", Secret{Cookies: big}); err != nil {
		t.Fatal(err)
	}
	kr.m[KeyringService+"|"+chunkKey("acc", 1)] = "xx"
	if _, err := k.Get(ctx, "acc"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}

	if err := k.Delete(ctx, "acc"); err != nil {
		t.Fatal(err)
	}
	if n := kr.len(); n != 0 {
		t.Fatalf("delete left %d entries", n)
	}
}

func TestKeyringStoreTooBigFallsBackToChunks(t *testing.T) {
	// A keyring with a limit below our chunk size (e.g. a stricter macOS
	// build) still works via smaller chunks.
	ctx := context.Background()
	kr := newLimitedKeyring(1500)
	k := &KeyringStore{service: KeyringService, kr: kr}
	val := strings.Repeat("a", 1800)
	if err := k.Set(ctx, "acc", Secret{APIKey: val}); err != nil {
		t.Fatal(err)
	}
	got, err := k.Get(ctx, "acc")
	if err != nil || got.APIKey != val {
		t.Fatalf("got %v", err)
	}
}

func TestKeyringRejectsChunkIDs(t *testing.T) {
	k := &KeyringStore{service: KeyringService, kr: newLimitedKeyring(100)}
	if err := k.Set(context.Background(), "x.chunk.1", Secret{APIKey: "a"}); err == nil {
		t.Fatal("ids colliding with chunk keys must be rejected")
	}
}

func TestSplitUTF8(t *testing.T) {
	s := strings.Repeat("€", 10) // 3 bytes each
	parts := splitUTF8(s, 4)
	if strings.Join(parts, "") != s {
		t.Fatal("split lost data")
	}
	for _, p := range parts {
		if len(p) > 4 || !strings.HasPrefix(p, "€") {
			t.Fatalf("bad part %q", p)
		}
	}
}

func TestProbeKeyring(t *testing.T) {
	if err := probeKeyring(context.Background(), newLimitedKeyring(100)); err != nil {
		t.Fatalf("probe on a working keyring: %v", err)
	}
	keyring.MockInitWithError(errors.New("no dbus"))
	defer keyring.MockInit()
	if err := ProbeKeyring(context.Background()); err == nil {
		t.Fatal("probe should fail")
	}
	// A hanging keyring (unlock prompt) is abandoned at the deadline.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	block := make(chan struct{})
	defer close(block)
	if err := probeKeyring(ctx, hangingKeyring{block}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
}

type hangingKeyring struct{ c chan struct{} }

func (h hangingKeyring) Set(string, string, string) error   { <-h.c; return nil }
func (h hangingKeyring) Get(string, string) (string, error) { <-h.c; return "", nil }
func (h hangingKeyring) Delete(string, string) error        { <-h.c; return nil }
