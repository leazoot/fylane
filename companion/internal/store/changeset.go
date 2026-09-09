package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ChangeSet statuses. Lifecycle: pending → approved → applied, or a terminal
// denied / failed / rolled_back. Acceptance is deliberately not among them:
// it is something the user does to an applied change set, not something the
// engine does instead of applying it (see migration 0006).
const (
	ChangeSetPending    = "pending"
	ChangeSetApproved   = "approved"
	ChangeSetApplied    = "applied"
	ChangeSetDenied     = "denied"
	ChangeSetFailed     = "failed"
	ChangeSetRolledBack = "rolled_back"
)

// ChangeSet records one multi-operation write transaction.
// Operations keeps the caller's operation list verbatim; the store does
// not interpret it. BackupLocation is a path under the local backup area and
// must never be serialized off the machine (`json:"-"`).
type ChangeSet struct {
	ID               string            `json:"id"`
	WorkspaceID      string            `json:"workspace_id"`
	Provider         string            `json:"provider"`
	Summary          string            `json:"summary"`
	Operations       json.RawMessage   `json:"operations"`
	Status           string            `json:"status"`
	BeforeHashes     map[string]string `json:"before_hashes"`
	AfterHashes      map[string]string `json:"after_hashes"`
	BackupLocation   string            `json:"-"`
	CreatedAt        time.Time         `json:"created_at"`
	ApprovedAt       time.Time         `json:"approved_at,omitzero"`
	AppliedAt        time.Time         `json:"applied_at,omitzero"`
	RollbackDeadline time.Time         `json:"rollback_deadline,omitzero"`
	// AcceptedAt is when the user reviewed the applied change and said it
	// was right. Zero means nobody has yet — which is not the same as the
	// rollback window having closed on a change nobody looked at.
	AcceptedAt time.Time `json:"accepted_at,omitzero"`
}

func (c *ChangeSet) validate() error {
	switch {
	case c.ID == "":
		return fmt.Errorf("change set: missing id")
	case c.WorkspaceID == "":
		return fmt.Errorf("change set %s: missing workspace id", c.ID)
	case len(c.Operations) == 0:
		return fmt.Errorf("change set %s: missing operations", c.ID)
	case c.Status == "":
		return fmt.Errorf("change set %s: missing status", c.ID)
	}
	return nil
}

// CreateChangeSet inserts a new change set. CreatedAt is set to now when zero.
func (s *Store) CreateChangeSet(ctx context.Context, c *ChangeSet) error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	before, after, err := marshalHashes(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO change_sets
			(id, workspace_id, provider, summary, operations, status, before_hashes, after_hashes,
			 backup_location, created_at, approved_at, applied_at, rollback_deadline)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Provider, c.Summary, string(c.Operations), c.Status,
		before, after, c.BackupLocation, formatTime(c.CreatedAt),
		formatNullableTime(c.ApprovedAt), formatNullableTime(c.AppliedAt),
		formatNullableTime(c.RollbackDeadline))
	if err != nil {
		return fmt.Errorf("creating change set %s: %w", c.ID, err)
	}
	return nil
}

