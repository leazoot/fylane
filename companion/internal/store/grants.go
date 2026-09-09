package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CommandGrant is a workspace the user authorized to run commands without
// being asked for each one.
type CommandGrant struct {
	WorkspaceID string    `json:"workspace_id"`
	Rung        string    `json:"rung"`
	GrantedAt   time.Time `json:"granted_at"`
}

// GrantCommands records the authorization, replacing any earlier one so a
// grant taken under a different rung does not survive the change.
func (s *Store) GrantCommands(ctx context.Context, workspaceID, rung string, at time.Time) error {
	if workspaceID == "" || rung == "" {
		return fmt.Errorf("command grant: workspace and rung are required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO command_grants (workspace_id, rung, granted_at)
		VALUES (?, ?, ?)
		ON CONFLICT (workspace_id) DO UPDATE SET rung = excluded.rung, granted_at = excluded.granted_at`,
		workspaceID, rung, formatTime(at))
	if err != nil {
		return fmt.Errorf("granting commands: %w", err)
	}
	return nil
}

// CommandGrantFor returns the grant for a workspace, or ErrNotFound.
func (s *Store) CommandGrantFor(ctx context.Context, workspaceID string) (*CommandGrant, error) {
	var (
		g       CommandGrant
		granted string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT workspace_id, rung, granted_at FROM command_grants WHERE workspace_id = ?`,
		workspaceID).Scan(&g.WorkspaceID, &g.Rung, &granted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading command grant: %w", err)
	}
	if g.GrantedAt, err = parseTime(granted); err != nil {
		return nil, fmt.Errorf("decoding command grant timestamp: %w", err)
	}
	return &g, nil
}

// RevokeCommands removes the grant. Revoking one that is not there is not an
// error: the caller wanted no grant, and there is none.
func (s *Store) RevokeCommands(ctx context.Context, workspaceID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM command_grants WHERE workspace_id = ?`, workspaceID); err != nil {
		return fmt.Errorf("revoking commands: %w", err)
	}
	return nil
}

// ListCommandGrants returns every grant, newest first. This is what the
// desktop shows so a grant can never be in force invisibly.
func (s *Store) ListCommandGrants(ctx context.Context) ([]*CommandGrant, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workspace_id, rung, granted_at FROM command_grants ORDER BY granted_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing command grants: %w", err)
	}
	defer rows.Close()

	var out []*CommandGrant
	for rows.Next() {
		var (
			g       CommandGrant
			granted string
		)
		if err := rows.Scan(&g.WorkspaceID, &g.Rung, &granted); err != nil {
			return nil, fmt.Errorf("scanning command grant: %w", err)
		}
		if g.GrantedAt, err = parseTime(granted); err != nil {
			return nil, fmt.Errorf("decoding command grant timestamp: %w", err)
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}
