package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DownloadRow is one row of the downloads table.
type DownloadRow struct {
	ID            int64
	PackageID     int64
	Position      int
	Hoster        string
	URL           string
	FileID        string
	Name          string
	RelPath       string
	DestPath      string
	Meta          map[string]string
	Size          int64
	DoneBytes     int64
	Resumable     *bool
	ETag          string
	LastModified  string
	ChecksumAlgo  string
	ChecksumValue string
	State         string
	Error         string
	ErrorKind     string
	// QuotaScope is the hosters.QuotaScope string of the quota the download
	// waits on / failed with ("" otherwise).
	QuotaScope   string
	Attempts     int
	Priority     int
	EgressID     string
	PublicIP     string
	AccountID    string
	WaitingUntil time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
}

const downloadCols = `id, package_id, position, hoster, url, file_id, name, rel_path, dest_path, meta, size,
	done_bytes, resumable, etag, last_modified, checksum_algo, checksum_value, state, error, error_kind,
	attempts, priority, egress_id, public_ip, account_id, waiting_until, created_at, updated_at,
	started_at, completed_at, quota_scope`

func scanDownload(sc interface{ Scan(...any) error }) (DownloadRow, error) {
	var d DownloadRow
	var meta string
	var account sql.NullString
	var resumableInt sql.NullInt64
	var waiting, started, completed sql.NullInt64
	var created, updated int64
	err := sc.Scan(&d.ID, &d.PackageID, &d.Position, &d.Hoster, &d.URL, &d.FileID, &d.Name, &d.RelPath,
		&d.DestPath, &meta, &d.Size, &d.DoneBytes, &resumableInt, &d.ETag, &d.LastModified,
		&d.ChecksumAlgo, &d.ChecksumValue, &d.State, &d.Error, &d.ErrorKind, &d.Attempts, &d.Priority,
		&d.EgressID, &d.PublicIP, &account, &waiting, &created, &updated, &started, &completed, &d.QuotaScope)
	if err != nil {
		return d, err
	}
	if meta != "" && meta != "{}" {
		if err := json.Unmarshal([]byte(meta), &d.Meta); err != nil {
			return d, fmt.Errorf("download %d meta: %w", d.ID, err)
		}
	}
	if resumableInt.Valid {
		b := resumableInt.Int64 != 0
		d.Resumable = &b
	}
	d.AccountID = account.String
	d.WaitingUntil = NullTimeFromDB(waiting)
	d.CreatedAt = TimeFromDB(created)
	d.UpdatedAt = TimeFromDB(updated)
	d.StartedAt = NullTimeFromDB(started)
	d.CompletedAt = NullTimeFromDB(completed)
	return d, nil
}

func motorMeta(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func motorNullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func motorNullBool(b *bool) sql.NullInt64 {
	if b == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(motorBool(*b)), Valid: true}
}

// CreateDownloadTx inserts d inside tx (ID, CreatedAt, UpdatedAt filled in).
func (s *Store) CreateDownloadTx(ctx context.Context, tx *sql.Tx, d *DownloadRow) error {
	return createDownload(ctx, tx, d)
}

// CreateDownload inserts d.
func (s *Store) CreateDownload(ctx context.Context, d *DownloadRow) error {
	return createDownload(ctx, s.db, d)
}

