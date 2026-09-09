package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ExecEvent is one append-only record of a command Fylane was asked to run
// . DirRelative is always workspace-relative and Argv is what the caller
// wrote — neither ever holds an absolute path.
type ExecEvent struct {
	ID          int64    `json:"id"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
	DirRelative string   `json:"dir_relative,omitempty"`
	Argv        []string `json:"argv"`
	Outcome     string   `json:"outcome"`
	ExitCode    int      `json:"exit_code"`
	DurationMS  int64    `json:"duration_ms"`
	RuleID      string   `json:"rule_id,omitempty"`
	// Provider is the platform that asked. Empty on rows written before
	// migration 0004, and on anything the caller could not name.
	Provider string `json:"provider,omitempty"`
	// RunID ties the 'started' row of one execution to the row that reports
	// how it ended. Empty on rows written before migration 0005 and on
	// attempts that never became a run at all — a refusal has nothing to
	// pair with.
	RunID     string    `json:"run_id,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// OutcomeStarted marks the row written when a process is up but has not
// finished. It is the only non-terminal outcome, and the only one that can be
// orphaned by a Core that stops mid-run.
const OutcomeStarted = "started"

// OutcomeInterrupted is what startup writes for a run whose 'started' row has
// no sibling: Fylane stopped while the command was running, so whether it
// finished, and what it left on disk, is not known to anyone.
const OutcomeInterrupted = "interrupted"

// AppendExecEvent inserts an execution record. CreatedAt is set to now when
// zero; the row ID is written back to e.ID. These rows are never updated or
// deleted by feature code.
func (s *Store) AppendExecEvent(ctx context.Context, e *ExecEvent) error {
	switch {
	case len(e.Argv) == 0:
		return fmt.Errorf("exec event: missing argv")
	case e.Outcome == "":
		return fmt.Errorf("exec event: missing outcome")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	argv, err := json.Marshal(e.Argv)
	if err != nil {
		return fmt.Errorf("encoding exec event argv: %w", err)
	}
	workspace := sql.NullString{String: e.WorkspaceID, Valid: e.WorkspaceID != ""}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO exec_events
			(workspace_id, dir_relative, argv, outcome, exit_code, duration_ms, rule_id, reason, provider, run_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		workspace, e.DirRelative, string(argv), e.Outcome, e.ExitCode,
		e.DurationMS, e.RuleID, e.Reason, e.Provider, e.RunID, formatTime(e.CreatedAt))
	if err := row.Scan(&e.ID); err != nil {
		return fmt.Errorf("appending exec event: %w", err)
	}
	return nil
}

// ListExecEvents returns the most recent events, newest first. limit must be
// positive; it is what keeps the desktop history view from loading a year of
// builds into memory.
//
// 'started' rows are left out. They are bookkeeping for the pairing, not
// history: a running command is already on the screen from the live task
// list, and a finished one would otherwise be drawn twice — once as the
// moment it began and once as what it did.
func (s *Store) ListExecEvents(ctx context.Context, limit int) ([]*ExecEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+execColumns+`
		FROM exec_events
		WHERE outcome <> ?
		ORDER BY id DESC
		LIMIT ?`, OutcomeStarted, limit)
	if err != nil {
		return nil, fmt.Errorf("listing exec events: %w", err)
	}
	return scanExecEvents(rows)
}

// execColumns is the one column list every exec query selects, so a new
// column cannot reach one reader and miss another.
const execColumns = `id, workspace_id, dir_relative, argv, outcome, exit_code,
	duration_ms, rule_id, reason, provider, run_id, created_at`

func scanExecEvents(rows *sql.Rows) ([]*ExecEvent, error) {
	defer rows.Close()
	var out []*ExecEvent
	for rows.Next() {
		var (
			e         ExecEvent
			workspace sql.NullString
			argv      string
			created   string
		)
		if err := rows.Scan(&e.ID, &workspace, &e.DirRelative, &argv, &e.Outcome,
			&e.ExitCode, &e.DurationMS, &e.RuleID, &e.Reason, &e.Provider, &e.RunID, &created); err != nil {
			return nil, fmt.Errorf("scanning exec event: %w", err)
		}
		e.WorkspaceID = workspace.String
		if err := json.Unmarshal([]byte(argv), &e.Argv); err != nil {
			return nil, fmt.Errorf("decoding exec event %d argv: %w", e.ID, err)
		}
		var err error
		if e.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("decoding exec event %d timestamp: %w", e.ID, err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// OrphanedRuns returns the 'started' rows that never got a sibling — the
// commands Fylane was running when it stopped. Oldest first, because that is
// the order they are answered in.
//
// A row with an empty run_id can never be paired and is therefore never an
// orphan: those predate migration 0005 and are already complete records.
func (s *Store) OrphanedRuns(ctx context.Context) ([]*ExecEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+execColumns+`
		FROM exec_events AS started
		WHERE started.outcome = ? AND started.run_id <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM exec_events AS ended
			WHERE ended.run_id = started.run_id AND ended.outcome <> ?
		  )
		ORDER BY started.id`, OutcomeStarted, OutcomeStarted)
	if err != nil {
		return nil, fmt.Errorf("listing orphaned runs: %w", err)
	}
	return scanExecEvents(rows)
}

// FindRun returns how a run ended, or nil when nothing is recorded under that
// id. It answers task_status for work the in-memory manager has forgotten —
// which, after a restart, is all of it.
//
// The newest terminal row wins: reconciliation appends rather than edits, so
// a run has at most one, but reading the latest keeps that an invariant of
// the data rather than an assumption of this query.
func (s *Store) FindRun(ctx context.Context, runID string) (*ExecEvent, error) {
	if runID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+execColumns+`
		FROM exec_events
		WHERE run_id = ? AND outcome <> ?
		ORDER BY id DESC
		LIMIT 1`, runID, OutcomeStarted)
	if err != nil {
		return nil, fmt.Errorf("finding run %s: %w", runID, err)
	}
	found, err := scanExecEvents(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return found[0], nil
}
