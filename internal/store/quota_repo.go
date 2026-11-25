package store

import (
	"context"
	"fmt"
	"time"
)

// QuotaMark is one row of quota_marks: (Hoster, Scope, Subject) is exhausted
// until Until. Scope is hosters.QuotaScope.String() ("ip", "account",
// "file"); Subject is an egress ID, an account ID or a hoster file ID.
type QuotaMark struct {
	Hoster    string
	Scope     string
	Subject   string
	Until     time.Time
	Reason    string
	CreatedAt time.Time
}

// UpsertQuotaMark creates or replaces the mark for (Hoster, Scope, Subject).
// A zero CreatedAt is set to now.
func (s *Store) UpsertQuotaMark(ctx context.Context, m QuotaMark) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO quota_marks (hoster, scope, subject, until, reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (hoster, scope, subject) DO UPDATE SET
		   until = excluded.until, reason = excluded.reason, created_at = excluded.created_at`,
		m.Hoster, m.Scope, m.Subject, TimeToDB(m.Until), m.Reason, TimeToDB(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: upsert quota mark %s/%s/%s: %w", m.Hoster, m.Scope, m.Subject, err)
	}
	return nil
}

// ListQuotaMarks returns the marks whose Until is after the given time
// (pass the zero time for every mark), ordered by Until.
func (s *Store) ListQuotaMarks(ctx context.Context, after time.Time) ([]QuotaMark, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hoster, scope, subject, until, reason, created_at
		 FROM quota_marks WHERE until > ? ORDER BY until, hoster, scope, subject`,
		TimeToDB(after))
	if err != nil {
		return nil, fmt.Errorf("store: list quota marks: %w", err)
	}
	defer rows.Close()
	var out []QuotaMark
	for rows.Next() {
		var m QuotaMark
		var until, created int64
		if err := rows.Scan(&m.Hoster, &m.Scope, &m.Subject, &until, &m.Reason, &created); err != nil {
			return nil, fmt.Errorf("store: scan quota mark: %w", err)
		}
		m.Until = TimeFromDB(until)
		m.CreatedAt = TimeFromDB(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list quota marks: %w", err)
	}
	return out, nil
}

// DeleteQuotaMark removes one mark; deleting a missing mark is not an error.
func (s *Store) DeleteQuotaMark(ctx context.Context, hoster, scope, subject string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM quota_marks WHERE hoster = ? AND scope = ? AND subject = ?`,
		hoster, scope, subject); err != nil {
		return fmt.Errorf("store: delete quota mark: %w", err)
	}
	return nil
}

// DeleteQuotaMarksBySubject removes the marks of subject for every hoster
// (e.g. after an egress got a new exit IP). It returns the number removed.
func (s *Store) DeleteQuotaMarksBySubject(ctx context.Context, scope, subject string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM quota_marks WHERE scope = ? AND subject = ?`, scope, subject)
	if err != nil {
		return 0, fmt.Errorf("store: delete quota marks of %s/%s: %w", scope, subject, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeExpiredQuotaMarks removes the marks whose Until is not after before.
func (s *Store) PurgeExpiredQuotaMarks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM quota_marks WHERE until <= ?`, TimeToDB(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge quota marks: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
