package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/store"
)

// TestManagerWithSQLite runs the manager over the real store (accounts_repo.go).
func TestManagerWithSQLite(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sec := accounts.NewVault(filepath.Join(t.TempDir(), accounts.VaultFileName), accounts.KDFParams{Time: 1, MemoryKiB: 64, Threads: 1})
	if err := sec.Unlock(ctx, "integration pw"); err != nil {
		t.Fatal(err)
	}
	m, err := accounts.NewManager(ctx, accounts.ManagerOptions{Repo: st, Secrets: sec})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create(ctx, accounts.NewAccount{Hoster: "mega", Kind: accounts.KindPassword, Username: "me@example.com", Secret: accounts.Secret{Password: "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, accounts.NewAccount{Hoster: "mega", Kind: accounts.KindPassword, Username: "two@example.com", Secret: accounts.Secret{Password: "pw2"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(ctx, a.ID)
	if err != nil || got.Username != "me@example.com" || got.Kind != accounts.KindPassword || !got.Enabled || got.QuotaTotal != -1 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if !got.CreatedAt.Equal(a.CreatedAt.Truncate(time.Millisecond)) {
		t.Fatalf("created_at %v vs %v", got.CreatedAt, a.CreatedAt)
	}

	if err := m.ReportQuota(ctx, a.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	// A new manager on the same DB sees the persisted quota mark.
	m2, err := accounts.NewManager(ctx, accounts.ManagerOptions{Repo: st, Secrets: sec})
	if err != nil {
		t.Fatal(err)
	}
	p, err := m2.Pick(ctx, "mega")
	if err != nil || p.ID != b.ID {
		t.Fatalf("pick: %v %v", p, err)
	}
	if u := m2.QuotaBlockedUntil(a.ID); u.IsZero() {
		t.Fatal("quota mark not reloaded")
	}

	lbl := "Principal"
	if _, err := m.Update(ctx, a.ID, accounts.AccountPatch{Label: &lbl}); err != nil {
		t.Fatal(err)
	}
	list, _ := m.List(ctx, "mega")
	if len(list) != 2 {
		t.Fatalf("list %+v", list)
	}
	if got, _ := m.Get(ctx, a.ID); got.Label != "Principal" {
		t.Fatalf("label %q", got.Label)
	}
	if err := m.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAccount(ctx, a.ID); !errors.Is(err, accounts.ErrAccountNotFound) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	marks, _ := st.AccountQuotaMarks(ctx, time.Now())
	if len(marks) != 0 {
		t.Fatalf("quota mark survived delete: %v", marks)
	}
}
