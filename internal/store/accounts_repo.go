package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"admindescargas/internal/accounts"
)

// The methods below implement accounts.Repo. Only metadata is stored:
// secret_ref is the SecretStore key (the account ID), never a secret.

var _ accounts.Repo = (*Store)(nil)

const accountColumns = `id, hoster, label, kind, username, enabled, allow_rotation, status, status_msg,
	premium, premium_until, quota_total, quota_used, checked_at, created_at, updated_at`

func accountNotFound(id string) error {
	return fmt.Errorf("account %q: %w: %w", id, ErrNotFound, accounts.ErrAccountNotFound)
}

func scanAccount(sc interface{ Scan(...any) error }) (*accounts.Account, error) {
	var (
		a                       accounts.Account
		kind, status            string
		enabled, rot, premium   int64
		premiumUntil, checkedAt sql.NullInt64
		createdAt, updatedAt    int64
	)
	if err := sc.Scan(&a.ID, &a.Hoster, &a.Label, &kind, &a.Username, &enabled, &rot, &status, &a.StatusMsg,
		&premium, &premiumUntil, &a.QuotaTotal, &a.QuotaUsed, &checkedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	a.Kind = accounts.Kind(kind)
	a.Status = accounts.Status(status)
	a.Enabled = enabled != 0
	a.AllowRotation = rot != 0
	a.Premium = premium != 0
	a.PremiumUntil = NullTimeFromDB(premiumUntil)
	a.CheckedAt = NullTimeFromDB(checkedAt)
	a.CreatedAt = TimeFromDB(createdAt)
	a.UpdatedAt = TimeFromDB(updatedAt)
	return &a, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// InsertAccount inserts a new account row (secret_ref = ID).
func (s *Store) InsertAccount(ctx context.Context, a *accounts.Account) error {
	now := time.Now()
	created, updated := a.CreatedAt, a.UpdatedAt
	if created.IsZero() {
		created = now
	}
	if updated.IsZero() {
		updated = created
	}
	status := a.Status
	if status == "" {
		status = accounts.StatusUnknown
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO accounts (`+accountColumns+`, secret_ref)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Hoster, a.Label, string(a.Kind), a.Username, b2i(a.Enabled), b2i(a.AllowRotation),
		string(status), a.StatusMsg, b2i(a.Premium), NullTimeToDB(a.PremiumUntil), a.QuotaTotal, a.QuotaUsed,
		NullTimeToDB(a.CheckedAt), TimeToDB(created), TimeToDB(updated), a.ID)
	if err != nil {
		return fmt.Errorf("store: insert account: %w", err)
	}
	return nil
}

// GetAccount returns one account; a missing row wraps both ErrNotFound and
// accounts.ErrAccountNotFound.
func (s *Store) GetAccount(ctx context.Context, id string) (*accounts.Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, accountNotFound(id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get account: %w", err)
	}
	return a, nil
}

// ListAccounts returns the accounts of hoster ("" = all), oldest first.
func (s *Store) ListAccounts(ctx context.Context, hoster string) ([]*accounts.Account, error) {
	q := `SELECT ` + accountColumns + ` FROM accounts`
	var args []any
	if hoster != "" {
		q += ` WHERE hoster = ?`
		args = append(args, hoster)
	}
	q += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()
	var out []*accounts.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	return out, nil
}

// UpdateAccount writes every mutable column of a (hoster, kind, created_at
// and secret_ref are immutable).
func (s *Store) UpdateAccount(ctx context.Context, a *accounts.Account) error {
	updated := a.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET label = ?, username = ?, enabled = ?, allow_rotation = ?,
		status = ?, status_msg = ?, premium = ?, premium_until = ?, quota_total = ?, quota_used = ?,
		checked_at = ?, updated_at = ? WHERE id = ?`,
		a.Label, a.Username, b2i(a.Enabled), b2i(a.AllowRotation), string(a.Status), a.StatusMsg,
		b2i(a.Premium), NullTimeToDB(a.PremiumUntil), a.QuotaTotal, a.QuotaUsed,
		NullTimeToDB(a.CheckedAt), TimeToDB(updated), a.ID)
	if err != nil {
		return fmt.Errorf("store: update account: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return accountNotFound(a.ID)
	}
	return nil
}

// DeleteAccount removes the row (downloads.account_id is set to NULL by the
// foreign key). Deleting a missing account wraps ErrNotFound.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return accountNotFound(id)
	}
	return nil
}

// MarkAccountQuota upserts a quota_marks row with scope 'account'.
func (s *Store) MarkAccountQuota(ctx context.Context, m accounts.QuotaMark) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO quota_marks (hoster, scope, subject, until, reason, created_at)
		VALUES (?, 'account', ?, ?, ?, ?)
		ON CONFLICT (hoster, scope, subject) DO UPDATE SET until = excluded.until, reason = excluded.reason`,
		m.Hoster, m.AccountID, TimeToDB(m.Until), m.Reason, TimeToDB(time.Now()))
	if err != nil {
		return fmt.Errorf("store: mark account quota: %w", err)
	}
	return nil
}

// ClearAccountQuota deletes the account's quota mark (missing = no error).
func (s *Store) ClearAccountQuota(ctx context.Context, hoster, accountID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM quota_marks WHERE hoster = ? AND scope = 'account' AND subject = ?`,
		hoster, accountID)
	if err != nil {
		return fmt.Errorf("store: clear account quota: %w", err)
	}
	return nil
}

// AccountQuotaMarks returns the account quota marks still active at now.
func (s *Store) AccountQuotaMarks(ctx context.Context, now time.Time) ([]accounts.QuotaMark, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hoster, subject, until, reason FROM quota_marks
		WHERE scope = 'account' AND until > ? ORDER BY until`, TimeToDB(now))
	if err != nil {
		return nil, fmt.Errorf("store: account quota marks: %w", err)
	}
	defer rows.Close()
	var out []accounts.QuotaMark
	for rows.Next() {
		var m accounts.QuotaMark
		var until int64
		if err := rows.Scan(&m.Hoster, &m.AccountID, &until, &m.Reason); err != nil {
			return nil, fmt.Errorf("store: scan quota mark: %w", err)
		}
		m.Until = TimeFromDB(until)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: account quota marks: %w", err)
	}
	return out, nil
}
