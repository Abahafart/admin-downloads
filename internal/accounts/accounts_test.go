package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretRedaction(t *testing.T) {
	s := Secret{Password: "hunter2", APIKey: "k3y", Extra: map[string]string{"a": "b"}}
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s)} {
		if strings.Contains(out, "hunter2") || strings.Contains(out, "k3y") {
			t.Fatalf("secret leaked by fmt: %q", out)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "secret", s)
	if strings.Contains(buf.String(), "hunter2") {
		t.Fatalf("secret leaked by slog: %s", buf.String())
	}
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	if _, err := st.Get(ctx, "a"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("err = %v", err)
	}
	in := Secret{Cookies: "ndus=1", Extra: map[string]string{"k": "v"}}
	if err := st.Set(ctx, "a", in); err != nil {
		t.Fatal(err)
	}
	in.Extra["k"] = "mutated"
	got, err := st.Get(ctx, "a")
	if err != nil || got.Cookies != "ndus=1" || got.Extra["k"] != "v" {
		t.Fatalf("got %#v, %v (aliasing?)", got.Extra, err)
	}
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatal("deleting a missing secret must not fail")
	}
	if _, err := st.Get(ctx, "a"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuotaLeft(t *testing.T) {
	a := Account{QuotaTotal: -1, QuotaUsed: 0}
	if a.QuotaLeft() != -1 {
		t.Fatal("unknown quota")
	}
	a = Account{QuotaTotal: 10, QuotaUsed: 4}
	if a.QuotaLeft() != 6 {
		t.Fatal("quota left")
	}
	a.QuotaUsed = 20
	if a.QuotaLeft() != 0 {
		t.Fatal("over quota")
	}
}
