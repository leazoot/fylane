package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Workspace modes.
const (
	ModeReadOnly  = "read_only"
	ModeReadWrite = "read_write"
)

// Workspace is the persisted form of a sandboxed directory.
//
// RootPath is local-only data: the `json:"-"` tag makes any accidental JSON
// serialization of this record (MCP responses travel as JSON) drop it, and
// TestWorkspaceRootPathNeverSerialized enforces that.
type Workspace struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	RootPath       string    `json:"-"`
	Mode           string    `json:"mode"`
	ExcludeRules   []string  `json:"exclude_rules"`
	SensitiveRules []string  `json:"sensitive_rules"`
	CreatedAt      time.Time `json:"created_at"`
	LastUsedAt     time.Time `json:"last_used_at,omitzero"`
	Status         string    `json:"status"`
	// Network is NetworkAllow or NetworkDeny: whether the programs started
	// for this workspace may reach the network. It is what the user asked
	// for, not what the machine can deliver — the kernel's own answer is
	// reported separately, because they differ on Linux.
	Network string `json:"network"`
}

// What a workspace asks for about outbound traffic. Allow is the default and
// what a NULL column reads as; see migration 0007 for why the permissive
// value is the default one.
const (
	NetworkAllow = "allow"
	NetworkDeny  = "deny"
)

func (w *Workspace) validate() error {
	switch {
	case w.ID == "":
		return fmt.Errorf("workspace: missing id")
	case w.Name == "":
		return fmt.Errorf("workspace %s: missing name", w.ID)
	case w.RootPath == "":
		return fmt.Errorf("workspace %s: missing root path", w.ID)
	case w.Mode != ModeReadOnly && w.Mode != ModeReadWrite:
		return fmt.Errorf("workspace %s: invalid mode %q", w.ID, w.Mode)
	case w.Status == "":
		return fmt.Errorf("workspace %s: missing status", w.ID)
	case w.Network != "" && w.Network != NetworkAllow && w.Network != NetworkDeny:
		return fmt.Errorf("workspace %s: invalid network setting %q", w.ID, w.Network)
	}
	return nil
}

// CreateWorkspace inserts a new workspace. CreatedAt is set to now when zero.
func (s *Store) CreateWorkspace(ctx context.Context, w *Workspace) error {
	if err := w.validate(); err != nil {
		return err
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	exclude, sensitive, err := marshalRules(w)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workspaces
			(id, name, root_path, mode, exclude_rules, sensitive_rules, created_at, last_used_at, status, network)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.RootPath, w.Mode, exclude, sensitive,
		formatTime(w.CreatedAt), formatNullableTime(w.LastUsedAt), w.Status, networkOf(w.Network))
	if err != nil {
		return fmt.Errorf("creating workspace %s: %w", w.ID, err)
	}
	return nil
}

// UpdateWorkspace replaces all mutable fields of the workspace row.
func (s *Store) UpdateWorkspace(ctx context.Context, w *Workspace) error {
	if err := w.validate(); err != nil {
		return err
	}
	exclude, sensitive, err := marshalRules(w)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE workspaces
		SET name = ?, root_path = ?, mode = ?, exclude_rules = ?, sensitive_rules = ?,
			last_used_at = ?, status = ?, network = ?
		WHERE id = ?`,
		w.Name, w.RootPath, w.Mode, exclude, sensitive,
		formatNullableTime(w.LastUsedAt), w.Status, networkOf(w.Network), w.ID)
	if err != nil {
		return fmt.Errorf("updating workspace %s: %w", w.ID, err)
	}
	return requireRow(res, "workspace", w.ID)
}

// TouchWorkspace updates last_used_at.
func (s *Store) TouchWorkspace(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE workspaces SET last_used_at = ? WHERE id = ?`, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("touching workspace %s: %w", id, err)
	}
	return requireRow(res, "workspace", id)
}

// GetWorkspace returns the workspace with the given ID, or ErrNotFound.
func (s *Store) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, root_path, mode, exclude_rules, sensitive_rules, created_at, last_used_at, status, network
		FROM workspaces WHERE id = ?`, id)
	w, err := scanWorkspace(row)
	if err != nil {
		return nil, fmt.Errorf("getting workspace %s: %w", id, err)
	}
	return w, nil
}

// ListWorkspaces returns all workspaces ordered by creation time.
func (s *Store) ListWorkspaces(ctx context.Context) ([]*Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, root_path, mode, exclude_rules, sensitive_rules, created_at, last_used_at, status, network
		FROM workspaces ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	defer rows.Close()
	var out []*Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("listing workspaces: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	return out, nil
}

// DeleteWorkspace removes the workspace row. Change sets referencing it must
// be removed first (enforced by the foreign key).
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting workspace %s: %w", id, err)
	}
	return requireRow(res, "workspace", id)
}

func marshalRules(w *Workspace) (exclude, sensitive string, err error) {
	e, err := marshalStrings(w.ExcludeRules)
	if err != nil {
		return "", "", fmt.Errorf("workspace %s: encoding exclude rules: %w", w.ID, err)
	}
	s, err := marshalStrings(w.SensitiveRules)
	if err != nil {
		return "", "", fmt.Errorf("workspace %s: encoding sensitive rules: %w", w.ID, err)
	}
	return e, s, nil
}

func scanWorkspace(row interface{ Scan(...any) error }) (*Workspace, error) {
	var (
		w                  Workspace
		exclude, sensitive string
		created            string
		lastUsed           sql.NullString
		network            sql.NullString
	)
	err := row.Scan(&w.ID, &w.Name, &w.RootPath, &w.Mode, &exclude, &sensitive,
		&created, &lastUsed, &w.Status, &network)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Rows written before this column existed read as allow, which is what
	// they were running under. Normalising here means no caller ever has to
	// treat "" as a third answer.
	w.Network = networkOf(network.String)
	if err := json.Unmarshal([]byte(exclude), &w.ExcludeRules); err != nil {
		return nil, fmt.Errorf("decoding exclude rules: %w", err)
	}
	if err := json.Unmarshal([]byte(sensitive), &w.SensitiveRules); err != nil {
		return nil, fmt.Errorf("decoding sensitive rules: %w", err)
	}
	if w.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if w.LastUsedAt, err = parseNullableTime(lastUsed); err != nil {
		return nil, err
	}
	return &w, nil
}

func marshalStrings(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func requireRow(res sql.Result, kind, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %s: %w", kind, id, err)
	}
	if n == 0 {
		return fmt.Errorf("%s %s: %w", kind, id, ErrNotFound)
	}
	return nil
}

// networkOf normalises a stored or supplied value. Anything that is not an
// explicit deny is an allow: this is the one place that decision is made, so
// a new caller cannot introduce a third meaning by leaving the field empty.
func networkOf(v string) string {
	if v == NetworkDeny {
		return NetworkDeny
	}
	return NetworkAllow
}
