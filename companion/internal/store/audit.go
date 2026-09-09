package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AuditEvent is one append-only audit record. PathRelative is
// always workspace-relative — audit rows must never contain absolute paths.
type AuditEvent struct {
	ID           int64     `json:"id"`
	ChangeSetID  string    `json:"change_set_id,omitempty"`
	EventType    string    `json:"event_type"`
	PathRelative string    `json:"path_relative,omitempty"`
	Result       string    `json:"result"`
	DurationMS   int64     `json:"duration_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

// AppendAuditEvent inserts an audit event. CreatedAt is set to now when zero;
// the row ID is written back to e.ID. Audit rows are never updated or
// deleted by feature code.
func (s *Store) AppendAuditEvent(ctx context.Context, e *AuditEvent) error {
	switch {
	case e.EventType == "":
		return fmt.Errorf("audit event: missing event type")
	case e.Result == "":
		return fmt.Errorf("audit event %s: missing result", e.EventType)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	changeSet := sql.NullString{String: e.ChangeSetID, Valid: e.ChangeSetID != ""}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO audit_events (change_set_id, event_type, path_relative, result, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id`,
		changeSet, e.EventType, e.PathRelative, e.Result, e.DurationMS, formatTime(e.CreatedAt))
	if err := row.Scan(&e.ID); err != nil {
		return fmt.Errorf("appending audit event %s: %w", e.EventType, err)
	}
	return nil
}

// ListAuditEvents returns events for one change set in insertion order.
func (s *Store) ListAuditEvents(ctx context.Context, changeSetID string) ([]*AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, change_set_id, event_type, path_relative, result, duration_ms, created_at
		FROM audit_events WHERE change_set_id = ? ORDER BY id`, changeSetID)
	if err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	defer rows.Close()
	var out []*AuditEvent
	for rows.Next() {
		var (
			e         AuditEvent
			changeSet sql.NullString
			created   string
		)
		if err := rows.Scan(&e.ID, &changeSet, &e.EventType, &e.PathRelative,
			&e.Result, &e.DurationMS, &created); err != nil {
			return nil, fmt.Errorf("listing audit events: %w", err)
		}
		e.ChangeSetID = changeSet.String
		if e.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("listing audit events: %w", err)
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	return out, nil
}
