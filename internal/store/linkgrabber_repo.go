package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GrabberEntryRow is one row of linkgrabber_entries plus its files.
type GrabberEntryRow struct {
	ID          int64
	URL         string
	Hoster      string
	State       string
	Error       string
	PackageName string
	IsFolder    bool
	Source      string
	AutoConfirm bool
	DestDir     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Files       []GrabberFileRow
}

// GrabberFileRow is one row of linkgrabber_files.
type GrabberFileRow struct {
	Idx           int
	FileID        string
	Name          string
	RelPath       string
	Size          int64
	ChecksumAlgo  string
	ChecksumValue string
	Link          string
	Meta          map[string]string
}

// CreateGrabberEntry inserts e unless an entry with the same URL exists. It
// reports whether a row was created (ID/CreatedAt filled in).
func (s *Store) CreateGrabberEntry(ctx context.Context, e *GrabberEntryRow) (bool, error) {
	now := time.Now()
	e.CreatedAt, e.UpdatedAt = now, now
	if e.State == "" {
		e.State = "pending"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO linkgrabber_entries (url, hoster, state, error, package_name, is_folder, source,
		   auto_confirm, dest_dir, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (url) DO NOTHING`,
		e.URL, e.Hoster, e.State, e.Error, e.PackageName, motorBool(e.IsFolder), e.Source,
		motorBool(e.AutoConfirm), e.DestDir, TimeToDB(now), TimeToDB(now))
	if err != nil {
		return false, fmt.Errorf("store: create grabber entry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	e.ID, err = res.LastInsertId()
	if err != nil {
		return false, fmt.Errorf("store: grabber entry id: %w", err)
	}
	return true, nil
}

// GrabberEntryPatch updates selected columns of an entry.
type GrabberEntryPatch struct {
	State       *string
	Error       *string
	PackageName *string
	IsFolder    *bool
	Hoster      *string
}

// UpdateGrabberEntry applies p.
func (s *Store) UpdateGrabberEntry(ctx context.Context, id int64, p GrabberEntryPatch) error {
	q := `UPDATE linkgrabber_entries SET updated_at = ?`
	args := []any{TimeToDB(time.Now())}
	if p.State != nil {
		q += `, state = ?`
		args = append(args, *p.State)
	}
	if p.Error != nil {
		q += `, error = ?`
		args = append(args, *p.Error)
	}
	if p.PackageName != nil {
		q += `, package_name = ?`
		args = append(args, *p.PackageName)
	}
	if p.IsFolder != nil {
		q += `, is_folder = ?`
		args = append(args, motorBool(*p.IsFolder))
	}
	if p.Hoster != nil {
		q += `, hoster = ?`
		args = append(args, *p.Hoster)
	}
	q += ` WHERE id = ?`
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("store: update grabber entry %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("grabber entry %d: %w", id, ErrNotFound)
	}
	return nil
}

// ReplaceGrabberFiles replaces every file of an entry.
func (s *Store) ReplaceGrabberFiles(ctx context.Context, entryID int64, files []GrabberFileRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM linkgrabber_files WHERE entry_id = ?`, entryID); err != nil {
			return fmt.Errorf("store: clear grabber files %d: %w", entryID, err)
		}
		return insertGrabberFiles(ctx, tx, entryID, files)
	})
}

// AddGrabberFiles appends files (their Idx must be unique within the entry).
func (s *Store) AddGrabberFiles(ctx context.Context, entryID int64, files []GrabberFileRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error { return insertGrabberFiles(ctx, tx, entryID, files) })
}

