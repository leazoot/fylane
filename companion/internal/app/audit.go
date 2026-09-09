package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// decisionRecorder writes what the user decided, and how long the platform
// waited for it, into the local audit log. Latency is a Beta metric
// that never leaves the machine.
//
// Only a write approval names a real change set. Command, disclosure and
// delegation approvals are keyed by a synthetic id, and audit_events.change_set_id
// is a foreign key into change_sets with PRAGMA foreign_keys ON — so writing
// that id there made the insert fail, every time, for every approval that was
// not a write. The failure was a log line nobody reads: the local machine had
// no record that the user had approved running a command, while the command
// itself was audited and ran. Found on a real machine, 2026-08-30.
//
// The event type carries the kind because the row otherwise cannot say which
// of the four questions was answered; event_type is already a free-form
// vocabulary in this table ("create", "change_set_applied", …).
func decisionRecorder(st *store.Store, log *slog.Logger) func(*approval.Pending, bool, time.Duration) {
	audit := execAuditor{store: st, log: log}
	return func(p *approval.Pending, approved bool, wait time.Duration) {
		kind := p.Request.Kind
		if kind == "" {
			kind = txn.KindWrite
		}
		// A command the user refused belongs in the command audit, with its
		// argv. The execution path only records a refusal it saw itself, and
		// it stops watching once the platform's budget runs out — so a user
		// who took a minute to say no left no trace of what they said no to.
		if !approved && len(p.Request.Command) > 0 {
			audit.ExecAttempt(context.Background(), cmdexec.Record{
				WorkspaceID: p.Request.WorkspaceID,
				Dir:         p.Request.Dir,
				Argv:        p.Request.Command,
				Provider:    p.Request.Provider,
				Outcome:     "denied",
				ExitCode:    -1,
				Rule:        p.Request.Rule,
				Reason:      p.Request.Reason,
				Duration:    wait,
			})
		}
		e := &store.AuditEvent{
			EventType:  "approval_" + kind,
			Result:     "denied",
			DurationMS: wait.Milliseconds(),
		}
		if approved {
			e.Result = "approved"
		}
		if kind == txn.KindWrite {
			e.ChangeSetID = p.Request.ChangeSetID
		} else if len(p.Request.Operations) > 0 {
			// A sensitive read names the file it would have disclosed.
			e.PathRelative = p.Request.Operations[0].Path
		} else {
			// A command has no operations. Its argv is already in exec_events
			// with its own timestamp; what this row adds is that a human was
			// asked and what they said.
			e.PathRelative = p.Request.Dir
		}
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.AppendAuditEvent(dctx, e); err != nil {
			// Loud: a lost audit row is the one kind of loss the user cannot
			// discover later by looking somewhere else.
			log.Error("could not record an approval decision", "kind", kind, "error", err)
		}
	}
}

// newApprovals builds the approval service with everything it must not be
// started without: the prompt announcement, and the recorder that writes what
// the user decided.
//
// It exists as a function so the wiring can be tested. Assigned inline in
// Run() it could not be, and an OnDecision nobody assigned is silent — the
// refusal simply never reaches the audit log, which is how three of the four
// approval kinds went unrecorded until 2026-08-30.
func newApprovals(mode string, st *store.Store, log *slog.Logger, announce func(provider string)) (*approval.Service, error) {
	svc, err := approval.New(mode, approval.DefaultBudgets(), func(p *approval.Pending) {
		log.Info("approval requested",
			"change_set_id", p.Request.ChangeSetID,
			"workspace_id", p.Request.WorkspaceID,
			"operations", len(p.Request.Operations))
		announce(p.Request.Provider)
	})
	if err != nil {
		return nil, err
	}
	svc.OnDecision = decisionRecorder(st, log)
	return svc, nil
}
