package store

import (
	"context"
	"fmt"
	"time"
)

// History outcomes.
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeDeleted   = "deleted"
)

// HistoryRow is one row of the history table.
type HistoryRow struct {
	ID            int64
	URL           string
	Hoster        string
	FileID        string
	Name          string
	PackageName   string
	DestPath      string
	Size          int64
	ChecksumAlgo  string
	ChecksumValue string
	Outcome       string
	Error         string
	FinishedAt    time.Time
}

// AddHistory inserts h (ID filled in; FinishedAt defaults to now).
func (s *Store) AddHistory(ctx context.Context, h *HistoryRow) error {
	if h.FinishedAt.IsZero() {
		h.FinishedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO history (url, hoster, file_id, name, package_name, dest_path, size, checksum_algo,
		   checksum_value, outcome, error, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.URL, h.Hoster, h.FileID, h.Name, h.PackageName, h.DestPath, h.Size, h.ChecksumAlgo,
		h.ChecksumValue, h.Outcome, h.Error, TimeToDB(h.FinishedAt))
	if err != nil {
		return fmt.Errorf("store: add history: %w", err)
	}
	h.ID, _ = res.LastInsertId()
	return nil
}

// ListHistory returns the most recent history rows first (limit <= 0 means
// 500).
func (s *Store) ListHistory(ctx context.Context, limit int) ([]HistoryRow, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, url, hoster, file_id, name, package_name, dest_path, size, checksum_algo, checksum_value,
		        outcome, error, finished_at
		 FROM history ORDER BY finished_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list history: %w", err)
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var h HistoryRow
		var fin int64
		if err := rows.Scan(&h.ID, &h.URL, &h.Hoster, &h.FileID, &h.Name, &h.PackageName, &h.DestPath, &h.Size,
			&h.ChecksumAlgo, &h.ChecksumValue, &h.Outcome, &h.Error, &fin); err != nil {
			return nil, fmt.Errorf("store: scan history: %w", err)
		}
		h.FinishedAt = TimeFromDB(fin)
		out = append(out, h)
	}
	return out, rows.Err()
}