func insertGrabberFiles(ctx context.Context, tx *sql.Tx, entryID int64, files []GrabberFileRow) error {
	for _, f := range files {
		meta, err := motorMeta(f.Meta)
		if err != nil {
			return fmt.Errorf("store: grabber file meta: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO linkgrabber_files (entry_id, idx, file_id, name, rel_path, size, checksum_algo,
			   checksum_value, link, meta)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (entry_id, idx) DO UPDATE SET file_id = excluded.file_id, name = excluded.name,
			   rel_path = excluded.rel_path, size = excluded.size, checksum_algo = excluded.checksum_algo,
			   checksum_value = excluded.checksum_value, link = excluded.link, meta = excluded.meta`,
			entryID, f.Idx, f.FileID, f.Name, f.RelPath, f.Size, f.ChecksumAlgo, f.ChecksumValue, f.Link, meta); err != nil {
			return fmt.Errorf("store: insert grabber file %d/%d: %w", entryID, f.Idx, err)
		}
	}
	return nil
}

const grabberCols = `id, url, hoster, state, error, package_name, is_folder, source, auto_confirm, dest_dir, created_at, updated_at`

func scanGrabberEntry(sc interface{ Scan(...any) error }) (GrabberEntryRow, error) {
	var e GrabberEntryRow
	var isFolder, auto int
	var created, updated int64
	err := sc.Scan(&e.ID, &e.URL, &e.Hoster, &e.State, &e.Error, &e.PackageName, &isFolder, &e.Source, &auto,
		&e.DestDir, &created, &updated)
	e.IsFolder, e.AutoConfirm = isFolder != 0, auto != 0
	e.CreatedAt, e.UpdatedAt = TimeFromDB(created), TimeFromDB(updated)
	return e, err
}

// GetGrabberEntry returns one entry with its files, or ErrNotFound.
func (s *Store) GetGrabberEntry(ctx context.Context, id int64) (GrabberEntryRow, error) {
	e, err := scanGrabberEntry(s.db.QueryRowContext(ctx, `SELECT `+grabberCols+` FROM linkgrabber_entries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, fmt.Errorf("grabber entry %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return e, fmt.Errorf("store: get grabber entry %d: %w", id, err)
	}
	files, err := s.grabberFiles(ctx, []int64{id})
	if err != nil {
		return e, err
	}
	e.Files = files[id]
	return e, nil
}

// ListGrabberEntries returns every entry with its files, oldest first.
func (s *Store) ListGrabberEntries(ctx context.Context) ([]GrabberEntryRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+grabberCols+` FROM linkgrabber_entries ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list grabber entries: %w", err)
	}
	var out []GrabberEntryRow
	for rows.Next() {
		e, err := scanGrabberEntry(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan grabber entry: %w", err)
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	files, err := s.grabberFiles(ctx, nil)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Files = files[out[i].ID]
	}
	return out, nil
}

// grabberFiles loads files for ids (all entries if ids is nil).
func (s *Store) grabberFiles(ctx context.Context, ids []int64) (map[int64][]GrabberFileRow, error) {
	q := `SELECT entry_id, idx, file_id, name, rel_path, size, checksum_algo, checksum_value, link, meta FROM linkgrabber_files`
	var args []any
	if ids != nil {
		q += ` WHERE entry_id IN (` + motorPlaceholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	q += ` ORDER BY entry_id, idx`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list grabber files: %w", err)
	}
	defer rows.Close()
	out := map[int64][]GrabberFileRow{}
	for rows.Next() {
		var id int64
		var f GrabberFileRow
		var meta string
		if err := rows.Scan(&id, &f.Idx, &f.FileID, &f.Name, &f.RelPath, &f.Size, &f.ChecksumAlgo, &f.ChecksumValue,
			&f.Link, &meta); err != nil {
			return nil, fmt.Errorf("store: scan grabber file: %w", err)
		}
		if meta != "" && meta != "{}" {
			if err := json.Unmarshal([]byte(meta), &f.Meta); err != nil {
				return nil, fmt.Errorf("store: grabber file meta: %w", err)
			}
		}
		out[id] = append(out[id], f)
	}
	return out, rows.Err()
}

// DeleteGrabberEntries removes entries (files cascade). Missing ids are
// ignored.
func (s *Store) DeleteGrabberEntries(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM linkgrabber_entries WHERE id IN (`+motorPlaceholders(len(ids))+`)`, args...); err != nil {
		return fmt.Errorf("store: delete grabber entries: %w", err)
	}
	return nil
}

// CountGrabberEntries returns the number of LinkGrabber entries.
func (s *Store) CountGrabberEntries(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM linkgrabber_entries`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count grabber entries: %w", err)
	}
	return n, nil
}
