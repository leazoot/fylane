package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"

	_ "modernc.org/sqlite"
)

// Three of the four approval kinds never reached the audit log at all. Their
// synthetic ids were written into audit_events.change_set_id, which is a
// foreign key into change_sets, so every insert failed and the failure was a
// log line. The machine ran a command the user had approved and kept no
// record that they were ever asked (found on a real machine, 2026-08-30).
func TestEveryApprovalKindReachesTheAuditLog(t *testing.T) {
	for _, tc := range []struct {
		kind      string
		wantEvent string
		wantPath  string
	}{
		{txn.KindCommand, "approval_command", "src"},
		{txn.KindDisclosure, "approval_disclosure", ".env"},
		{txn.KindDelegation, "approval_delegation", "src"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dir := t.TempDir()
			st, db := auditStore(t, dir)

			req := &txn.ApprovalRequest{
				// The id no change set will ever have. Writing it into the
				// foreign key is what used to lose the row.
				ChangeSetID: "0f1e2d3c4b5a69788796a5b4c3d2e1f0",
				Kind:        tc.kind,
				Dir:         "src",
			}
			if tc.kind == txn.KindDisclosure {
				req.Operations = []txn.OpPreview{{Path: ".env"}}
			}
			decisionRecorder(st, testLogger())(
				&approval.Pending{Request: req}, true, 3200*time.Millisecond)

			var event, path, result string
			var linked sql.NullString
			var ms int64
			err := db.QueryRow(`SELECT event_type, path_relative, result, duration_ms, change_set_id
				FROM audit_events ORDER BY id DESC LIMIT 1`).Scan(&event, &path, &result, &ms, &linked)
			if err != nil {
				t.Fatalf("no audit row for a %s approval: %v", tc.kind, err)
			}
			if event != tc.wantEvent {
				t.Errorf("event_type = %q, want %q — the row cannot say which question was answered", event, tc.wantEvent)
			}
			if result != "approved" || ms != 3200 {
				t.Errorf("result/duration = %q/%d, want approved/3200", result, ms)
			}
			if path != tc.wantPath {
				t.Errorf("path_relative = %q, want %q", path, tc.wantPath)
			}
			// The link has to be absent rather than dangling: a non-existent
			// change set is what the foreign key rejects.
			if linked.Valid {
				t.Errorf("change_set_id = %q on a %s approval, which names no change set", linked.String, tc.kind)
			}
		})
	}
}

func TestDeniedWriteApprovalKeepsItsChangeSetLink(t *testing.T) {
	// The write path is the one that always worked, and it works because the
	// id is real. It has to keep the link, or the change set's own history
	// loses the decision that produced it.
	dir := t.TempDir()
	st, db := auditStore(t, dir)
	cs := &store.ChangeSet{ID: "chg_0000000000000001", WorkspaceID: seedWorkspace(t, st),
		Provider: "claude", Summary: "s", Status: store.ChangeSetPending,
		Operations: []byte(`[{"type":"update","path":"a.txt"}]`)}
	if err := st.CreateChangeSet(context.Background(), cs); err != nil {
		t.Fatal(err)
	}

	decisionRecorder(st, testLogger())(&approval.Pending{Request: &txn.ApprovalRequest{
		ChangeSetID: cs.ID, Kind: txn.KindWrite,
	}}, false, time.Second)

	var event, result string
	var linked sql.NullString
	if err := db.QueryRow(`SELECT event_type, result, change_set_id FROM audit_events
		ORDER BY id DESC LIMIT 1`).Scan(&event, &result, &linked); err != nil {
		t.Fatal(err)
	}
	if event != "approval_write" || result != "denied" {
		t.Errorf("event/result = %q/%q", event, result)
	}
	if linked.String != cs.ID {
		t.Errorf("change_set_id = %q, want the change set it decided", linked.String)
	}
}

