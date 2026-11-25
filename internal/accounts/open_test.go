package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func okProbe(context.Context) error   { return nil }
func failProbe(context.Context) error { return errors.New("no secret service") }

func TestOpenSecretStoreAuto(t *testing.T) {
	ctx := context.Background()

	t.Run("keyring available", func(t *testing.T) {
		dir := t.TempDir()
		st, err := OpenSecretStore(ctx, StoreOptions{probe: okProbe, keyring: newLimitedKeyring(2560)}, dir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status.Backend != BackendKeyring {
			t.Fatalf("backend %q", st.Status.Backend)
		}
		if _, ok := st.SecretStore.(*KeyringStore); !ok {
			t.Fatalf("type %T", st.SecretStore)
		}
		if readMarker(filepath.Join(dir, BackendFileName)) != BackendKeyring {
			t.Fatal("choice not recorded")
		}
		if _, ok := AsLockable(st); ok {
			t.Fatal("keyring is not lockable")
		}
	})

	t.Run("headless falls back to locked vault", func(t *testing.T) {
		dir := t.TempDir()
		st, err := OpenSecretStore(ctx, StoreOptions{probe: failProbe, VaultKDF: fastKDF}, dir)
		if err != nil {
			t.Fatal(err)
		}
		s := StatusOf(st)
		if s.Backend != BackendVault || !s.Locked || s.Initialized || !s.Lockable || s.Warning != "" {
			t.Fatalf("status %+v", s)
		}
		if _, err := st.Get(ctx, "x"); !errors.Is(err, ErrLocked) {
			t.Fatalf("locked vault Get: %v", err)
		}
		l, _ := AsLockable(st)
		if err := l.Unlock(ctx, "master password"); err != nil {
			t.Fatal(err)
		}
		if s := StatusOf(st); s.Locked || !s.Initialized {
			t.Fatalf("status after unlock %+v", s)
		}
	})

	t.Run("existing vault wins over keyring", func(t *testing.T) {
		dir := t.TempDir()
		v := NewVault(filepath.Join(dir, VaultFileName), fastKDF)
		if err := v.Unlock(ctx, "master password"); err != nil {
			t.Fatal(err)
		}
		st, err := OpenSecretStore(ctx, StoreOptions{probe: okProbe}, dir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status.Backend != BackendVault {
			t.Fatalf("backend %q", st.Status.Backend)
		}
	})

	t.Run("keyring gone warns", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, BackendFileName), []byte("keyring\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := OpenSecretStore(ctx, StoreOptions{probe: failProbe}, dir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status.Backend != BackendVault || st.Status.Warning == "" || StatusOf(st).Warning == "" {
			t.Fatalf("status %+v", st.Status)
		}
	})
}

func TestOpenSecretStoreOverrides(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := OpenSecretStore(ctx, StoreOptions{Backend: "keyring", probe: failProbe}, dir); err == nil {
		t.Fatal("forced keyring without keyring must fail")
	}
	if _, err := OpenSecretStore(ctx, StoreOptions{Backend: "bogus"}, dir); err == nil {
		t.Fatal("unknown backend must fail")
	}
	st, err := OpenSecretStore(ctx, StoreOptions{Backend: "memory"}, dir)
	if err != nil || StatusOf(st).Backend != BackendMemory {
		t.Fatalf("memory: %v", err)
	}
	st, err = OpenSecretStore(ctx, StoreOptions{Backend: "VAULT", probe: okProbe, VaultKDF: fastKDF}, dir)
	if err != nil || StatusOf(st).Backend != BackendVault {
		t.Fatalf("vault: %v", err)
	}

	// Password file unlocks at start-up (creating the vault).
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = OpenSecretStore(ctx, StoreOptions{Backend: "vault", VaultPasswordFile: pw, VaultKDF: fastKDF}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if StatusOf(st).Locked {
		t.Fatal("password file should unlock")
	}
	if err := os.WriteFile(pw, []byte("wrong password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSecretStore(ctx, StoreOptions{Backend: "vault", VaultPasswordFile: pw}, dir); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password file: %v", err)
	}
}