func createDownload(ctx context.Context, db motorDB, d *DownloadRow) error {
	now := time.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.State == "" {
		d.State = "queued"
	}
	meta, err := motorMeta(d.Meta)
	if err != nil {
		return fmt.Errorf("store: download meta: %w", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO downloads (package_id, position, hoster, url, file_id, name, rel_path, dest_path, meta,
		   size, done_bytes, resumable, etag, last_modified, checksum_algo, checksum_value, state, error,
		   error_kind, attempts, priority, egress_id, public_ip, account_id, waiting_until, created_at,
		   updated_at, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.PackageID, d.Position, d.Hoster, d.URL, d.FileID, d.Name, d.RelPath, d.DestPath, meta,
		d.Size, d.DoneBytes, motorNullBool(d.Resumable), d.ETag, d.LastModified, d.ChecksumAlgo, d.ChecksumValue,
		d.State, d.Error, d.ErrorKind, d.Attempts, d.Priority, d.EgressID, d.PublicIP, motorNullString(d.AccountID),
		NullTimeToDB(d.WaitingUntil), TimeToDB(d.CreatedAt), TimeToDB(d.UpdatedAt),
		NullTimeToDB(d.StartedAt), NullTimeToDB(d.CompletedAt))
	if err != nil {
		return fmt.Errorf("store: create download: %w", err)
	}
	d.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: create download id: %w", err)
	}
	return nil
}

// GetDownload returns the download with that id, or ErrNotFound.
func (s *Store) GetDownload(ctx context.Context, id int64) (DownloadRow, error) {
	d, err := scanDownload(s.db.QueryRowContext(ctx, `SELECT `+downloadCols+` FROM downloads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DownloadRow{}, fmt.Errorf("download %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return DownloadRow{}, fmt.Errorf("store: get download %d: %w", id, err)
	}
	return d, nil
}

// DownloadQuery narrows ListDownloads. Zero value = everything.
type DownloadQuery struct {
	PackageID int64
	States    []string
	Hoster    string
	// IDs, if non-empty, restricts the result to these ids.
	IDs []int64
}

// ListDownloads returns downloads ordered by package and position.
func (s *Store) ListDownloads(ctx context.Context, q DownloadQuery) ([]DownloadRow, error) {
	var where []string
	var args []any
	if q.PackageID != 0 {
		where = append(where, "package_id = ?")
		args = append(args, q.PackageID)
	}
	if q.Hoster != "" {
		where = append(where, "hoster = ?")
		args = append(args, q.Hoster)
	}
	if len(q.States) > 0 {
		where = append(where, "state IN ("+motorPlaceholders(len(q.States))+")")
		for _, st := range q.States {
			args = append(args, st)
		}
	}
	if len(q.IDs) > 0 {
		where = append(where, "id IN ("+motorPlaceholders(len(q.IDs))+")")
		for _, id := range q.IDs {
			args = append(args, id)
		}
	}
	query := `SELECT ` + downloadCols + ` FROM downloads`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY package_id, position, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list downloads: %w", err)
	}
	defer rows.Close()
	var out []DownloadRow
	for rows.Next() {
		d, err := scanDownload(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan download: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func motorPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// DownloadPatch updates selected columns of a download; nil fields are left
// unchanged. Time fields set to the zero time store NULL.
type DownloadPatch struct {
	State         *string
	Error         *string
	ErrorKind     *string
	QuotaScope    *string
	Attempts      *int
	Priority      *int
	Name          *string
	DestPath      *string
	Size          *int64
	DoneBytes     *int64
	Resumable     *bool
	ETag          *string
	LastModified  *string
	ChecksumAlgo  *string
	ChecksumValue *string
	EgressID      *string
	PublicIP      *string
	AccountID     *string // "" stores NULL
	WaitingUntil  *time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

// UpdateDownload applies p to the download id. It returns ErrNotFound if the
// row does not exist.
func (s *Store) UpdateDownload(ctx context.Context, id int64, p DownloadPatch) error {
	var sets []string
	var args []any
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if p.State != nil {
		add("state", *p.State)
	}
	if p.Error != nil {
		add("error", *p.Error)
	}
	if p.ErrorKind != nil {
		add("error_kind", *p.ErrorKind)
	}
	if p.QuotaScope != nil {
		add("quota_scope", *p.QuotaScope)
	}
	if p.Attempts != nil {
		add("attempts", *p.Attempts)
	}
	if p.Priority != nil {
		add("priority", *p.Priority)
	}
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.DestPath != nil {
		add("dest_path", *p.DestPath)
	}
	if p.Size != nil {
		add("size", *p.Size)
	}
	if p.DoneBytes != nil {
		add("done_bytes", *p.DoneBytes)
	}
	if p.Resumable != nil {
		add("resumable", motorNullBool(p.Resumable))
	}
	if p.ETag != nil {
		add("etag", *p.ETag)
	}
	if p.LastModified != nil {
		add("last_modified", *p.LastModified)
	}
	if p.ChecksumAlgo != nil {
		add("checksum_algo", *p.ChecksumAlgo)
	}
	if p.ChecksumValue != nil {
		add("checksum_value", *p.ChecksumValue)
	}
	if p.EgressID != nil {
		add("egress_id", *p.EgressID)
	}
	if p.PublicIP != nil {
		add("public_ip", *p.PublicIP)
	}
	if p.AccountID != nil {
		add("account_id", motorNullString(*p.AccountID))
	}
	if p.WaitingUntil != nil {
		add("waiting_until", NullTimeToDB(*p.WaitingUntil))
	}
	if p.StartedAt != nil {
		add("started_at", NullTimeToDB(*p.StartedAt))
	}
	if p.CompletedAt != nil {
		add("completed_at", NullTimeToDB(*p.CompletedAt))
	}
	add("updated_at", TimeToDB(time.Now()))
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: update download %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("download %d: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateDownloadsState sets state (and clears error/waiting_until when
// clearError) for every id whose current state is in from (any state if
// from is empty). It returns the number of rows changed.
func (s *Store) UpdateDownloadsState(ctx context.Context, ids []int64, from []string, to string, clearError bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q := `UPDATE downloads SET state = ?, updated_at = ?`
	args := []any{to, TimeToDB(time.Now())}
	if clearError {
		q += `, error = '', error_kind = '', quota_scope = '', waiting_until = NULL`
	}
	q += ` WHERE id IN (` + motorPlaceholders(len(ids)) + `)`
	for _, id := range ids {
		args = append(args, id)
	}
	if len(from) > 0 {
		q += ` AND state IN (` + motorPlaceholders(len(from)) + `)`
		for _, st := range from {
			args = append(args, st)
		}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("store: update downloads state: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ResetDownloadProgress forgets everything learnt about the remote file and
// the bytes on disk: segments are deleted, done_bytes = 0, etag /
// last_modified cleared, resumable = NULL.
func (s *Store) ResetDownloadProgress(ctx context.Context, id int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE download_id = ?`, id); err != nil {
			return fmt.Errorf("store: reset segments %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE downloads SET done_bytes = 0, etag = '', last_modified = '', resumable = NULL, updated_at = ?
			 WHERE id = ?`, TimeToDB(time.Now()), id); err != nil {
			return fmt.Errorf("store: reset download %d: %w", id, err)
		}
		return nil
	})
}

// DeleteDownload removes a download (segments cascade). Missing is not an
// error.
func (s *Store) DeleteDownload(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete download %d: %w", id, err)
	}
	return nil
}

// CountDownloads returns the number of downloads in a package.
func (s *Store) CountDownloads(ctx context.Context, packageID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE package_id = ?`, packageID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count downloads: %w", err)
	}
	return n, nil
}

// MaxDownloadPosition returns the highest position used in a package (-1 if
// empty).
func (s *Store) MaxDownloadPosition(ctx context.Context, packageID int64) (int, error) {
	var n sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(position) FROM downloads WHERE package_id = ?`, packageID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: max position: %w", err)
	}
	if !n.Valid {
		return -1, nil
	}
	return int(n.Int64), nil
}

// DownloadRef identifies a file already known to the queue or history.
type DownloadRef struct {
	Hoster string
	FileID string
	URL    string
}

// KnownLinks reports which of refs are already in the downloads queue or in
// the history (completed/failed/deleted). A ref matches by (hoster,
// file_id) when FileID is set, and by url otherwise or additionally. The
// result is indexed like refs.
func (s *Store) KnownLinks(ctx context.Context, refs []DownloadRef) ([]bool, error) {
	out := make([]bool, len(refs))
	for i, r := range refs {
		var n int
		var err error
		if r.FileID != "" {
			err = s.db.QueryRowContext(ctx,
				`SELECT (SELECT COUNT(*) FROM downloads WHERE hoster = ? AND file_id = ?) +
				        (SELECT COUNT(*) FROM history WHERE hoster = ? AND file_id = ? AND outcome = 'completed')`,
				r.Hoster, r.FileID, r.Hoster, r.FileID).Scan(&n)
		}
		if err == nil && n == 0 && r.URL != "" {
			err = s.db.QueryRowContext(ctx,
				`SELECT (SELECT COUNT(*) FROM downloads WHERE url = ?) +
				        (SELECT COUNT(*) FROM history WHERE url = ? AND outcome = 'completed')`,
				r.URL, r.URL).Scan(&n)
		}
		if err != nil {
			return nil, fmt.Errorf("store: known links: %w", err)
		}
		out[i] = n > 0
	}
	return out, nil
}

// CountDownloadsByState returns the number of downloads per state.
func (s *Store) CountDownloadsByState(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM downloads GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("store: count downloads by state: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("store: scan state count: %w", err)
		}
		out[st] = n
	}
	return out, rows.Err()
}
