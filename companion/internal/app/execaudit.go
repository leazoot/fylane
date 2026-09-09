package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/store"
)

// execAuditor persists every command attempt. It is the only
// implementation of cmdexec.Auditor in the daemon, and the execution engine
// refuses to be constructed without one — an audit that a caller can forget
// to wire is not an audit.
type execAuditor struct {
	store *store.Store
	log   *slog.Logger
}

// execAuditTimeout bounds the write. Recording an attempt must not become a
// way to stall the command that produced it.
const execAuditTimeout = 5 * time.Second

// ExecAttempt records one attempt. It never returns an error to the caller:
// the command has already run (or already been refused), and there is nothing
// useful for the execution path to do about a failed insert except say so in
// the log.
func (a execAuditor) ExecAttempt(ctx context.Context, rec cmdexec.Record) {
	// The caller's context may already be cancelled — a backgrounded task
	// whose tool call hung up is the normal case — so the write gets its own.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execAuditTimeout)
	defer cancel()

	e := &store.ExecEvent{
		WorkspaceID: rec.WorkspaceID,
		DirRelative: rec.Dir,
		Argv:        rec.Argv,
		Provider:    rec.Provider,
		RunID:       rec.RunID,
		Outcome:     rec.Outcome,
		ExitCode:    rec.ExitCode,
		DurationMS:  rec.Duration.Milliseconds(),
		RuleID:      rec.Rule,
		Reason:      rec.Reason,
	}
	if err := a.store.AppendExecEvent(writeCtx, e); err != nil {
		// The argv is deliberately absent from this line: a log is not the
		// audit table, and a failed insert is no reason to start writing
		// command lines to disk somewhere else.
		a.log.Warn("recording command attempt", "outcome", rec.Outcome, "error", err)
	}
}
