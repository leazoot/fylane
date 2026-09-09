package ctlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/store"
)

// memRules is an in-memory RuleStore; the persistence itself is covered in
// companion/internal/app.
type memRules struct {
	rules []routerule.Rule
	fail  error
	// saves counts writes back to the table, so a test can assert that a
	// save which routed nothing did not rewrite it.
	saves int
}

func (m *memRules) Load() ([]routerule.Rule, error) { return m.rules, m.fail }
func (m *memRules) Save(r []routerule.Rule) error {
	if m.fail != nil {
		return m.fail
	}
	m.saves++
	for i := range r {
		if err := r[i].Validate(); err != nil {
			return err
		}
	}
	m.rules = r
	return nil
}

func TestRulesRoundTripAndReportShadowing(t *testing.T) {
	f := newFixture(t)
	f.srv.Rules = &memRules{}

	body := map[string]any{"rules": []routerule.Rule{
		{ID: "1", Source: routerule.SourceAny, Patterns: []string{"*.md"}, Dest: "notes/", Action: routerule.ActionRoute},
		{ID: "2", Source: "claude", Patterns: []string{"*.md"}, Dest: "docs/", Action: routerule.ActionRoute},
	}}
	resp, raw := f.call(t, "POST", "/v1/rules", f.token, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d: %s", resp.StatusCode, raw)
	}

	resp, raw = f.call(t, "GET", "/v1/rules", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("load status = %d: %s", resp.StatusCode, raw)
	}
	var got struct {
		Rules      []routerule.Rule `json:"rules"`
		ShadowedBy []int            `json:"shadowed_by"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 2 || got.Rules[0].ID != "1" {
		t.Fatalf("rules = %+v", got.Rules)
	}
	// Order is priority: the ANY rule first makes the later one unreachable.
	if got.ShadowedBy[0] != -1 || got.ShadowedBy[1] != 0 {
		t.Fatalf("shadowed_by = %v, want [-1 0]", got.ShadowedBy)
	}
}

func TestRulesRejectEscapingDestination(t *testing.T) {
	f := newFixture(t)
	f.srv.Rules = &memRules{}
	body := map[string]any{"rules": []routerule.Rule{
		{ID: "1", Source: "claude", Patterns: []string{"*"}, Dest: "../../etc", Action: routerule.ActionRoute},
	}}
	resp, raw := f.call(t, "POST", "/v1/rules", f.token, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, raw)
	}
}

func TestRulesUnavailableWithoutStore(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.call(t, "GET", "/v1/rules", f.token, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestSourcesReportConnectionState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seen := time.Now().UTC().Add(-2 * time.Minute)
	if err := f.st.UpsertConnector(ctx, &store.Connector{
		Provider: "claude", RemoteConnectorID: "rc-1", Status: "active", LastConnectedAt: seen,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertConnector(ctx, &store.Connector{
		Provider: "chatgpt", RemoteConnectorID: "rc-2", Status: "revoked", LastConnectedAt: seen,
	}); err != nil {
		t.Fatal(err)
	}

	resp, raw := f.call(t, "GET", "/v1/sources", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
	var got struct {
		Sources []sourceView `json:"sources"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 3 {
		t.Fatalf("want a row for every known provider, got %d", len(got.Sources))
	}
	by := map[string]sourceView{}
	for _, s := range got.Sources {
		by[s.Provider] = s
	}
	if !by["claude"].Connected || by["claude"].LastSeenAt == "" {
		t.Errorf("claude = %+v, want connected", by["claude"])
	}
	if by["chatgpt"].Connected {
		t.Error("a revoked connector must not read as connected")
	}
	if by["grok"].Connected || by["grok"].LastSeenAt != "" {
		t.Errorf("grok = %+v, want never connected", by["grok"])
	}
	// The response is a status board; it must not leak credential material.
	if s := string(raw); strings.Contains(s, "token") || strings.Contains(s, "remote_connector_id") {
		t.Errorf("sources response leaks connector internals: %s", s)
	}
}

func TestTraceClearDropsHistoryAndUndoCopiesButNotFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ws, err := f.manager.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A landed change set with an undo copy on disk, and a workspace file
	// that must survive.
	cs := &store.ChangeSet{
		ID: "chg_clear_1", WorkspaceID: ws.ID, Provider: "claude", Summary: "write",
		Operations: []byte(`[{"type":"create","path":"a.md"}]`),
		Status:     store.ChangeSetApplied, BackupLocation: "chg_clear_1",
	}
	if err := f.st.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(f.backups, "chg_clear_1", "files")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "a.md"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(f.root, "a.md")
	if err := os.WriteFile(live, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Every real change set has audit events pointing at it — an approval
	// decision is recorded for each one. Those rows reference change_sets, so
	// a clear that ignores them fails the foreign key and deletes nothing.
	if err := f.st.AppendAuditEvent(ctx, &store.AuditEvent{
		ChangeSetID: cs.ID, EventType: "approval_decision", Result: "approved", DurationMS: 12,
	}); err != nil {
		t.Fatal(err)
	}

	// A change set still held at the gate: a caller is blocked on it, so
	// clearing must leave it alone.
	held := &store.ChangeSet{
		ID: "chg_held_1", WorkspaceID: ws.ID, Provider: "claude", Summary: "held",
		Operations: []byte(`[{"type":"update","path":"b.md"}]`),
		Status:     store.ChangeSetPending,
	}
	if err := f.st.CreateChangeSet(ctx, held); err != nil {
		t.Fatal(err)
	}

	f.srv.BackupRoot = f.backups
	resp, raw := f.call(t, "POST", "/v1/trace/clear", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
	var got struct {
		Cleared        int `json:"cleared"`
		BackupsRemoved int `json:"backups_removed"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cleared != 1 || got.BackupsRemoved != 1 {
		t.Fatalf("result = %+v, want 1 cleared and 1 backup removed", got)
	}

	list, err := f.st.ListChangeSets(ctx, ws.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "chg_held_1" {
		t.Fatalf("remaining change sets = %+v, want only the held one", list)
	}
	if _, err := os.Stat(filepath.Join(f.backups, "chg_clear_1")); !os.IsNotExist(err) {
		t.Error("undo copy survived the clear")
	}
	if body, err := os.ReadFile(live); err != nil || string(body) != "new" {
		t.Errorf("workspace file changed: %q (%v)", body, err)
	}
}

func TestWorkspacesReportAvailability(t *testing.T) {
	f := newFixture(t)
	resp, raw := f.call(t, "GET", "/v1/workspaces", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
	var got struct {
		Workspaces []struct {
			Availability string `json:"availability"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].Availability != availabilityOK {
		t.Fatalf("availability = %+v, want available", got.Workspaces)
	}

	// A directory that was moved away reads as missing, not unavailable:
	// the two have different repairs (relocate vs reconnect the volume).
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	if got := availabilityOf(f.root); got != availabilityMissing {
		t.Errorf("availability = %q, want missing", got)
	}
	if got := availabilityOf(filepath.Join(f.root, "deep", "gone")); got != availabilityUnavailable {
		t.Errorf("availability with a missing parent = %q, want unavailable", got)
	}
}
