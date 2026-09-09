package ctlapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/update"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

type fixture struct {
	addr    string
	token   string
	svc     *approval.Service
	manager *workspace.Manager
	st      *store.Store
	srv     *Server
	dataDir string
	root    string
	backups string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	root := t.TempDir()

	st, err := store.Open(ctx, filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	manager, err := workspace.NewManager(st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := manager.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCurrent(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	svc, err := approval.New(approval.ModeSafe, approval.DefaultBudgets(), nil)
	if err != nil {
		t.Fatal(err)
	}

	backups := filepath.Join(dataDir, "backups")
	engine := &txn.Engine{Store: st, BackupRoot: backups, Approver: svc}
	srv := &Server{Manager: manager, Store: st, Approvals: svc,
		Engine: engine, Tunnel: stubTunnel(true), RelayURL: "wss://relay.test/tunnel"}
	addr, err := srv.Start(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dataDir, controlFileName))
	if err != nil {
		t.Fatalf("control file: %v", err)
	}
	var cf controlFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatal(err)
	}
	if cf.Addr != addr || cf.Token == "" || cf.PID != os.Getpid() {
		t.Fatalf("control file = %+v, addr = %s", cf, addr)
	}
	info, err := os.Stat(filepath.Join(dataDir, controlFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("control file permissions = %v, %v", info.Mode(), err)
	}
	return &fixture{addr: addr, token: cf.Token, svc: svc, manager: manager, st: st, srv: srv, dataDir: dataDir, root: root, backups: backups}
}

func (f *fixture) call(t *testing.T, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://%s%s", f.addr, path), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

type stubUpdates struct{ s update.Status }

func (u stubUpdates) Last() update.Status { return u.s }

func TestStatusReportsUpdateNotice(t *testing.T) {
	f := newFixture(t)
	f.srv.Updates = stubUpdates{update.Status{Latest: "9.9.9", Available: true}}

	resp, body := f.call(t, "GET", "/v1/status", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	var got struct {
		LatestVersion   string `json:"latest_version"`
		UpdateAvailable bool   `json:"update_available"`
		DownloadPage    string `json:"download_page"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.LatestVersion != "9.9.9" || !got.UpdateAvailable {
		t.Fatalf("update fields = %+v", got)
	}
	// The page follows update.DownloadPage, which is empty until the public
	// release page exists — its own comment tells consumers to hide the link
	// while it is, and they can only do that if the wire stays silent too.
	if got.DownloadPage != update.DownloadPage {
		t.Errorf("download_page = %q, want the built-in constant %q", got.DownloadPage, update.DownloadPage)
	}
}

func TestPairEndpoint(t *testing.T) {
	f := newFixture(t)

	// No relay configured: the connect flow reports it instead of minting.
	resp, _ := f.call(t, "POST", "/v1/pair", f.token, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pair without relay = %d, want 409", resp.StatusCode)
	}

	f.srv.ConnectorURL = func() string { return "https://relay.example/mcp" }
	f.srv.Pairing = func(context.Context) (string, time.Duration, error) {
		return "ABCD-EFGH", 10 * time.Minute, nil
	}
	resp, body := f.call(t, "POST", "/v1/pair", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair = %d %s", resp.StatusCode, body)
	}
	var got struct {
		Code         string `json:"code"`
		ExpiresIn    int    `json:"expires_in_seconds"`
		ConnectorURL string `json:"connector_url"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "ABCD-EFGH" || got.ExpiresIn != 600 || got.ConnectorURL != "https://relay.example/mcp" {
		t.Fatalf("pair response = %+v", got)
	}
}

func TestSafetyModeSwitch(t *testing.T) {
	f := newFixture(t)
	persisted := ""
	f.srv.PersistApprovalMode = func(m string) error { persisted = m; return nil }

	resp, body := f.call(t, "POST", "/v1/safety", f.token, map[string]string{"mode": "balanced"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("safety switch = %d %s", resp.StatusCode, body)
	}
	if persisted != "balanced" || f.srv.Approvals.Mode() != "balanced" {
		t.Fatalf("mode = %q persisted = %q", f.srv.Approvals.Mode(), persisted)
	}
	if resp, _ := f.call(t, "POST", "/v1/safety", f.token, map[string]string{"mode": "always_allow"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("always_allow accepted: %d", resp.StatusCode)
	}
	_, body = f.call(t, "GET", "/v1/status", f.token, nil)
	if !strings.Contains(string(body), `"approval_mode":"balanced"`) {
		t.Fatalf("status missing mode: %s", body)
	}
}

func TestControlFileModeRepaired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, controlFileName)

	// A pre-planted world-readable file must not keep its lax mode when the
	// token is written into it.
	if err := os.WriteFile(path, []byte("{}"), 0o666); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	manager, err := workspace.NewManager(st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := approval.New(approval.ModeSafe, approval.DefaultBudgets(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Manager: manager, Store: st, Approvals: svc}
	if _, err := srv.Start(ctx, dataDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pre-planted control file kept mode %v (err %v)", info.Mode(), err)
	}
}

func TestAuthRequired(t *testing.T) {
	f := newFixture(t)
	for _, token := range []string{"", "wrong-token"} {
		resp, _ := f.call(t, "GET", "/v1/status", token, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", token, resp.StatusCode)
		}
	}
	resp, _ := f.call(t, "GET", "/v1/status", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid token: status = %d", resp.StatusCode)
	}
}

func TestApprovalListAndResolve(t *testing.T) {
	f := newFixture(t)

	// Raise a pending approval by blocking in the background.
	decided := make(chan txn.Decision, 1)
	go func() {
		d, _ := f.svc.Approve(context.Background(), &txn.ApprovalRequest{
			ChangeSetID: "chg_ctl", WorkspaceID: "ws_x", WorkspaceName: "demo",
			Provider: "grok", Summary: "ctl test",
			Operations: []txn.OpPreview{{Type: txn.OpUpdate, Path: "a.txt", Diff: "-a\n+b\n"}},
		})
		decided <- d
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.svc.Pending()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("approval never became pending")
		}
		time.Sleep(2 * time.Millisecond)
	}

	resp, body := f.call(t, "GET", "/v1/approvals", f.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "chg_ctl") {
		t.Fatalf("approvals = %d %s", resp.StatusCode, body)
	}

	resp, _ = f.call(t, "POST", "/v1/approvals/resolve", f.token,
		map[string]any{"change_set_id": "chg_ctl", "approved": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d", resp.StatusCode)
	}
	d := <-decided
	if !d.Approved {
		t.Fatalf("decision = %+v", d)
	}

	// Resolving again → 404 (already decided/unknown).
	resp, _ = f.call(t, "POST", "/v1/approvals/resolve", f.token,
		map[string]any{"change_set_id": "chg_ctl", "approved": true})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("double resolve status = %d", resp.StatusCode)
	}
}

// raisePending blocks an approval in the background and waits for it to reach
// the pending list, so a test can read what the desktop would render.
func raisePending(t *testing.T, f *fixture, req *txn.ApprovalRequest) {
	t.Helper()
	go f.svc.Approve(context.Background(), req)
	deadline := time.Now().Add(5 * time.Second)
	for len(f.svc.Pending()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("approval never became pending")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestApprovalListCarriesTheBasisForACommandDecision(t *testing.T) {
	// A command prompt has no diff. The desktop once received the
	// summary and nothing else, so the user was deciding on an argv with no
	// statement of what the rule table objected to or where it would run.
	f := newFixture(t)
	raisePending(t, f, &txn.ApprovalRequest{
		ChangeSetID: "cmd:abc", WorkspaceID: "ws_x", WorkspaceName: "demo",
		Provider: "chatgpt", Summary: "git push --force",
		Kind:    txn.KindCommand,
		Command: []string{"git", "push", "--force"},
		Dir:     "src",
		Rule:    "rewrites-published-history",
		Reason:  "force-pushes, which overwrites history other people may already have",
	})

	resp, body := f.call(t, "GET", "/v1/approvals", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approvals = %d %s", resp.StatusCode, body)
	}
	var got struct {
		Approvals []pendingApproval `json:"approvals"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(got.Approvals) != 1 {
		t.Fatalf("approvals = %d", len(got.Approvals))
	}
	a := got.Approvals[0]
	if a.Kind != txn.KindCommand {
		t.Errorf("kind = %q, want %q", a.Kind, txn.KindCommand)
	}
	if strings.Join(a.Command, " ") != "git push --force" {
		t.Errorf("command = %q", a.Command)
	}
	if a.Dir != "src" {
		t.Errorf("dir = %q", a.Dir)
	}
	if a.Rule != "rewrites-published-history" {
		t.Errorf("rule = %q", a.Rule)
	}
	if !strings.Contains(a.Reason, "overwrites history") {
		t.Errorf("reason = %q", a.Reason)
	}
}

func TestApprovalKindDefaultsToWriteRatherThanEmpty(t *testing.T) {
	// A prompt that reached the desktop with no kind would put the screen
	// back to guessing from which fields are populated.
	f := newFixture(t)
	raisePending(t, f, &txn.ApprovalRequest{
		ChangeSetID: "chg_nokind", WorkspaceID: "ws_x", WorkspaceName: "demo",
		Provider: "claude", Summary: "no kind set",
		Operations: []txn.OpPreview{{Type: txn.OpUpdate, Path: "a.txt"}},
	})

	_, body := f.call(t, "GET", "/v1/approvals", f.token, nil)
	var got struct {
		Approvals []pendingApproval `json:"approvals"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(got.Approvals) != 1 || got.Approvals[0].Kind != txn.KindWrite {
		t.Fatalf("kind = %+v, want %q", got.Approvals, txn.KindWrite)
	}
}

func TestWorkspacesEndpointShowsRootPathLocally(t *testing.T) {
	f := newFixture(t)
	resp, body := f.call(t, "GET", "/v1/workspaces", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "current_workspace_id") {
		t.Fatalf("body = %s", body)
	}
	// The desktop UI shows the user their own folder; this is a
	// loopback-only surface. MCP and Relay surfaces still never carry
	// root_path — enforced by the mcpserver tests.
	if !strings.Contains(string(body), "root_path") || !strings.Contains(string(body), f.root) {
		t.Fatalf("workspaces endpoint must include root_path for local display: %s", body)
	}
}

func TestWorkspaceActions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Add a second workspace over the API, then drive it through
	// select/pause/resume and verify the store agrees after each step.
	otherRoot := t.TempDir()
	resp, body := f.call(t, "POST", "/v1/workspaces/add", f.token, map[string]any{"path": otherRoot})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add = %d %s", resp.StatusCode, body)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &added); err != nil || added.ID == "" {
		t.Fatalf("add response = %s", body)
	}

	if resp, body := f.call(t, "POST", "/v1/workspaces/select", f.token, map[string]any{"id": added.ID}); resp.StatusCode != http.StatusOK {
		t.Fatalf("select = %d %s", resp.StatusCode, body)
	}
	if rec, err := f.manager.Current(ctx); err != nil || rec.ID != added.ID {
		t.Fatalf("current after select = %v, %v", rec, err)
	}

	if resp, _ := f.call(t, "POST", "/v1/workspaces/pause", f.token, map[string]any{"id": added.ID}); resp.StatusCode != http.StatusOK {
		t.Fatal("pause failed")
	}
	if rec, _ := f.manager.Get(ctx, added.ID); rec.Status != "paused" {
		t.Fatalf("status after pause = %s", rec.Status)
	}
	if resp, _ := f.call(t, "POST", "/v1/workspaces/resume", f.token, map[string]any{"id": added.ID}); resp.StatusCode != http.StatusOK {
		t.Fatal("resume failed")
	}
	if rec, _ := f.manager.Get(ctx, added.ID); rec.Status != "active" {
		t.Fatalf("status after resume = %s", rec.Status)
	}

	// Bad bodies are rejected.
	if resp, _ := f.call(t, "POST", "/v1/workspaces/select", f.token, map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("select without id must 400")
	}
	if resp, _ := f.call(t, "POST", "/v1/workspaces/add", f.token, map[string]any{"path": filepath.Join(otherRoot, "missing")}); resp.StatusCode == http.StatusOK {
		t.Fatal("adding a nonexistent folder must fail")
	}
}

// approverFunc adapts a function to txn.Approver for test setup.
type approverFunc func(context.Context, *txn.ApprovalRequest) (txn.Decision, error)

func (f approverFunc) Approve(ctx context.Context, req *txn.ApprovalRequest) (txn.Decision, error) {
	return f(ctx, req)
}

func TestRollbackEndpoint(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Seed one applied change set with an auto-approving engine that shares
	// the server's store and backup root.
	seed := &txn.Engine{Store: f.st, BackupRoot: f.backups,
		Approver: approverFunc(func(context.Context, *txn.ApprovalRequest) (txn.Decision, error) {
			return txn.Decision{Approved: true}, nil
		})}
	rec, err := f.manager.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.manager.Open(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := seed.Execute(ctx, ws, txn.Request{Provider: "test", Summary: "seed",
		Operations: []txn.Operation{{Type: txn.OpCreate, Path: "a.txt", Content: "v1\n"}}})
	if err != nil || res.Status != txn.StatusApplied {
		t.Fatalf("seed = %+v, %v", res, err)
	}

	// UI-initiated rollback: the endpoint claims the engine's approval
	// request itself (the confirm-sheet click is the local decision).
	resp, body := f.call(t, "POST", "/v1/changesets/rollback", f.token,
		map[string]any{"change_set_id": res.ChangeSetID})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "rolled_back") {
		t.Fatalf("rollback = %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(f.root, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("rolled-back create still on disk")
	}

	// Unknown change set → clear client error, not a hang.
	resp, _ = f.call(t, "POST", "/v1/changesets/rollback", f.token,
		map[string]any{"change_set_id": "chg_nope"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown rollback = %d", resp.StatusCode)
	}

	// The confirmation surface is an allowlist: an unrecognised caller may
	// not put its own text into the decision reason.
	resp, _ = f.call(t, "POST", "/v1/changesets/rollback", f.token,
		map[string]any{"change_set_id": res.ChangeSetID, "confirmed_in": "the page said yes"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged confirmation surface = %d", resp.StatusCode)
	}
}

// TestRollbackFromPanel covers the browser side panel's rollback: the same
// endpoint, recorded as confirmed in the panel rather than the desktop.
func TestRollbackFromPanel(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	seed := &txn.Engine{Store: f.st, BackupRoot: f.backups,
		Approver: approverFunc(func(context.Context, *txn.ApprovalRequest) (txn.Decision, error) {
			return txn.Decision{Approved: true}, nil
		})}
	rec, err := f.manager.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.manager.Open(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := seed.Execute(ctx, ws, txn.Request{Provider: "test", Summary: "seed",
		Operations: []txn.Operation{{Type: txn.OpCreate, Path: "panel.txt", Content: "v1\n"}}})
	if err != nil || res.Status != txn.StatusApplied {
		t.Fatalf("seed = %+v, %v", res, err)
	}

	resp, body := f.call(t, "POST", "/v1/changesets/rollback", f.token,
		map[string]any{"change_set_id": res.ChangeSetID, "confirmed_in": "panel"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "rolled_back") {
		t.Fatalf("panel rollback = %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(f.root, "panel.txt")); !os.IsNotExist(err) {
		t.Fatal("rolled-back create still on disk")
	}
}

// Acceptance is a record, not an authorization. The endpoint's job is to
// write the user's verdict down once and to leave everything else alone —
// including the undo, which is the invariant that the column form of
// migration 0006 buys and the status form would have broken.
func TestAcceptEndpoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	seed := &txn.Engine{Store: f.st, BackupRoot: f.backups,
		Approver: approverFunc(func(context.Context, *txn.ApprovalRequest) (txn.Decision, error) {
			return txn.Decision{Approved: true}, nil
		})}
	rec, err := f.manager.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.manager.Open(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := seed.Execute(ctx, ws, txn.Request{Provider: "test", Summary: "seed",
		Operations: []txn.Operation{{Type: txn.OpCreate, Path: "accepted.txt", Content: "v1\n"}}})
	if err != nil || res.Status != txn.StatusApplied {
		t.Fatalf("seed = %+v, %v", res, err)
	}

	resp, body := f.call(t, "POST", "/v1/changesets/accept", f.token,
		map[string]any{"change_set_id": res.ChangeSetID})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"accepted_at"`) {
		t.Fatalf("accept = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"status":"applied"`) {
		t.Fatalf("acceptance changed the status: %s", body)
	}

	stored, err := f.st.GetChangeSet(ctx, res.ChangeSetID)
	if err != nil {
		t.Fatal(err)
	}
	first := stored.AcceptedAt
	if first.IsZero() {
		t.Fatal("accept endpoint recorded nothing")
	}

	// One review, one audit row, however many times the button is pressed.
	accepts := func() int {
		events, err := f.st.ListAuditEvents(ctx, res.ChangeSetID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range events {
			if e.EventType == "change_set_accepted" {
				n++
			}
		}
		return n
	}
	if got := accepts(); got != 1 {
		t.Fatalf("audit rows after one acceptance = %d, want 1", got)
	}
	resp, _ = f.call(t, "POST", "/v1/changesets/accept", f.token,
		map[string]any{"change_set_id": res.ChangeSetID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second accept = %d", resp.StatusCode)
	}
	stored, err = f.st.GetChangeSet(ctx, res.ChangeSetID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.AcceptedAt.Equal(first) {
		t.Fatalf("second acceptance moved the timestamp: %v then %v", first, stored.AcceptedAt)
	}
	if got := accepts(); got != 1 {
		t.Fatalf("audit rows after two acceptances = %d, want 1", got)
	}

	// Accepting does not spend the undo. Saying "this is right" and giving up
	// the ability to take it back are two different decisions, and that is
	// the standing rule that they must not be asked as one.
	resp, body = f.call(t, "POST", "/v1/changesets/rollback", f.token,
		map[string]any{"change_set_id": res.ChangeSetID})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "rolled_back") {
		t.Fatalf("rollback after acceptance = %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(f.root, "accepted.txt")); !os.IsNotExist(err) {
		t.Fatal("rolled-back create still on disk")
	}

	// A change set that no longer stands as applied cannot be reviewed: there
	// is nothing on disk left to have judged.
	resp, _ = f.call(t, "POST", "/v1/changesets/accept", f.token,
		map[string]any{"change_set_id": res.ChangeSetID})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("accept of a rolled-back change set = %d, want 409", resp.StatusCode)
	}

	resp, _ = f.call(t, "POST", "/v1/changesets/accept", f.token,
		map[string]any{"change_set_id": "chg_nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("accept of an unknown change set = %d, want 404", resp.StatusCode)
	}
}

// stubTunnel reports a fixed connection state.
type stubTunnel bool

func (s stubTunnel) Connected() bool { return bool(s) }

func TestStatusReportsTunnel(t *testing.T) {
	f := newFixture(t)
	resp, body := f.call(t, "GET", "/v1/status", f.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"tunnel":"connected"`) {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
}

func TestChangeSetsEndpoint(t *testing.T) {
	f := newFixture(t)
	resp, body := f.call(t, "GET", "/v1/changesets", f.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "change_sets") {
		t.Fatalf("changesets = %d %s", resp.StatusCode, body)
	}
}

func TestSaveEndpoint(t *testing.T) {
	f := newFixture(t)

	// Safe mode blocks on approval: resolve it from a helper goroutine the
	// way the desktop UI would.
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, p := range f.svc.Pending() {
				f.svc.Resolve(p.Request.ChangeSetID, true, "")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	body := map[string]any{
		"provider": "chatgpt",
		"summary":  "Save answer",
		"files": []map[string]any{
			{"path": "notes/answer.md", "content_base64": base64.StdEncoding.EncodeToString([]byte("# hi\n"))},
		},
	}
	resp, raw := f.call(t, "POST", "/v1/save", f.token, body)
	<-done
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"status":"applied"`) {
		t.Fatalf("save = %d %s", resp.StatusCode, raw)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "notes", "answer.md"))
	if err != nil || string(data) != "# hi\n" {
		t.Fatalf("saved content = %q, %v", data, err)
	}

	// Saving to the same path again without a base hash is a conflict —
	// never a silent overwrite.
	resp, raw = f.call(t, "POST", "/v1/save", f.token, body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"status":"conflict"`) {
		t.Fatalf("re-save = %d %s", resp.StatusCode, raw)
	}
	if data, _ := os.ReadFile(filepath.Join(f.root, "notes", "answer.md")); string(data) != "# hi\n" {
		t.Fatalf("conflict overwrote content: %q", data)
	}

	// Bad requests are clear client errors.
	if resp, _ := f.call(t, "POST", "/v1/save", f.token, map[string]any{"files": []any{}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("empty files must 400")
	}
	if resp, _ := f.call(t, "POST", "/v1/save", f.token, map[string]any{
		"files": []map[string]any{{"path": "a.txt", "content_base64": "not base64!!"}},
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("bad base64 must 400")
	}
}

// approveNext stands in for the user at the window: it approves the first
// change set that reaches the gate. Without it every write in safe mode
// would sit pending, which is the correct behaviour but not what these
// tests are about.
func approveNext(t *testing.T, f *fixture) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, p := range f.svc.Pending() {
				f.svc.Resolve(p.Request.ChangeSetID, true, "")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { <-done })
}

// A saved answer arrives with a name Fylane guessed and no location the user
// chose; that is the question route rules answer.
func TestSaveFollowsRouteRules(t *testing.T) {
	f := newFixture(t)
	rules := &memRules{rules: []routerule.Rule{
		{ID: "r1", Source: "claude", Patterns: []string{"*.md"}, Dest: "docs/", Action: routerule.ActionRoute},
	}}
	f.srv.Rules = rules
	approveNext(t, f)

	body := SaveRequest{
		Provider: "claude", Summary: "Save answer",
		Files: []SaveFile{{Path: "ai-answers/plan.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("hi"))}},
	}
	resp, data := f.call(t, "POST", "/v1/save", f.token, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d %s", resp.StatusCode, data)
	}
	var res txn.Result
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != txn.StatusApplied {
		t.Fatalf("status = %s (%s)", res.Status, res.Reason)
	}
	// The rule chose the folder; the file kept its own name.
	if _, err := os.Stat(filepath.Join(f.root, "docs", "plan.md")); err != nil {
		t.Fatalf("file did not land where the rule points: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "ai-answers", "plan.md")); !os.IsNotExist(err) {
		t.Fatal("the file also landed at the requested path")
	}
	// The rule is credited once the save actually landed.
	if rules.rules[0].Uses != 1 {
		t.Fatalf("uses = %d, want 1", rules.rules[0].Uses)
	}

	// Retrying the same change set must not count the save twice.
	body2 := SaveRequest{
		ChangeSetID: res.ChangeSetID, Provider: "claude", Summary: "Save answer",
		Files: []SaveFile{{Path: "ai-answers/plan.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("hi"))}},
	}
	if resp, data := f.call(t, "POST", "/v1/save", f.token, body2); resp.StatusCode != http.StatusOK {
		t.Fatalf("retry = %d %s", resp.StatusCode, data)
	}
	if rules.rules[0].Uses != 1 {
		t.Fatalf("a replayed change set was counted again: uses = %d", rules.rules[0].Uses)
	}
}

// An "ask" rule must stop a write the policy would otherwise have let
// through, and must not move it anywhere.
func TestAskRuleHoldsAWriteBalancedModeWouldPass(t *testing.T) {
	f := newFixture(t)
	if err := f.srv.Approvals.SetMode(approval.ModeBalanced); err != nil {
		t.Fatal(err)
	}
	f.srv.Rules = &memRules{rules: []routerule.Rule{
		{ID: "hold", Source: routerule.SourceAny, Patterns: []string{"*.env"}, Action: routerule.ActionAsk},
	}}

	save := func(path string) txn.Result {
		t.Helper()
		body := SaveRequest{
			Provider: "claude", Summary: "Save answer",
			Files: []SaveFile{{Path: path, ContentBase64: base64.StdEncoding.EncodeToString([]byte("x"))}},
		}
		resp, data := f.call(t, "POST", "/v1/save", f.token, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("save %s = %d %s", path, resp.StatusCode, data)
		}
		var res txn.Result
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Balanced mode lets a plain new file through with no prompt…
	if res := save("notes.md"); res.Status != txn.StatusApplied {
		t.Fatalf("unmatched file = %s (%s)", res.Status, res.Reason)
	}
	// …but the rule outranks the policy for the file the user named.
	res := save("config.env")
	if res.Status != txn.StatusPending {
		t.Fatalf("ask rule did not hold the write: %s (%s)", res.Status, res.Reason)
	}
	if _, err := os.Stat(filepath.Join(f.root, "config.env")); !os.IsNotExist(err) {
		t.Fatal("a held write reached disk")
	}
}

// A save with no matching rule keeps the path the extension asked for, and
// nothing is written back to the rule table.
func TestSaveWithoutMatchingRuleIsUntouched(t *testing.T) {
	f := newFixture(t)
	rules := &memRules{rules: []routerule.Rule{
		{ID: "r1", Source: "chatgpt", Patterns: []string{"*.ts"}, Dest: "src/", Action: routerule.ActionRoute},
	}}
	f.srv.Rules = rules
	approveNext(t, f)

	body := SaveRequest{
		Provider: "claude", Summary: "Save answer",
		Files: []SaveFile{{Path: "ai-answers/plan.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("hi"))}},
	}
	if resp, data := f.call(t, "POST", "/v1/save", f.token, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d %s", resp.StatusCode, data)
	}
	if _, err := os.Stat(filepath.Join(f.root, "ai-answers", "plan.md")); err != nil {
		t.Fatalf("file did not land at the requested path: %v", err)
	}
	if rules.saves != 0 {
		t.Fatalf("the rule table was rewritten for a save it did not route (%d times)", rules.saves)
	}
}

// stubConnect stands in for the app's tunnel control.
type stubConnect struct {
	doc      ConnectDoc
	applied  TunnelRequest
	stopped  bool
	applyErr error
	stopErr  error

	setupFor       string
	setupErr       error
	setupCancelled bool
	signedOut      string

	downloadFor       string
	downloadErr       error
	downloadCancelled bool
}

func (s *stubConnect) ConnectSnapshot() ConnectDoc { return s.doc }

func (s *stubConnect) ApplyTunnel(_ context.Context, req TunnelRequest) error {
	if s.applyErr != nil {
		return s.applyErr
	}
	s.applied = req
	s.doc.Provider, s.doc.State = req.Provider, "starting"
	return nil
}

func (s *stubConnect) StartSetup(_ context.Context, provider string) error {
	s.setupFor = provider
	return s.setupErr
}

func (s *stubConnect) CancelSetup() { s.setupCancelled = true }

func (s *stubConnect) StartDownload(_ context.Context, provider string) error {
	s.downloadFor = provider
	return s.downloadErr
}

func (s *stubConnect) CancelDownload() { s.downloadCancelled = true }

func (s *stubConnect) SignOut(provider string) error {
	s.signedOut = provider
	return nil
}

func (s *stubConnect) StopTunnel() error {
	if s.stopErr != nil {
		return s.stopErr
	}
	s.stopped = true
	s.doc.Provider, s.doc.State = "", "stopped"
	return nil
}

func TestConnectEndpoints(t *testing.T) {
	f := newFixture(t)

	// Nothing wired at all is the only 409 on the read: with no control there
	// is no answer to give.
	resp, _ := f.call(t, "GET", "/v1/connect", f.token, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("connect with no control = %d, want 409", resp.StatusCode)
	}

	// A relay-mode companion answers the read — "where do platforms connect"
	// has an answer either way — and refuses only the tunnel controls. The
	// screen used to show the raw 409 because the read failed too.
	relay := &stubConnect{doc: ConnectDoc{Mode: "relay", ConnectorURL: "https://relay.example/mcp"},
		applyErr: ErrNotDirect, stopErr: ErrNotDirect}
	f.srv.Connect = relay
	resp, body := f.call(t, "GET", "/v1/connect", f.token, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"mode":"relay"`)) {
		t.Fatalf("relay-mode connect = %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.call(t, "POST", "/v1/connect", f.token, TunnelRequest{Provider: "ngrok"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("relay-mode apply = %d, want 409", resp.StatusCode)
	}
	if resp, _ := f.call(t, "POST", "/v1/connect/stop", f.token, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("relay-mode stop = %d, want 409", resp.StatusCode)
	}

	stub := &stubConnect{doc: ConnectDoc{Mode: "direct", State: "stopped",
		Providers: []ConnectProvider{{Kind: "cloudflare-quick", Binary: "cloudflared"}}}}
	f.srv.Connect = stub

	resp, body = f.call(t, "GET", "/v1/connect", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect = %d %s", resp.StatusCode, body)
	}
	var doc ConnectDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Mode != "direct" || len(doc.Providers) != 1 {
		t.Fatalf("doc = %+v", doc)
	}

	resp, body = f.call(t, "POST", "/v1/connect", f.token, TunnelRequest{
		Provider: "cloudflare-named", Hostname: "fylane.example.com", Token: "secret"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply = %d %s", resp.StatusCode, body)
	}
	if stub.applied.Provider != "cloudflare-named" || stub.applied.Hostname != "fylane.example.com" {
		t.Fatalf("applied = %+v", stub.applied)
	}
	// The token is a credential: it goes to the keychain, and nothing echoes
	// it back over the control API.
	if bytes.Contains(body, []byte("secret")) {
		t.Fatal("the response echoed the tunnel token")
	}

	resp, _ = f.call(t, "POST", "/v1/connect/stop", f.token, nil)
	if resp.StatusCode != http.StatusOK || !stub.stopped {
		t.Fatalf("stop = %d, stopped = %v", resp.StatusCode, stub.stopped)
	}
	// Unauthenticated callers get nowhere near any of it.
	if resp, _ := f.call(t, "GET", "/v1/connect", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated connect = %d", resp.StatusCode)
	}
}

func TestConnectDownloadEndpoints(t *testing.T) {
	f := newFixture(t)
	stub := &stubConnect{doc: ConnectDoc{Mode: "direct", State: "stopped",
		Providers: []ConnectProvider{{Kind: "cloudflare-quick", Binary: "cloudflared"}}}}
	f.srv.Connect = stub

	resp, body := f.call(t, "POST", "/v1/connect/download", f.token,
		map[string]string{"provider": "cloudflare-quick"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download = %d %s", resp.StatusCode, body)
	}
	if stub.downloadFor != "cloudflare-quick" {
		t.Fatalf("started a download for %q", stub.downloadFor)
	}

	resp, _ = f.call(t, "POST", "/v1/connect/download/cancel", f.token, nil)
	if resp.StatusCode != http.StatusOK || !stub.downloadCancelled {
		t.Fatalf("cancel = %d, cancelled = %v", resp.StatusCode, stub.downloadCancelled)
	}

	// The request names a provider and nothing else. A URL, a version or a
	// digest in the body must not reach the Core in any form — this endpoint
	// is where such a field would first become reachable, so the guard is
	// asserted here rather than assumed from the struct definition.
	stub.downloadFor = ""
	resp, _ = f.call(t, "POST", "/v1/connect/download", f.token, map[string]string{
		"provider": "cloudflare-quick",
		"url":      "https://evil.example/payload",
		"sha256":   "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if resp.StatusCode != http.StatusOK || stub.downloadFor != "cloudflare-quick" {
		t.Fatalf("download with extra fields = %d, provider %q", resp.StatusCode, stub.downloadFor)
	}

	// A failure to start is the caller's answer, not a silent no-op.
	stub.downloadErr = errors.New("cloudflared is already on this machine")
	if resp, _ := f.call(t, "POST", "/v1/connect/download", f.token,
		map[string]string{"provider": "cloudflare-quick"}); resp.StatusCode == http.StatusOK {
		t.Fatal("a refused download answered 200")
	}

	// With no control wired there is nothing to download onto.
	f.srv.Connect = nil
	if resp, _ := f.call(t, "POST", "/v1/connect/download", f.token,
		map[string]string{"provider": "cloudflare-quick"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("download with no control = %d, want 409", resp.StatusCode)
	}
	if resp, _ := f.call(t, "POST", "/v1/connect/download", "",
		map[string]string{"provider": "cloudflare-quick"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("an unauthenticated caller could start a download")
	}
}
