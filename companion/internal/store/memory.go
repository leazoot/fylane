package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Memory bounds. The tools cut what they are given to these before it
// arrives; the store refuses anything larger so that no other path can
// grow a page past what a conversation is meant to start from.
const (
	MemoryGoalBytes     = 300
	MemoryProgressBytes = 800
	MemoryNextBytes     = 800
	MemoryListItems     = 8
	MemoryListItemBytes = 200
	MemoryTitleBytes    = 120
	MemoryBodyBytes     = 2000
	MemoryRefBytes      = 64
)

// MemoryPage is the one page a workspace's memory keeps current. Every
// field is replaced whole on each save.
type MemoryPage struct {
	Goal      string   `json:"goal,omitempty" jsonschema:"What the work in this workspace is for."`
	Progress  string   `json:"progress,omitempty" jsonschema:"Where it stands now."`
	Next      string   `json:"next,omitempty" jsonschema:"What should happen next."`
	Decisions []string `json:"decisions,omitempty" jsonschema:"Decisions taken that a later conversation must not reopen."`
	Open      []string `json:"open,omitempty" jsonschema:"Questions still unresolved."`
}

// MemoryState is the page with its bookkeeping.
type MemoryState struct {
	WorkspaceID string     `json:"workspace_id"`
	Page        MemoryPage `json:"page"`
	Provider    string     `json:"provider,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// MemoryNote is one entry of the trail.
type MemoryNote struct {
	ID          int64     `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Provider    string    `json:"provider,omitempty"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	ChangeSetID string    `json:"change_set_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	Archived    bool      `json:"archived,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (p *MemoryPage) validate() error {
	if err := within("goal", p.Goal, MemoryGoalBytes); err != nil {
		return err
	}
	if err := within("progress", p.Progress, MemoryProgressBytes); err != nil {
		return err
	}
	if err := within("next", p.Next, MemoryNextBytes); err != nil {
		return err
	}
	for name, list := range map[string][]string{"decisions": p.Decisions, "open": p.Open} {
		if len(list) > MemoryListItems {
			return fmt.Errorf("memory page: %s has %d items; the limit is %d", name, len(list), MemoryListItems)
		}
		for _, item := range list {
			if err := within(name+" item", item, MemoryListItemBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func within(name, v string, limit int) error {
	switch {
	case len(v) > limit:
		return fmt.Errorf("memory: %s is %d bytes; the limit is %d", name, len(v), limit)
	case !utf8.ValidString(v):
		return fmt.Errorf("memory: %s is not valid UTF-8", name)
	}
	return nil
}

// SaveMemoryState replaces the workspace's page.
func (s *Store) SaveMemoryState(ctx context.Context, workspaceID, provider string, page MemoryPage, at time.Time) error {
	if workspaceID == "" {
		return fmt.Errorf("memory page: missing workspace id")
	}
	if err := page.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return fmt.Errorf("encoding memory page: %w", err)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_state (workspace_id, page, provider, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (workspace_id) DO UPDATE SET page = excluded.page, provider = excluded.provider, updated_at = excluded.updated_at`,
		workspaceID, string(raw), provider, formatTime(at))
	if err != nil {
		return fmt.Errorf("saving memory page: %w", err)
	}
	return nil
}

// GetMemoryState reads the page; ErrNotFound when none was ever saved.
func (s *Store) GetMemoryState(ctx context.Context, workspaceID string) (*MemoryState, error) {
	var (
		st      MemoryState
		raw     string
		updated string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT workspace_id, page, provider, updated_at FROM memory_state WHERE workspace_id = ?`,
		workspaceID).Scan(&st.WorkspaceID, &raw, &st.Provider, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading memory page: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &st.Page); err != nil {
		return nil, fmt.Errorf("decoding memory page: %w", err)
	}
	if st.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, fmt.Errorf("decoding memory page timestamp: %w", err)
	}
	return &st, nil
}

// AddMemoryNote appends to the trail. CreatedAt is set to now when zero;
// the row id is written back.
func (s *Store) AddMemoryNote(ctx context.Context, n *MemoryNote) error {
	switch {
	case n.WorkspaceID == "":
		return fmt.Errorf("memory note: missing workspace id")
	case strings.TrimSpace(n.Title) == "":
		return fmt.Errorf("memory note: missing title")
	}
	for _, f := range []struct {
		name  string
		v     string
		limit int
	}{{"title", n.Title, MemoryTitleBytes}, {"body", n.Body, MemoryBodyBytes},
		{"change_set_id", n.ChangeSetID, MemoryRefBytes}, {"run_id", n.RunID, MemoryRefBytes}} {
		if err := within(f.name, f.v, f.limit); err != nil {
			return err
		}
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO memory_notes (workspace_id, provider, title, body, change_set_id, run_id, archived, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
		n.WorkspaceID, n.Provider, n.Title, n.Body, n.ChangeSetID, n.RunID, formatTime(n.CreatedAt))
	if err != nil {
		return fmt.Errorf("adding memory note: %w", err)
	}
	if n.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("adding memory note: %w", err)
	}
	return nil
}

const memoryNoteColumns = `id, workspace_id, provider, title, body, change_set_id, run_id, archived, created_at`

// ListMemoryNotes pages the trail newest first. beforeID > 0 continues
// past a previous page; archived notes are left out.
func (s *Store) ListMemoryNotes(ctx context.Context, workspaceID string, beforeID int64, limit int) ([]*MemoryNote, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + memoryNoteColumns + ` FROM memory_notes WHERE workspace_id = ? AND archived = 0`
	args := []any{workspaceID}
	if beforeID > 0 {
		query += ` AND id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing memory notes: %w", err)
	}
	return scanMemoryNotes(rows)
}

// GetMemoryNotes reads notes by id, newest first. Ids of another
// workspace are not returned: a note is addressed within its workspace.
func (s *Store) GetMemoryNotes(ctx context.Context, workspaceID string, ids []int64) ([]*MemoryNote, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := strings.Repeat("?,", len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, workspaceID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+memoryNoteColumns+` FROM memory_notes
		WHERE workspace_id = ? AND id IN (`+marks[:len(marks)-1]+`) ORDER BY id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading memory notes: %w", err)
	}
	return scanMemoryNotes(rows)
}

// SearchMemoryNotes finds notes whose title or body contains the words,
// newest first. Every word must appear; case is ignored for ASCII, as
// SQLite's LIKE does. Archived notes are searched too — search is how a
// compacted note is found again.
func (s *Store) SearchMemoryNotes(ctx context.Context, workspaceID, query string, limit int) ([]*MemoryNote, error) {
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, fmt.Errorf("memory search: empty query")
	}
	if limit <= 0 {
		limit = 10
	}
	sqlq := `SELECT ` + memoryNoteColumns + ` FROM memory_notes WHERE workspace_id = ?`
	args := []any{workspaceID}
	for _, w := range words {
		pattern := "%" + likeEscape(w) + "%"
		sqlq += ` AND (title LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern)
	}
	sqlq += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, fmt.Errorf("searching memory notes: %w", err)
	}
	return scanMemoryNotes(rows)
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// CountMemoryNotes says how many notes the trail holds, live and archived.
func (s *Store) CountMemoryNotes(ctx context.Context, workspaceID string) (live, archived int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(archived = 0), 0), COALESCE(SUM(archived = 1), 0) FROM memory_notes WHERE workspace_id = ?`,
		workspaceID).Scan(&live, &archived)
	if err != nil {
		return 0, 0, fmt.Errorf("counting memory notes: %w", err)
	}
	return live, archived, nil
}

// DeleteMemory forgets everything remembered about a workspace.
func (s *Store) DeleteMemory(ctx context.Context, workspaceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("deleting memory: %w", err)
	}
	defer tx.Rollback()
	for _, table := range []string{"memory_notes", "memory_state"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE workspace_id = ?`, workspaceID); err != nil {
			return fmt.Errorf("deleting memory: %w", err)
		}
	}
	return tx.Commit()
}

func scanMemoryNotes(rows *sql.Rows) ([]*MemoryNote, error) {
	defer rows.Close()
	var out []*MemoryNote
	for rows.Next() {
		var (
			n        MemoryNote
			archived int
			created  string
		)
		if err := rows.Scan(&n.ID, &n.WorkspaceID, &n.Provider, &n.Title, &n.Body, &n.ChangeSetID, &n.RunID, &archived, &created); err != nil {
			return nil, fmt.Errorf("scanning memory note: %w", err)
		}
		n.Archived = archived != 0
		var err error
		if n.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("decoding memory note %d timestamp: %w", n.ID, err)
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

// OldestMemoryNotes lists the live trail oldest first, for compaction.
func (s *Store) OldestMemoryNotes(ctx context.Context, workspaceID string, limit int) ([]*MemoryNote, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+memoryNoteColumns+` FROM memory_notes
		WHERE workspace_id = ? AND archived = 0 ORDER BY id ASC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing memory notes: %w", err)
	}
	return scanMemoryNotes(rows)
}

// ArchiveMemoryNotes marks every live note up to and including throughID
// as folded into a summary. They stay readable by id and searchable.
func (s *Store) ArchiveMemoryNotes(ctx context.Context, workspaceID string, throughID int64) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE memory_notes SET archived = 1
		WHERE workspace_id = ? AND archived = 0 AND id <= ?`, workspaceID, throughID)
	if err != nil {
		return 0, fmt.Errorf("archiving memory notes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("archiving memory notes: %w", err)
	}
	return int(n), nil
}
