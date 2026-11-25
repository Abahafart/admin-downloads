package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", FileName)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestMigrationsCreateSchema(t *testing.T) {
	s, _ := openTemp(t)
	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	for _, want := range []string{
		"accounts", "downloads", "egresses", "goose_db_version", "history",
		"linkgrabber_entries", "linkgrabber_files", "packages", "quota_marks",
		"segments", "settings",
	} {
		if !slices.Contains(tables, want) {
			t.Errorf("missing table %s (have %v)", want, tables)
		}
	}
}

func TestPragmas(t *testing.T) {
	s, _ := openTemp(t)
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %s", mode)
	}
	var fk, busy int
	_ = s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	_ = s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&busy)
	if fk != 1 || busy < 1000 {
		t.Fatalf("foreign_keys=%d busy_timeout=%d", fk, busy)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	s, path := openTemp(t)
	if err := s.SetSetting(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, err := s2.GetSetting(context.Background(), "k"); err != nil || v != "v" {
		t.Fatalf("got %q, %v", v, err)
	}
}

func TestConstraintsAndCascade(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := TimeToDB(time.Now())
	db := s.DB()

	res, err := db.ExecContext(ctx, `INSERT INTO packages (name, dest_dir, created_at, updated_at) VALUES ('p', '/tmp/p', ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	pkg, _ := res.LastInsertId()
	res, err = db.ExecContext(ctx, `INSERT INTO downloads (package_id, hoster, url, file_id, name, rel_path, created_at, updated_at)
		VALUES (?, 'mega', 'https://mega.nz/file/x', 'x', 'a.bin', 'S1/a.bin', ?, ?)`, pkg, now, now)
	if err != nil {
		t.Fatal(err)
	}
	dl, _ := res.LastInsertId()
	if _, err := db.ExecContext(ctx, `INSERT INTO segments (download_id, idx, start_off, end_off, done) VALUES (?, 0, 0, 100, 50)`, dl); err != nil {
		t.Fatal(err)
	}

	// Same hoster file twice in a package is rejected.
	if _, err := db.ExecContext(ctx, `INSERT INTO downloads (package_id, hoster, url, file_id, name, created_at, updated_at)
		VALUES (?, 'mega', 'u', 'x', 'dup', ?, ?)`, pkg, now, now); err == nil {
		t.Fatal("duplicate file_id accepted")
	}
	// Unknown state is rejected.
	if _, err := db.ExecContext(ctx, `UPDATE downloads SET state = 'bogus' WHERE id = ?`, dl); err == nil {
		t.Fatal("invalid state accepted")
	}
	// Segment progress cannot exceed its range.
	if _, err := db.ExecContext(ctx, `UPDATE segments SET done = 101 WHERE download_id = ?`, dl); err == nil {
		t.Fatal("segment overflow accepted")
	}
	// Foreign keys are enforced.
	if _, err := db.ExecContext(ctx, `INSERT INTO downloads (package_id, hoster, url, name, created_at, updated_at) VALUES (999, 'h', 'u', 'n', ?, ?)`, now, now); err == nil {
		t.Fatal("dangling package_id accepted")
	}

	// Deleting the package cascades to downloads and segments.
	if _, err := db.ExecContext(ctx, `DELETE FROM packages WHERE id = ?`, pkg); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM segments`).Scan(&n)
	if n != 0 {
		t.Fatalf("segments left: %d", n)
	}
}

func TestSettingsAndTx(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.GetSetting(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := s.SetSetting(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "a", "2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting(ctx, "a"); v != "2" {
		t.Fatalf("v = %q", v)
	}

	boom := errors.New("boom")
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = 'rolled-back' WHERE key = 'a'`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if v, _ := s.GetSetting(ctx, "a"); v != "2" {
		t.Fatalf("rollback failed: %q", v)
	}
	if err := s.DeleteSetting(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestMemory(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetSetting(context.Background(), "x", "y"); err != nil {
		t.Fatal(err)
	}
}

func TestTimeHelpers(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond).UTC()
	if !TimeFromDB(TimeToDB(now)).Equal(now) {
		t.Fatal("round trip")
	}
	if NullTimeToDB(time.Time{}).Valid || !NullTimeFromDB(sql.NullInt64{}).IsZero() {
		t.Fatal("null mapping")
	}
}

func TestOddPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dir #1 %20 ñ", FileName)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetSetting(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not created at the literal path: %v", err)
	}
}
