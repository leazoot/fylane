package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "fylane.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := NewManager(st, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, st, dir
}

func TestManagerAdd(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	root := t.TempDir()

	rec, err := m.Add(ctx, root)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(rec.ID, "ws_") {
		t.Fatalf("ID = %q, want ws_ prefix", rec.ID)
	}
	if strings.Contains(rec.ID, root) || strings.Contains(rec.ID, filepath.Base(root)) {
		t.Fatalf("ID %q derives from root path, must be opaque", rec.ID)
	}
	if rec.Mode != store.ModeReadWrite || rec.Status != StatusActive {
		t.Fatalf("record = %+v", rec)
	}
	if len(rec.ExcludeRules) == 0 || len(rec.SensitiveRules) == 0 {
		t.Fatal("default rules not applied")
	}

	if _, err := m.Add(ctx, root); !errors.Is(err, ErrDuplicateRoot) {
		t.Fatalf("duplicate Add: err = %v, want ErrDuplicateRoot", err)
	}
	if _, err := m.Add(ctx, filepath.Join(root, "missing")); err == nil {
		t.Fatal("Add with nonexistent root succeeded")
	}
}

// The ID must be stable across restarts: it is persisted, not re-derived.
func TestManagerIDStableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := t.TempDir()
	dbPath := filepath.Join(dir, "fylane.db")

	st1, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	m1, err := NewManager(st1, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	rec, err := m1.Add(ctx, root)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := m1.SetCurrent(ctx, rec.ID); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}
	st1.Close()

	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer st2.Close()
	m2, err := NewManager(st2, dir)
	if err != nil {
		t.Fatalf("NewManager reopen: %v", err)
	}
	got, err := m2.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.ID != rec.ID || got.RootPath != rec.RootPath {
		t.Fatalf("reopened record = %+v, want %+v", got, rec)
	}
	current, err := m2.Current(ctx)
	if err != nil {
		t.Fatalf("Current after reopen: %v", err)
	}
	if current.ID != rec.ID {
		t.Fatalf("Current = %s, want %s", current.ID, rec.ID)
	}
}

func TestManagerSingleCurrent(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)

	if _, err := m.Current(ctx); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("Current with none selected: err = %v, want ErrNoCurrent", err)
	}

	a, err := m.Add(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Add a: %v", err)
	}
	b, err := m.Add(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Add b: %v", err)
	}

	if err := m.SetCurrent(ctx, a.ID); err != nil {
		t.Fatalf("SetCurrent a: %v", err)
	}
	if err := m.SetCurrent(ctx, b.ID); err != nil {
		t.Fatalf("SetCurrent b: %v", err)
	}
	current, err := m.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.ID != b.ID {
		t.Fatalf("Current = %s, want %s (only one current workspace)", current.ID, b.ID)
	}
	if current.LastUsedAt.IsZero() {
		t.Fatal("SetCurrent did not update last_used_at")
	}

	if err := m.SetCurrent(ctx, "ws_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetCurrent missing: err = %v, want ErrNotFound", err)
	}
}

func TestManagerPauseResume(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	rec, err := m.Add(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := m.Open(ctx, rec.ID); err != nil {
		t.Fatalf("Open active: %v", err)
	}

	if err := m.Pause(ctx, rec.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := m.Open(ctx, rec.ID); !errors.Is(err, ErrPaused) {
		t.Fatalf("Open paused: err = %v, want ErrPaused", err)
	}
	if err := m.Pause(ctx, rec.ID); err == nil {
		t.Fatal("double Pause succeeded")
	}

	if err := m.Resume(ctx, rec.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	ws, err := m.Open(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Open resumed: %v", err)
	}
	if ws.ID() != rec.ID {
		t.Fatalf("handle ID = %s, want %s", ws.ID(), rec.ID)
	}
}

func TestManagerRevoke(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	rec, err := m.Add(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := m.SetCurrent(ctx, rec.ID); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}

	if err := m.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := m.Open(ctx, rec.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Open revoked: err = %v, want ErrRevoked", err)
	}
	if _, err := m.Current(ctx); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("Current after revoke: err = %v, want ErrNoCurrent", err)
	}
	if err := m.SetCurrent(ctx, rec.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("SetCurrent revoked: err = %v, want ErrRevoked", err)
	}
	if err := m.Resume(ctx, rec.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Resume revoked: err = %v, want ErrRevoked", err)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List returned %d workspaces after revoke, want 0", len(list))
	}
	// History row is kept (soft revoke).
	got, err := m.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get revoked: %v", err)
	}
	if got.Status != StatusRevoked {
		t.Fatalf("Status = %q, want revoked", got.Status)
	}

	// The same directory can be registered again after revocation.
	again, err := m.Add(ctx, rec.RootPath)
	if err != nil {
		t.Fatalf("Add after revoke: %v", err)
	}
	if again.ID == rec.ID {
		t.Fatal("re-added workspace reused the revoked ID")
	}
}

func TestManagerOpenAppliesStoredRules(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newTestManager(t)
	rec, err := m.Add(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	rec.ExcludeRules = []string{"private/"}
	rec.SensitiveRules = []string{"*.token"}
	if err := st.UpdateWorkspace(ctx, rec); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}

	ws, err := m.Open(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !ws.Excluded("private/notes.md", false) || ws.Excluded("src/main.go", false) {
		t.Fatal("stored exclude rules not applied to runtime handle")
	}
	if !ws.Sensitive("api.token") || ws.Sensitive(".env") {
		t.Fatal("stored sensitive rules not applied to runtime handle")
	}
}

func TestSetNetworkIsRememberedAndRefusedOnARevokedWorkspace(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	rec, err := m.Add(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The default is what every workspace ran under before this existed.
	ws, err := m.Open(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Network() {
		t.Error("a new workspace denies the network before anyone asked for that")
	}

	if err := m.SetNetwork(ctx, rec.ID, false); err != nil {
		t.Fatal(err)
	}
	if ws, err = m.Open(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if ws.Network() {
		t.Error("the workspace still allows the network after being told not to")
	}

	// A revoked workspace refuses, for the same reason it refuses everything
	// else: its authorization is gone, so a setting on it would be a setting
	// nothing reads.
	if err := m.Revoke(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.SetNetwork(ctx, rec.ID, true); err == nil {
		t.Error("a revoked workspace accepted a network setting")
	}
}
