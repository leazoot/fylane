package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
)

// Workspace statuses (pause all external access, revoke
// authorization). Revocation is soft — the row is kept so change-set history
// and audit events stay intact — but a revoked workspace can never be opened.
const (
	StatusActive  = "active"
	StatusPaused  = "paused"
	StatusRevoked = "revoked"
)

var (
	// ErrPaused is returned when opening a workspace whose external access
	// is paused.
	ErrPaused = errors.New("workspace access is paused")
	// ErrRevoked is returned when opening a workspace whose authorization
	// was revoked.
	ErrRevoked = errors.New("workspace authorization was revoked")
	// ErrNoCurrent is returned when no current workspace is selected.
	ErrNoCurrent = errors.New("no current workspace selected")
	// ErrDuplicateRoot is returned when adding a root that is already
	// registered as a non-revoked workspace.
	ErrDuplicateRoot = errors.New("directory is already registered as a workspace")
)

// Manager owns workspace records and the single "current workspace"
// selection. The selection lives in a small state file next to the
// database, not in tool-call state: MCP tools always address workspaces by
// explicit workspace_id.
type Manager struct {
	store     *store.Store
	statePath string

	mu sync.Mutex // guards the state file
}

// NewManager returns a Manager persisting the current-workspace selection in
// stateDir (created if missing).
func NewManager(st *store.Store, stateDir string) (*Manager, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}
	return &Manager{store: st, statePath: filepath.Join(stateDir, "state.json")}, nil
}

// Add registers root as a new read-write workspace with the default exclude
// and sensitive rules and returns its record. The root must be an existing
// directory; registering the same real path twice is rejected.
func (m *Manager) Add(ctx context.Context, root string) (*store.Workspace, error) {
	real, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	existing, err := m.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range existing {
		if w.RootPath == real && w.Status != StatusRevoked {
			return nil, fmt.Errorf("%w (%s)", ErrDuplicateRoot, w.ID)
		}
	}
	rec := &store.Workspace{
		ID:             store.NewID("ws"),
		Name:           filepath.Base(real),
		RootPath:       real,
		Mode:           store.ModeReadWrite,
		ExcludeRules:   DefaultExcludeRules(),
		SensitiveRules: DefaultSensitiveRules(),
		Status:         StatusActive,
	}
	if err := m.store.CreateWorkspace(ctx, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// List returns all non-revoked workspaces.
func (m *Manager) List(ctx context.Context) ([]*store.Workspace, error) {
	all, err := m.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, w := range all {
		if w.Status != StatusRevoked {
			out = append(out, w)
		}
	}
	return out, nil
}

// Get returns the workspace record with the given ID.
func (m *Manager) Get(ctx context.Context, id string) (*store.Workspace, error) {
	return m.store.GetWorkspace(ctx, id)
}

// Open returns a runtime handle for tool use. Only active workspaces can be
// opened; paused and revoked workspaces fail with ErrPaused / ErrRevoked.
// Opening records the workspace as used.
func (m *Manager) Open(ctx context.Context, id string) (*Workspace, error) {
	rec, err := m.store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}
	switch rec.Status {
	case StatusPaused:
		return nil, fmt.Errorf("workspace %s: %w", id, ErrPaused)
	case StatusRevoked:
		return nil, fmt.Errorf("workspace %s: %w", id, ErrRevoked)
	}
	ws, err := FromRecord(rec)
	if err != nil {
		return nil, err
	}
	if err := m.store.TouchWorkspace(ctx, id, time.Now()); err != nil {
		return nil, err
	}
	return ws, nil
}

// Pause suspends all external access to the workspace.
func (m *Manager) Pause(ctx context.Context, id string) error {
	return m.setStatus(ctx, id, StatusActive, StatusPaused)
}

// Resume restores external access to a paused workspace.
func (m *Manager) Resume(ctx context.Context, id string) error {
	return m.setStatus(ctx, id, StatusPaused, StatusActive)
}

// Revoke permanently withdraws the workspace's authorization. The record is
// kept for history; if it was the current workspace the selection is cleared.
func (m *Manager) Revoke(ctx context.Context, id string) error {
	rec, err := m.store.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	rec.Status = StatusRevoked
	if err := m.store.UpdateWorkspace(ctx, rec); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.readState()
	if err != nil {
		return err
	}
	if current == id {
		return m.writeState("")
	}
	return nil
}

func (m *Manager) setStatus(ctx context.Context, id, from, to string) error {
	rec, err := m.store.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if rec.Status == StatusRevoked {
		return fmt.Errorf("workspace %s: %w", id, ErrRevoked)
	}
	if rec.Status != from {
		return fmt.Errorf("workspace %s: status is %q, expected %q", id, rec.Status, from)
	}
	rec.Status = to
	return m.store.UpdateWorkspace(ctx, rec)
}

// SetNetwork records whether the programs started for this workspace may
// reach the network. A revoked workspace refuses the change for the same
// reason it refuses every other: its authorization is gone, and settings on
// a folder Fylane may not touch would be settings nothing reads.
func (m *Manager) SetNetwork(ctx context.Context, id string, allow bool) error {
	rec, err := m.store.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if rec.Status == StatusRevoked {
		return fmt.Errorf("workspace %s: %w", id, ErrRevoked)
	}
	rec.Network = store.NetworkAllow
	if !allow {
		rec.Network = store.NetworkDeny
	}
	return m.store.UpdateWorkspace(ctx, rec)
}

// SetCurrent selects the single current workspace. Revoked workspaces cannot
// be selected.
func (m *Manager) SetCurrent(ctx context.Context, id string) error {
	rec, err := m.store.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if rec.Status == StatusRevoked {
		return fmt.Errorf("workspace %s: %w", id, ErrRevoked)
	}
	if err := m.store.TouchWorkspace(ctx, id, time.Now()); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeState(id)
}

// Current returns the current workspace record, or ErrNoCurrent when none is
// selected or the selection no longer resolves to a usable workspace.
func (m *Manager) Current(ctx context.Context) (*store.Workspace, error) {
	m.mu.Lock()
	id, err := m.readState()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, ErrNoCurrent
	}
	rec, err := m.store.GetWorkspace(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoCurrent
	}
	if err != nil {
		return nil, err
	}
	if rec.Status == StatusRevoked {
		return nil, ErrNoCurrent
	}
	return rec, nil
}

type managerState struct {
	CurrentWorkspaceID string `json:"current_workspace_id"`
}

func (m *Manager) readState() (string, error) {
	b, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading workspace state: %w", err)
	}
	var st managerState
	if err := json.Unmarshal(b, &st); err != nil {
		return "", fmt.Errorf("decoding workspace state: %w", err)
	}
	return st.CurrentWorkspaceID, nil
}

// writeState persists the selection atomically (temp file + rename) so a
// crash can never leave a truncated state file.
func (m *Manager) writeState(id string) error {
	b, err := json.Marshal(managerState{CurrentWorkspaceID: id})
	if err != nil {
		return fmt.Errorf("encoding workspace state: %w", err)
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("writing workspace state: %w", err)
	}
	if err := os.Rename(tmp, m.statePath); err != nil {
		return fmt.Errorf("replacing workspace state: %w", err)
	}
	return nil
}
