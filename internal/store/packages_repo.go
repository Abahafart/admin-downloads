package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PackageRow is one row of the packages table.
type PackageRow struct {
	ID           int64
	Name         string
	DestDir      string
	SourceURL    string
	Hoster       string
	IsFolder     bool
	Priority     int
	ExistsPolicy string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const packageCols = `id, name, dest_dir, source_url, hoster, is_folder, priority, exists_policy, created_at, updated_at`

func scanPackage(sc interface{ Scan(...any) error }) (PackageRow, error) {
	var p PackageRow
	var isFolder int
	var created, updated int64
	err := sc.Scan(&p.ID, &p.Name, &p.DestDir, &p.SourceURL, &p.Hoster, &isFolder, &p.Priority, &p.ExistsPolicy, &created, &updated)
	p.IsFolder = isFolder != 0
	p.CreatedAt = TimeFromDB(created)
	p.UpdatedAt = TimeFromDB(updated)
	return p, err
}

// CreatePackage inserts p (ID, CreatedAt and UpdatedAt are filled in).
func (s *Store) CreatePackage(ctx context.Context, p *PackageRow) error {
	return createPackage(ctx, s.db, p)
}

// CreatePackageTx is CreatePackage inside a transaction.
func (s *Store) CreatePackageTx(ctx context.Context, tx *sql.Tx, p *PackageRow) error {
	return createPackage(ctx, tx, p)
}

type motorDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func createPackage(ctx context.Context, db motorDB, p *PackageRow) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.ExistsPolicy == "" {
		p.ExistsPolicy = "skip"
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO packages (name, dest_dir, source_url, hoster, is_folder, priority, exists_policy, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.DestDir, p.SourceURL, p.Hoster, motorBool(p.IsFolder), p.Priority, p.ExistsPolicy,
		TimeToDB(p.CreatedAt), TimeToDB(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: create package: %w", err)
	}
	p.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: create package id: %w", err)
	}
	return nil
}

// GetPackage returns the package with that id, or ErrNotFound.
func (s *Store) GetPackage(ctx context.Context, id int64) (PackageRow, error) {
	p, err := scanPackage(s.db.QueryRowContext(ctx, `SELECT `+packageCols+` FROM packages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PackageRow{}, fmt.Errorf("package %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return PackageRow{}, fmt.Errorf("store: get package %d: %w", id, err)
	}
	return p, nil
}

// ListPackages returns every package ordered by priority (desc) and id.
func (s *Store) ListPackages(ctx context.Context) ([]PackageRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+packageCols+` FROM packages ORDER BY priority DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list packages: %w", err)
	}
	defer rows.Close()
	var out []PackageRow
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan package: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePackage removes a package and (by cascade) its downloads and
// segments. Deleting a missing package is not an error.
func (s *Store) DeletePackage(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM packages WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete package %d: %w", id, err)
	}
	return nil
}

// TouchPackage updates updated_at.
func (s *Store) TouchPackage(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE packages SET updated_at = ? WHERE id = ?`, TimeToDB(time.Now()), id); err != nil {
		return fmt.Errorf("store: touch package %d: %w", id, err)
	}
	return nil
}

func motorBool(b bool) int {
	if b {
		return 1
	}
	return 0
}
