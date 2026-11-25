package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/oauth2"
)

// fastKDF keeps tests quick; production uses DefaultKDFParams.
var fastKDF = KDFParams{Time: 1, MemoryKiB: 64, Threads: 1}

func newTestVault(t *testing.T) (*Vault, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), VaultFileName)
	return NewVault(p, fastKDF), p
}

func TestVaultRoundTrip(t *testing.T) {
	ctx := context.Background()
	v, path := newTestVault(t)
	if !v.Locked() || v.Initialized() {
		t.Fatal("new vault must be locked and uninitialized")
	}
	if err := v.Unlock(ctx, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	if err := v.Unlock(ctx, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if v.Locked() || !v.Initialized() {
		t.Fatal("should be unlocked and initialized")
	}
	sec := Secret{Password: "hunter2-SECRET", Cookies: "ndus=abc", OAuthToken: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	if err := v.Set(ctx, "a1", sec); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(ctx, "a2", Secret{APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(ctx, "a2"); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(ctx, "missing"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"hunter2-SECRET", "ndus=abc"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Fatalf("vault file leaks %q", leak)
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}

	// Reopen from disk.
	v2 := NewVault(path, fastKDF)
	if _, err := v2.Get(ctx, "a1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked Get: %v", err)
	}
	if err := v2.Unlock(ctx, "correct horse"); err != nil {
		t.Fatal(err)
	}
	got, err := v2.Get(ctx, "a1")
	if err != nil || got.Password != sec.Password || got.OAuthToken.RefreshToken != "rt" {
		t.Fatalf("got %v", err)
	}
	if _, err := v2.Get(ctx, "a2"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("deleted secret: %v", err)
	}
}

func TestVaultWrongPassword(t *testing.T) {
	ctx := context.Background()
	v, path := newTestVault(t)
	if err := v.Unlock(ctx, "the right one"); err != nil {
		t.Fatal(err)
	}
	_ = v.Set(ctx, "a", Secret{APIKey: "x"})
	v2 := NewVault(path, fastKDF)
	if err := v2.Unlock(ctx, "the wrong one"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("want ErrWrongPassword, got %v", err)
	}
	if !v2.Locked() {
		t.Fatal("must stay locked")
	}
	// Short wrong passwords on an existing vault are "wrong", not "weak".
	if err := v2.Unlock(ctx, "x"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("got %v", err)
	}
}

func TestVaultTamperDetection(t *testing.T) {
	ctx := context.Background()
	v, path := newTestVault(t)
	if err := v.Unlock(ctx, "the right one"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(ctx, "a", Secret{APIKey: "x"}); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(path)

	mutate := func(fn func(f *vaultFile)) []byte {
		var f vaultFile
		if err := json.Unmarshal(orig, &f); err != nil {
			t.Fatal(err)
		}
		fn(&f)
		b, _ := json.Marshal(&f)
		return b
	}
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"ciphertext bit flip", mutate(func(f *vaultFile) { f.Ciphertext[3] ^= 1 }), ErrVaultCorrupted},
		{"nonce changed", mutate(func(f *vaultFile) { f.Nonce[0] ^= 1 }), ErrVaultCorrupted},
		{"kdf time changed", mutate(func(f *vaultFile) { f.KDF.Time = 2 }), ErrWrongPassword},
		{"salt changed", mutate(func(f *vaultFile) { f.KDF.Salt[0] ^= 1 }), ErrWrongPassword},
		{"absurd memory", mutate(func(f *vaultFile) { f.KDF.MemoryKiB = 1 << 31 }), ErrVaultCorrupted},
		{"truncated", orig[:len(orig)/2], ErrVaultCorrupted},
		{"garbage", []byte("not json"), ErrVaultCorrupted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(path, c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			err := NewVault(path, fastKDF).Unlock(ctx, "the right one")
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestVaultLockAndChangePassword(t *testing.T) {
	ctx := context.Background()
	v, path := newTestVault(t)
	if err := v.Unlock(ctx, "first password"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(ctx, "a", Secret{APIKey: "x"}); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	for name, err := range map[string]error{
		"get":    func() error { _, e := v.Get(ctx, "a"); return e }(),
		"set":    v.Set(ctx, "a", Secret{}),
		"delete": v.Delete(ctx, "a"),
	} {
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("%s while locked: %v", name, err)
		}
	}
	if err := v.ChangePassword(ctx, "nope nope", "second password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("change with wrong old password: %v", err)
	}
	if err := v.ChangePassword(ctx, "first password", "tiny"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak new password: %v", err)
	}
	if err := v.ChangePassword(ctx, "first password", "second password"); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(ctx, "a"); err != nil || got.APIKey != "x" {
		t.Fatalf("after change: %v", err)
	}
	if err := NewVault(path, fastKDF).Unlock(ctx, "first password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("old password still works: %v", err)
	}
	if err := NewVault(path, fastKDF).Unlock(ctx, "second password"); err != nil {
		t.Fatal(err)
	}
}

func TestVaultDefaultParams(t *testing.T) {
	p := DefaultKDFParams()
	if p.Time < 1 || p.MemoryKiB < 64*1024 || p.Threads < 1 {
		t.Fatalf("weak defaults %+v", p)
	}
}