// UpdateChangeSet replaces the mutable lifecycle fields (status, hashes,
// backup location, approval/apply/rollback times). Identity fields
// (workspace, provider, operations) are immutable after creation.
func (s *Store) UpdateChangeSet(ctx context.Context, c *ChangeSet) error {
	if err := c.validate(); err != nil {
		return err
	}
	before, after, err := marshalHashes(c)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE change_sets
		SET status = ?, before_hashes = ?, after_hashes = ?, backup_location = ?,
			approved_at = ?, applied_at = ?, rollback_deadline = ?
		WHERE id = ?`,
		c.Status, before, after, c.BackupLocation,
		formatNullableTime(c.ApprovedAt), formatNullableTime(c.AppliedAt),
		formatNullableTime(c.RollbackDeadline), c.ID)
	if err != nil {
		return fmt.Errorf("updating change set %s: %w", c.ID, err)
	}
	return requireRow(res, "change set", c.ID)
}

// AcceptChangeSet records that the user reviewed an applied change set and
// said it was right, and returns the record as it now stands.
//
// The condition is in the statement rather than in the caller so that neither
// a concurrent rollback nor a second click can move the timestamp: acceptance
// is recorded once, because "when did a human look at this" has one answer.
// A change set that is not applied, or is already accepted, is left alone —
// the caller compares the returned record against what it expected.
func (s *Store) AcceptChangeSet(ctx context.Context, id string, at time.Time) (*ChangeSet, error) {
	_, err := s.db.ExecContext(ctx, `
		UPDATE change_sets SET accepted_at = ?
		WHERE id = ? AND status = ? AND accepted_at IS NULL`,
		formatTime(at.UTC()), id, ChangeSetApplied)
	if err != nil {
		return nil, fmt.Errorf("accepting change set %s: %w", id, err)
	}
	return s.GetChangeSet(ctx, id)
}

// GetChangeSet returns the change set with the given ID, or ErrNotFound.
func (s *Store) GetChangeSet(ctx context.Context, id string) (*ChangeSet, error) {
	row := s.db.QueryRowContext(ctx, changeSetSelect+` WHERE id = ?`, id)
	c, err := scanChangeSet(row)
	if err != nil {
		return nil, fmt.Errorf("getting change set %s: %w", id, err)
	}
	return c, nil
}

// ListChangeSets returns change sets for a workspace, newest first. limit <= 0
// means no limit.
func (s *Store) ListChangeSets(ctx context.Context, workspaceID string, limit int) ([]*ChangeSet, error) {
	query := changeSetSelect + ` WHERE workspace_id = ? ORDER BY created_at DESC, id DESC`
	args := []any{workspaceID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing change sets: %w", err)
	}
	defer rows.Close()
	var out []*ChangeSet
	for rows.Next() {
		c, err := scanChangeSet(rows)
		if err != nil {
			return nil, fmt.Errorf("listing change sets: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing change sets: %w", err)
	}
	return out, nil
}

// ListChangeSetsByStatus returns all change sets in any of the given
// statuses, oldest first. Used by crash recovery to find interrupted
// transactions.
func (s *Store) ListChangeSetsByStatus(ctx context.Context, statuses ...string) ([]*ChangeSet, error) {
	if len(statuses) == 0 {
		return nil, fmt.Errorf("listing change sets: no statuses given")
	}
	query := changeSetSelect + ` WHERE status IN (?` + strings.Repeat(",?", len(statuses)-1) + `) ORDER BY created_at, id`
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing change sets by status: %w", err)
	}
	defer rows.Close()
	var out []*ChangeSet
	for rows.Next() {
		c, err := scanChangeSet(rows)
		if err != nil {
			return nil, fmt.Errorf("listing change sets by status: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing change sets by status: %w", err)
	}
	return out, nil
}

// DeleteChangeSets removes every recorded change set for a workspace and
// returns how many rows went along with the backup locations that are now
// unreferenced, so the caller can delete the undo copies too. Clearing the
// trace is the user's explicit, confirmed choice to give up those copies
// (design A14); it never touches a file inside the workspace. In-flight sets
// are kept — a change set waiting at the gate still has a caller blocked on
// it.
//
// The audit events recorded against those change sets go in the same
// transaction. They reference change_sets, so leaving them would fail the
// foreign key, and keeping them would contradict what the screen promises:
// the record of what happened when is what the user asked to clear.
func (s *Store) DeleteChangeSets(ctx context.Context, workspaceID string) (int, []string, error) {
	if workspaceID == "" {
		return 0, nil, fmt.Errorf("deleting change sets: workspace id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("deleting change sets: %w", err)
	}
	defer tx.Rollback()

	const keep = `status NOT IN ('` + ChangeSetPending + `', '` + ChangeSetApproved + `')`
	const scope = `SELECT id FROM change_sets WHERE workspace_id = ? AND ` + keep
	rows, err := tx.QueryContext(ctx,
		`SELECT backup_location FROM change_sets WHERE workspace_id = ? AND `+keep, workspaceID)
	if err != nil {
		return 0, nil, fmt.Errorf("deleting change sets: %w", err)
	}
	var backups []string
	deleted := 0
	for rows.Next() {
		var loc string
		if err := rows.Scan(&loc); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("deleting change sets: %w", err)
		}
		deleted++
		if loc != "" {
			backups = append(backups, loc)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("deleting change sets: %w", err)
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM audit_events WHERE change_set_id IN (`+scope+`)`, workspaceID); err != nil {
		return 0, nil, fmt.Errorf("deleting audit events: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM change_sets WHERE workspace_id = ? AND `+keep, workspaceID); err != nil {
		return 0, nil, fmt.Errorf("deleting change sets: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("deleting change sets: %w", err)
	}
	return deleted, backups, nil
}

// ClearBackups drops the undo copies for a workspace and returns how many
// change sets lost one, along with the locations to remove from disk.
//
// Narrower than DeleteChangeSets on purpose: the record of what was written
// stays. What goes is the ability to take it back, so the rollback deadline
// is cleared in the same transaction — a row still advertising a window whose
// copy no longer exists would offer an undo that can only fail.
//
// Change sets still on the gate are untouched: they have not been applied,
// so there is nothing of theirs on disk to reclaim.
func (s *Store) ClearBackups(ctx context.Context, workspaceID string) (int, []string, error) {
	if workspaceID == "" {
		return 0, nil, fmt.Errorf("clearing undo copies: workspace id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("clearing undo copies: %w", err)
	}
	defer tx.Rollback()

	const scope = `workspace_id = ? AND backup_location != '' AND status NOT IN ('` +
		ChangeSetPending + `', '` + ChangeSetApproved + `')`
	rows, err := tx.QueryContext(ctx, `SELECT backup_location FROM change_sets WHERE `+scope, workspaceID)
	if err != nil {
		return 0, nil, fmt.Errorf("clearing undo copies: %w", err)
	}
	var backups []string
	for rows.Next() {
		var loc string
		if err := rows.Scan(&loc); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("clearing undo copies: %w", err)
		}
		backups = append(backups, loc)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("clearing undo copies: %w", err)
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx,
		`UPDATE change_sets SET backup_location = '', rollback_deadline = NULL WHERE `+scope,
		workspaceID); err != nil {
		return 0, nil, fmt.Errorf("clearing undo copies: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("clearing undo copies: %w", err)
	}
	return len(backups), backups, nil
}

const changeSetSelect = `
	SELECT id, workspace_id, provider, summary, operations, status, before_hashes, after_hashes,
		backup_location, created_at, approved_at, applied_at, rollback_deadline, accepted_at
	FROM change_sets`

func marshalHashes(c *ChangeSet) (before, after string, err error) {
	b, err := marshalStringMap(c.BeforeHashes)
	if err != nil {
		return "", "", fmt.Errorf("change set %s: encoding before hashes: %w", c.ID, err)
	}
	a, err := marshalStringMap(c.AfterHashes)
	if err != nil {
		return "", "", fmt.Errorf("change set %s: encoding after hashes: %w", c.ID, err)
	}
	return b, a, nil
}

func marshalStringMap(m map[string]string) (string, error) {
	if m == nil {
		m = map[string]string{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func scanChangeSet(row interface{ Scan(...any) error }) (*ChangeSet, error) {
	var (
		c                          ChangeSet
		operations, before, after  string
		created                    string
		approved, applied, rollbck sql.NullString
		accepted                   sql.NullString
	)
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.Provider, &c.Summary, &operations,
		&c.Status, &before, &after, &c.BackupLocation, &created,
		&approved, &applied, &rollbck, &accepted)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Operations = json.RawMessage(operations)
	if err := json.Unmarshal([]byte(before), &c.BeforeHashes); err != nil {
		return nil, fmt.Errorf("decoding before hashes: %w", err)
	}
	if err := json.Unmarshal([]byte(after), &c.AfterHashes); err != nil {
		return nil, fmt.Errorf("decoding after hashes: %w", err)
	}
	if c.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if c.ApprovedAt, err = parseNullableTime(approved); err != nil {
		return nil, err
	}
	if c.AppliedAt, err = parseNullableTime(applied); err != nil {
		return nil, err
	}
	if c.RollbackDeadline, err = parseNullableTime(rollbck); err != nil {
		return nil, err
	}
	if c.AcceptedAt, err = parseNullableTime(accepted); err != nil {
		return nil, err
	}
	return &c, nil
}
