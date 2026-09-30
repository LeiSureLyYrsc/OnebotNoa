package store

import (
	"context"
	"fmt"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
)

// AppendAudit writes one audit entry.
func (s *Store) AppendAudit(ctx context.Context, entry model.AuditEntry) error {
	at := entry.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO audit_log (at, actor, action, target, detail, ip) VALUES (?, ?, ?, ?, ?, ?)",
		timeToUnix(at), entry.Actor, entry.Action, entry.Target, entry.Detail, entry.IP)
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// ListAudit returns audit entries newest first.
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]model.AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, at, actor, action, target, detail, ip FROM audit_log
ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []model.AuditEntry{}
	for rows.Next() {
		var (
			e  model.AuditEntry
			at int64
		)
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		e.At = unixToTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountAudit reports the number of audit rows.
func (s *Store) CountAudit(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log").Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count audit: %w", err)
	}
	return n, nil
}
