package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Egress row sources (egresses.source).
const (
	EgressSourceConfig = "config" // defined in config.toml (rebuilt at start-up)
	EgressSourceImport = "import" // imported through the API (proxy list, WireGuard .conf)
	EgressSourceAuto   = "auto"   // created automatically (Tor circuits)
)

// EgressRow is one row of the egresses table. It never holds secrets:
// SecretRef points into the SecretStore.
type EgressRow struct {
	ID          string
	Kind        string // netx.Kind
	Label       string
	Source      string // EgressSource*
	Address     string // proxy URL without credentials, conf name...
	Settings    string // kind-specific non-secret JSON ("{}" if empty)
	SecretRef   string
	Enabled     bool
	Healthy     bool
	PublicIP    string
	LastError   string
	LastCheckAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const egressCols = `id, kind, label, source, address, settings, secret_ref, enabled, healthy,
	public_ip, last_error, last_check_at, created_at, updated_at`

// UpsertEgress creates the row or updates its definition (kind, label,
// source, address, settings, secret_ref, enabled). Health columns and
// created_at of an existing row are kept.
func (s *Store) UpsertEgress(ctx context.Context, r EgressRow) error {
	now := time.Now()
	if r.Settings == "" {
		r.Settings = "{}"
	}
	if r.Source == "" {
		r.Source = EgressSourceConfig
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO egresses (`+egressCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   kind = excluded.kind, label = excluded.label, source = excluded.source,
		   address = excluded.address, settings = excluded.settings,
		   secret_ref = excluded.secret_ref, enabled = excluded.enabled,
		   updated_at = excluded.updated_at`,
		r.ID, r.Kind, r.Label, r.Source, r.Address, r.Settings, r.SecretRef,
		egressBoolToDB(r.Enabled), egressBoolToDB(r.Healthy), r.PublicIP, r.LastError,
		NullTimeToDB(r.LastCheckAt), TimeToDB(created), TimeToDB(now))
	if err != nil {
		return fmt.Errorf("store: upsert egress %q: %w", r.ID, err)
	}
	return nil
}

// GetEgress returns the row with that id, or an error wrapping ErrNotFound.
func (s *Store) GetEgress(ctx context.Context, id string) (EgressRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egresses WHERE id = ?`, id)
	r, err := scanEgress(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EgressRow{}, fmt.Errorf("egress %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return EgressRow{}, fmt.Errorf("store: get egress %q: %w", id, err)
	}
	return r, nil
}

// ListEgresses returns every row, optionally only those of the given
// sources, ordered by creation time then id.
func (s *Store) ListEgresses(ctx context.Context, sources ...string) ([]EgressRow, error) {
	q := `SELECT ` + egressCols + ` FROM egresses`
	args := make([]any, 0, len(sources))
	if len(sources) > 0 {
		q += ` WHERE source IN (?` + strings.Repeat(", ?", len(sources)-1) + `)`
		for _, src := range sources {
			args = append(args, src)
		}
	}
	q += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list egresses: %w", err)
	}
	defer rows.Close()
	var out []EgressRow
	for rows.Next() {
		r, err := scanEgress(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan egress: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list egresses: %w", err)
	}
	return out, nil
}

// DeleteEgress removes the row; deleting a missing row is not an error.
func (s *Store) DeleteEgress(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM egresses WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete egress %q: %w", id, err)
	}
	return nil
}

// DeleteEgressesNotIn removes the rows of source whose id is not in keep
// (used at start-up to forget config egresses removed from config.toml).
// It returns the number of rows removed.
func (s *Store) DeleteEgressesNotIn(ctx context.Context, source string, keep []string) (int64, error) {
	q := `DELETE FROM egresses WHERE source = ?`
	args := []any{source}
	if len(keep) > 0 {
		q += ` AND id NOT IN (?` + strings.Repeat(", ?", len(keep)-1) + `)`
		for _, id := range keep {
			args = append(args, id)
		}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("store: prune egresses of %s: %w", source, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SetEgressEnabled enables or disables an egress row.
func (s *Store) SetEgressEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE egresses SET enabled = ?, updated_at = ? WHERE id = ?`,
		egressBoolToDB(enabled), TimeToDB(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: enable egress %q: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("egress %q: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateEgressHealth stores the result of a health check. A missing row is
// not an error (direct and command egresses have no row).
func (s *Store) UpdateEgressHealth(ctx context.Context, id string, healthy bool, publicIP, lastError string, checkedAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE egresses SET healthy = ?, public_ip = ?, last_error = ?, last_check_at = ?, updated_at = ?
		 WHERE id = ?`,
		egressBoolToDB(healthy), publicIP, lastError, NullTimeToDB(checkedAt), TimeToDB(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: update egress health %q: %w", id, err)
	}
	return nil
}

type egressRowScanner interface {
	Scan(dest ...any) error
}

func scanEgress(sc egressRowScanner) (EgressRow, error) {
	var r EgressRow
	var enabled, healthy, created, updated int64
	var checked sql.NullInt64
	err := sc.Scan(&r.ID, &r.Kind, &r.Label, &r.Source, &r.Address, &r.Settings, &r.SecretRef,
		&enabled, &healthy, &r.PublicIP, &r.LastError, &checked, &created, &updated)
	if err != nil {
		return EgressRow{}, err
	}
	r.Enabled = enabled != 0
	r.Healthy = healthy != 0
	r.LastCheckAt = NullTimeFromDB(checked)
	r.CreatedAt = TimeFromDB(created)
	r.UpdatedAt = TimeFromDB(updated)
	return r, nil
}

func egressBoolToDB(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
