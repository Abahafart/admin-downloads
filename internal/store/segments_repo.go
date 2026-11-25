package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SegmentRow is one byte range of a download: [Start, End) with End = -1
// for an open-ended single stream. Done counts bytes from Start that are
// fsynced to the .part file.
type SegmentRow struct {
	Idx   int
	Start int64
	End   int64
	Done  int64
}

// ListSegments returns the segments of a download ordered by idx.
func (s *Store) ListSegments(ctx context.Context, downloadID int64) ([]SegmentRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT idx, start_off, end_off, done FROM segments WHERE download_id = ? ORDER BY idx`, downloadID)
	if err != nil {
		return nil, fmt.Errorf("store: list segments %d: %w", downloadID, err)
	}
	defer rows.Close()
	var out []SegmentRow
	for rows.Next() {
		var sg SegmentRow
		if err := rows.Scan(&sg.Idx, &sg.Start, &sg.End, &sg.Done); err != nil {
			return nil, fmt.Errorf("store: scan segment: %w", err)
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// SaveProgress atomically replaces the segment map of a download and sets
// done_bytes. Callers must fsync the .part file before calling it so the
// persisted progress is never ahead of the data on disk.
func (s *Store) SaveProgress(ctx context.Context, downloadID int64, done int64, segs []SegmentRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE download_id = ?`, downloadID); err != nil {
			return fmt.Errorf("store: clear segments %d: %w", downloadID, err)
		}
		for i, sg := range segs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO segments (download_id, idx, start_off, end_off, done) VALUES (?, ?, ?, ?, ?)`,
				downloadID, i, sg.Start, sg.End, sg.Done); err != nil {
				return fmt.Errorf("store: insert segment %d/%d: %w", downloadID, i, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE downloads SET done_bytes = ?, updated_at = ? WHERE id = ?`,
			done, TimeToDB(time.Now()), downloadID); err != nil {
			return fmt.Errorf("store: update done %d: %w", downloadID, err)
		}
		return nil
	})
}

// DeleteSegments removes the segment map of a download.
func (s *Store) DeleteSegments(ctx context.Context, downloadID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM segments WHERE download_id = ?`, downloadID); err != nil {
		return fmt.Errorf("store: delete segments %d: %w", downloadID, err)
	}
	return nil
}