func TestApprovalWithNoKindIsRecordedAsAWrite(t *testing.T) {
	// Kind is documented to default to write for requests built before the
	// field existed. A recorder that dropped those would lose the one kind
	// that was never broken.
	dir := t.TempDir()
	st, db := auditStore(t, dir)
	decisionRecorder(st, testLogger())(&approval.Pending{Request: &txn.ApprovalRequest{}}, true, 0)

	var event string
	if err := db.QueryRow(`SELECT event_type FROM audit_events ORDER BY id DESC LIMIT 1`).Scan(&event); err != nil {
		t.Fatalf("an approval with no kind was not recorded: %v", err)
	}
	if event != "approval_write" {
		t.Errorf("event_type = %q, want approval_write", event)
	}
}

// auditStore opens the store plus a second read-only handle, because the
// store lists audit events by change set and these rows deliberately have no
// change set to list them by.
func auditStore(t *testing.T, dir string) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(dir, "fylane.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening a reader: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return st, db
}

func seedWorkspace(t *testing.T, st *store.Store) string {
	t.Helper()
	ws := &store.Workspace{ID: "ws_1", Name: "w", RootPath: t.TempDir(),
		Mode: store.ModeReadWrite, Status: "active"}
	if err := st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("seeding a workspace: %v", err)
	}
	return ws.ID
}

func TestARefusedCommandKeepsItsArgv(t *testing.T) {
	// The approval log says a command was denied; only the command audit says
	// which one. Without this the user could see that they had refused
	// something and never find out what (reported from a real machine,
	// 2026-08-30).
	dir := t.TempDir()
	st, db := auditStore(t, dir)
	ws := seedWorkspace(t, st)

	decisionRecorder(st, testLogger())(&approval.Pending{Request: &txn.ApprovalRequest{
		ChangeSetID: "cmd:abc", WorkspaceID: ws, Kind: txn.KindDisclosure,
		Command: []string{"ps"}, Rule: "reads-machine-state", Reason: "reports the whole machine",
	}}, false, 12*time.Second)

	var argv, outcome, rule string
	if err := db.QueryRow(`SELECT argv, outcome, rule_id FROM exec_events
		ORDER BY id DESC LIMIT 1`).Scan(&argv, &outcome, &rule); err != nil {
		t.Fatalf("a refused command left no command-audit row: %v", err)
	}
	if argv != `["ps"]` || outcome != "denied" || rule != "reads-machine-state" {
		t.Errorf("argv/outcome/rule = %s/%s/%s", argv, outcome, rule)
	}
}

func TestAnApprovedCommandIsNotAuditedTwice(t *testing.T) {
	// The execution path writes its own row when the command actually runs.
	// A second row from here would double every approved command in the
	// history the task screen reads.
	dir := t.TempDir()
	st, db := auditStore(t, dir)
	decisionRecorder(st, testLogger())(&approval.Pending{Request: &txn.ApprovalRequest{
		ChangeSetID: "cmd:abc", WorkspaceID: seedWorkspace(t, st), Kind: txn.KindCommand,
		Command: []string{"go", "test"},
	}}, true, time.Second)

	var n int
	db.QueryRow(`SELECT count(*) FROM exec_events`).Scan(&n)
	if n != 0 {
		t.Errorf("exec_events rows = %d, want the execution path to be the only writer for approvals", n)
	}
}

func TestTheApprovalServiceIsBornWithARecorder(t *testing.T) {
	// The recorder used to be assigned inline in Run(), where nothing could
	// reach it. Deleting that line broke nothing and silenced the audit; this
	// is the guard for that, not for the recorder's own behaviour.
	st, db := auditStore(t, t.TempDir())
	svc, err := newApprovals("safe", st, testLogger(), func(*approval.Pending) {})
	if err != nil {
		t.Fatal(err)
	}
	if svc.OnDecision == nil {
		t.Fatal("the service was built without anything to record decisions")
	}
	svc.OnDecision(&approval.Pending{Request: &txn.ApprovalRequest{
		ChangeSetID: "cmd:x", WorkspaceID: seedWorkspace(t, st), Kind: txn.KindCommand,
		Command: []string{"ps"},
	}}, false, time.Second)

	var n int
	db.QueryRow(`SELECT count(*) FROM audit_events WHERE event_type = 'approval_command'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d, want the wired recorder to have written one", n)
	}
}
