package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "fylane.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testWorkspace(id string) *Workspace {
	return &Workspace{
		ID:             id,
		Name:           "demo",
		RootPath:       "/home/user/demo",
		Mode:           ModeReadWrite,
		ExcludeRules:   []string{"node_modules/**", ".git/**"},
		SensitiveRules: []string{".env*", "*.pem"},
		Status:         "active",
	}
}

func TestMigrationsApplyOnceAndAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fylane.db")

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// Counted from the embedded files rather than written as a literal, so
	// adding a migration does not mean editing this assertion — and so the
	// test keeps meaning "every migration applied" instead of "some did".
	want, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s1.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("reading version: %v", err)
	}
	if version != len(want) {
		t.Fatalf("schema version = %d, want %d", version, len(want))
	}
	s1.Close()

	// Reopening must not re-apply anything (CREATE TABLE would fail).
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	var count int
	if err := s2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("counting migrations: %v", err)
	}
	if count != len(want) {
		t.Fatalf("applied migrations = %d, want %d", count, len(want))
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fylane.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 'x')`); err != nil {
		t.Fatalf("inserting future version: %v", err)
	}
	s.Close()

	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("Open with future schema version: err = %v, want newer-than-build error", err)
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	w := testWorkspace("ws_0000000000000001")
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if w.CreatedAt.IsZero() {
		t.Fatal("CreateWorkspace did not set CreatedAt")
	}
	if err := s.CreateWorkspace(ctx, w); err == nil {
		t.Fatal("duplicate CreateWorkspace succeeded, want error")
	}

	got, err := s.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.Name != w.Name || got.RootPath != w.RootPath || got.Mode != w.Mode || got.Status != w.Status {
		t.Fatalf("GetWorkspace = %+v, want %+v", got, w)
	}
	if len(got.ExcludeRules) != 2 || got.ExcludeRules[0] != "node_modules/**" {
		t.Fatalf("ExcludeRules = %v", got.ExcludeRules)
	}
	if len(got.SensitiveRules) != 2 || got.SensitiveRules[0] != ".env*" {
		t.Fatalf("SensitiveRules = %v", got.SensitiveRules)
	}
	if !got.LastUsedAt.IsZero() {
		t.Fatalf("LastUsedAt = %v, want zero", got.LastUsedAt)
	}

	got.Mode = ModeReadOnly
	got.Status = "paused"
	got.ExcludeRules = append(got.ExcludeRules, "dist/**")
	if err := s.UpdateWorkspace(ctx, got); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	used := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if err := s.TouchWorkspace(ctx, w.ID, used); err != nil {
		t.Fatalf("TouchWorkspace: %v", err)
	}

	got2, err := s.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after update: %v", err)
	}
	if got2.Mode != ModeReadOnly || got2.Status != "paused" || len(got2.ExcludeRules) != 3 {
		t.Fatalf("updated workspace = %+v", got2)
	}
	if !got2.LastUsedAt.Equal(used) {
		t.Fatalf("LastUsedAt = %v, want %v", got2.LastUsedAt, used)
	}

	if err := s.CreateWorkspace(ctx, testWorkspace("ws_0000000000000002")); err != nil {
		t.Fatalf("CreateWorkspace second: %v", err)
	}
	all, err := s.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListWorkspaces returned %d, want 2", len(all))
	}

	if err := s.DeleteWorkspace(ctx, "ws_0000000000000002"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := s.GetWorkspace(ctx, "ws_0000000000000002"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWorkspace deleted: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteWorkspace(ctx, "ws_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteWorkspace missing: err = %v, want ErrNotFound", err)
	}
}

func TestWorkspaceValidation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	w := testWorkspace("ws_0000000000000003")
	w.Mode = "read-write" // wrong spelling must be rejected
	if err := s.CreateWorkspace(ctx, w); err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("CreateWorkspace with bad mode: err = %v", err)
	}
}

// Workspace records travel as JSON inside Companion (and one day over MCP
// responses by mistake); the type itself must make root_path unleakable.
func TestWorkspaceRootPathNeverSerialized(t *testing.T) {
	w := testWorkspace("ws_0000000000000004")
	w.CreatedAt = time.Now()
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "root_path") || strings.Contains(string(b), w.RootPath) {
		t.Fatalf("workspace JSON leaks root path: %s", b)
	}
}

func TestConnectorUpsertAndGet(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	c := &Connector{
		Provider:          "claude",
		RemoteConnectorID: "conn-1",
		Status:            "connected",
		Capabilities:      []string{"read", "write"},
		TokenReference:    "keychain:fylane/claude/conn-1",
	}
	if err := s.UpsertConnector(ctx, c); err != nil {
		t.Fatalf("UpsertConnector: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("UpsertConnector did not set ID")
	}

	c2 := &Connector{
		Provider:          "claude",
		RemoteConnectorID: "conn-1",
		Status:            "disconnected",
		Capabilities:      []string{"read"},
		LastConnectedAt:   time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
	}
	if err := s.UpsertConnector(ctx, c2); err != nil {
		t.Fatalf("UpsertConnector update: %v", err)
	}
	if c2.ID != c.ID {
		t.Fatalf("upsert created new row: id %d != %d", c2.ID, c.ID)
	}

	got, err := s.GetConnector(ctx, "claude", "conn-1")
	if err != nil {
		t.Fatalf("GetConnector: %v", err)
	}
	if got.Status != "disconnected" || len(got.Capabilities) != 1 {
		t.Fatalf("GetConnector = %+v", got)
	}
	if !got.LastConnectedAt.Equal(c2.LastConnectedAt) {
		t.Fatalf("LastConnectedAt = %v", got.LastConnectedAt)
	}

	if _, err := s.GetConnector(ctx, "grok", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetConnector missing: err = %v, want ErrNotFound", err)
	}

	all, err := s.ListConnectors(ctx)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListConnectors returned %d, want 1", len(all))
	}
	if err := s.DeleteConnector(ctx, c.ID); err != nil {
		t.Fatalf("DeleteConnector: %v", err)
	}
}

// Connector JSON must not leak the token reference either — it is a
// local-only lookup key.
func TestConnectorTokenReferenceNeverSerialized(t *testing.T) {
	c := &Connector{Provider: "claude", RemoteConnectorID: "conn-1",
		Status: "connected", TokenReference: "keychain:fylane/claude/conn-1"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "token") || strings.Contains(string(b), "keychain") {
		t.Fatalf("connector JSON leaks token reference: %s", b)
	}
}

func TestChangeSetLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	w := testWorkspace("ws_0000000000000005")
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	ops := json.RawMessage(`[{"type":"update","path":"src/a.ts","expected_sha256":"aa","content":"x"}]`)
	c := &ChangeSet{
		ID:           "chg_0000000000000001",
		WorkspaceID:  w.ID,
		Provider:     "claude",
		Summary:      "Update a.ts",
		Operations:   ops,
		Status:       ChangeSetPending,
		BeforeHashes: map[string]string{"src/a.ts": "aa"},
	}
	if err := s.CreateChangeSet(ctx, c); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}

	// Foreign key: change sets cannot reference unknown workspaces.
	bad := &ChangeSet{ID: "chg_bad", WorkspaceID: "ws_missing", Operations: ops, Status: ChangeSetPending}
	if err := s.CreateChangeSet(ctx, bad); err == nil {
		t.Fatal("CreateChangeSet with unknown workspace succeeded, want FK error")
	}

	c.Status = ChangeSetApplied
	c.AfterHashes = map[string]string{"src/a.ts": "bb"}
	c.BackupLocation = "backups/chg_0000000000000001"
	c.ApprovedAt = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	c.AppliedAt = c.ApprovedAt.Add(2 * time.Second)
	c.RollbackDeadline = c.AppliedAt.Add(7 * 24 * time.Hour)
	if err := s.UpdateChangeSet(ctx, c); err != nil {
		t.Fatalf("UpdateChangeSet: %v", err)
	}

	got, err := s.GetChangeSet(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChangeSet: %v", err)
	}
	if got.Status != ChangeSetApplied || got.AfterHashes["src/a.ts"] != "bb" {
		t.Fatalf("GetChangeSet = %+v", got)
	}
	if string(got.Operations) != string(ops) {
		t.Fatalf("Operations round-trip = %s", got.Operations)
	}
	if !got.RollbackDeadline.Equal(c.RollbackDeadline) {
		t.Fatalf("RollbackDeadline = %v", got.RollbackDeadline)
	}

	later := &ChangeSet{
		ID: "chg_0000000000000002", WorkspaceID: w.ID, Provider: "grok",
		Summary: "Second", Operations: ops, Status: ChangeSetPending,
		CreatedAt: c.CreatedAt.Add(time.Minute),
	}
	if err := s.CreateChangeSet(ctx, later); err != nil {
		t.Fatalf("CreateChangeSet second: %v", err)
	}
	list, err := s.ListChangeSets(ctx, w.ID, 0)
	if err != nil {
		t.Fatalf("ListChangeSets: %v", err)
	}
	if len(list) != 2 || list[0].ID != later.ID {
		t.Fatalf("ListChangeSets order = %v", []string{list[0].ID, list[1].ID})
	}
	limited, err := s.ListChangeSets(ctx, w.ID, 1)
	if err != nil {
		t.Fatalf("ListChangeSets limited: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("ListChangeSets limit=1 returned %d", len(limited))
	}
}

// ChangeSet JSON must not carry the backup location — it is an absolute-ish
// local path under the backup area.
func TestChangeSetBackupLocationNeverSerialized(t *testing.T) {
	c := &ChangeSet{ID: "chg_x", WorkspaceID: "ws_x", Status: ChangeSetApplied,
		Operations: json.RawMessage(`[]`), BackupLocation: "/Users/u/.fylane/backups/chg_x"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "backup") || strings.Contains(string(b), "/Users/") {
		t.Fatalf("change set JSON leaks backup location: %s", b)
	}
}

func TestAuditEvents(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	w := testWorkspace("ws_0000000000000006")
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	c := &ChangeSet{ID: "chg_0000000000000003", WorkspaceID: w.ID,
		Operations: json.RawMessage(`[]`), Status: ChangeSetPending}
	if err := s.CreateChangeSet(ctx, c); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}

	events := []*AuditEvent{
		{ChangeSetID: c.ID, EventType: "approval_requested", Result: "ok"},
		{ChangeSetID: c.ID, EventType: "write_applied", PathRelative: "src/a.ts", Result: "ok", DurationMS: 12},
		{EventType: "read_file", PathRelative: "src/b.ts", Result: "ok", DurationMS: 3},
	}
	for _, e := range events {
		if err := s.AppendAuditEvent(ctx, e); err != nil {
			t.Fatalf("AppendAuditEvent(%s): %v", e.EventType, err)
		}
		if e.ID == 0 {
			t.Fatalf("AppendAuditEvent(%s) did not set ID", e.EventType)
		}
	}

	got, err := s.ListAuditEvents(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListAuditEvents returned %d, want 2", len(got))
	}
	if got[0].EventType != "approval_requested" || got[1].PathRelative != "src/a.ts" {
		t.Fatalf("ListAuditEvents = %+v, %+v", got[0], got[1])
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID("ws"), NewID("ws")
	if !strings.HasPrefix(a, "ws_") || len(a) != len("ws_")+16 {
		t.Fatalf("NewID format: %q", a)
	}
	if a == b {
		t.Fatalf("NewID returned duplicate: %q", a)
	}
}

// Acceptance is a column, not a status (migration 0006). These are the four
// facts that shape entails, and each one is a thing the status-value design
// would have got wrong.
func TestChangeSetAcceptance(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	w := testWorkspace("ws_0000000000000009")
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	ops := json.RawMessage(`[{"type":"create","path":"notes/a.md","content":"x"}]`)
	newSet := func(id, status string) *ChangeSet {
		c := &ChangeSet{ID: id, WorkspaceID: w.ID, Provider: "claude",
			Summary: "Add a.md", Operations: ops, Status: status}
		if err := s.CreateChangeSet(ctx, c); err != nil {
			t.Fatalf("CreateChangeSet %s: %v", id, err)
		}
		return c
	}

	// A fresh applied change set has not been accepted. "Nobody has looked at
	// this" is a real value, not a missing one.
	applied := newSet("chg_acc_applied", ChangeSetApplied)
	got, err := s.GetChangeSet(ctx, applied.ID)
	if err != nil {
		t.Fatalf("GetChangeSet: %v", err)
	}
	if !got.AcceptedAt.IsZero() {
		t.Fatalf("a new change set is already accepted at %v", got.AcceptedAt)
	}

	at := time.Date(2026, 9, 1, 14, 2, 0, 0, time.UTC)
	got, err = s.AcceptChangeSet(ctx, applied.ID, at)
	if err != nil {
		t.Fatalf("AcceptChangeSet: %v", err)
	}
	if !got.AcceptedAt.Equal(at) {
		t.Fatalf("AcceptedAt = %v, want %v", got.AcceptedAt, at)
	}
	// The status is untouched: an accepted write is still an applied write,
	// which is what keeps rollback, the lane count and the day's activity
	// reading it the same way they did before.
	if got.Status != ChangeSetApplied {
		t.Fatalf("status = %q after acceptance, want %q", got.Status, ChangeSetApplied)
	}

	// A second acceptance does not move the timestamp. "When did a human look
	// at this" has one answer, and a double click is not two reviews.
	got, err = s.AcceptChangeSet(ctx, applied.ID, at.Add(time.Hour))
	if err != nil {
		t.Fatalf("AcceptChangeSet again: %v", err)
	}
	if !got.AcceptedAt.Equal(at) {
		t.Fatalf("second acceptance moved the timestamp to %v", got.AcceptedAt)
	}

	// Nothing that did not land can be accepted — there is no change to have
	// reviewed.
	for _, status := range []string{ChangeSetPending, ChangeSetApproved, ChangeSetDenied,
		ChangeSetFailed, ChangeSetRolledBack} {
		rec := newSet("chg_acc_"+status, status)
		got, err := s.AcceptChangeSet(ctx, rec.ID, at)
		if err != nil {
			t.Fatalf("AcceptChangeSet %s: %v", status, err)
		}
		if !got.AcceptedAt.IsZero() {
			t.Fatalf("a %s change set was accepted", status)
		}
	}

	// The engine's own lifecycle write must not clear the user's verdict.
	applied.Status = ChangeSetApplied
	applied.AppliedAt = at.Add(-time.Minute)
	if err := s.UpdateChangeSet(ctx, applied); err != nil {
		t.Fatalf("UpdateChangeSet: %v", err)
	}
	got, err = s.GetChangeSet(ctx, applied.ID)
	if err != nil {
		t.Fatalf("GetChangeSet after update: %v", err)
	}
	if !got.AcceptedAt.Equal(at) {
		t.Fatalf("UpdateChangeSet cleared the acceptance: %v", got.AcceptedAt)
	}
}

func TestAWorkspaceWrittenBeforeTheNetworkColumnReadsAsAllowed(t *testing.T) {
	// Migration 0007 adds a nullable column. Existing rows must read as the
	// behaviour they were already running under — every workspace could
	// reach the network before BL-8 — and not as the stricter value, which
	// would silently change what a machine does on upgrade.
	ctx := context.Background()
	s := openTestStore(t)
	ws := testWorkspace("ws_1")
	if err := s.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// Put the row back the way an upgrade leaves it.
	if _, err := s.db.ExecContext(ctx, `UPDATE workspaces SET network = NULL WHERE id = ?`, ws.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Network != NetworkAllow {
		t.Errorf("network = %q, want the permissive value existing rows ran under", got.Network)
	}
}

func TestAWorkspaceRemembersThatItDeniesTheNetwork(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ws := testWorkspace("ws_1")
	ws.Network = NetworkDeny
	if err := s.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Network != NetworkDeny {
		t.Fatalf("network = %q, want deny", got.Network)
	}

	// And an unrelated update must not put it back: the settings on a folder
	// are not something a rename gets to decide.
	got.Name = "renamed"
	if err := s.UpdateWorkspace(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Network != NetworkDeny {
		t.Errorf("network = %q after renaming the workspace, want deny", again.Network)
	}

	// A value that is neither is refused rather than stored and misread.
	again.Network = "maybe"
	if err := s.UpdateWorkspace(ctx, again); err == nil {
		t.Error("an invalid network setting was accepted")
	}
}
