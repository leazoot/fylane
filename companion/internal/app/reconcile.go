package app

import (
	"context"
	"log/slog"

	"github.com/leazoot/fylane/companion/internal/store"
)

// reconcileRuns answers every command that was running when the Core last
// stopped.
//
// The Core's call never returns and never writes an outcome, so nothing
// closes the row. What became of the command itself depends on the platform:
// on Windows the job object is set to kill on close and takes the tree down
// with the Core, while on Unix the child sits in its own process group and
// nothing signals it, so it may well still be running when the next start
// answers it. That is a further reason the answer is "interrupted" rather
// than "failed" — the row is being closed by something that does not know.
//
// Before this ran, an interrupted command left no trace at all: the in-memory task table was gone, the audit log held only
// the 'started' row nobody looked at, the task screen showed nothing, and
// task_status answered "unknown task id" for work that had really run and
// really may have written to disk.
//
// Each orphan gets an 'interrupted' row. The table stays append-only — the
// orphan is answered, not edited — and running twice is harmless because the
// answer is itself terminal, so the second pass finds nothing.
//
// It never fails startup. A Companion that will not start because it could
// not annotate history is worse than one that starts with history it cannot
// fully explain.
func reconcileRuns(ctx context.Context, st *store.Store, log *slog.Logger) {
	orphans, err := st.OrphanedRuns(ctx)
	if err != nil {
		log.Error("could not look for commands interrupted by a restart", "error", err)
		return
	}
	for _, o := range orphans {
		// Duration is deliberately not carried over from the 'started' row:
		// it is zero there, and the elapsed wall-clock since then measures
		// how long Fylane was off, not how long the command ran. Reporting
		// that as a duration would be a made-up number.
		e := &store.ExecEvent{
			WorkspaceID: o.WorkspaceID,
			DirRelative: o.DirRelative,
			Argv:        o.Argv,
			Provider:    o.Provider,
			RunID:       o.RunID,
			Outcome:     store.OutcomeInterrupted,
			ExitCode:    -1,
			Reason:      "Fylane stopped while this command was running; whether it finished, and what it left behind, is not known",
		}
		if err := st.AppendExecEvent(ctx, e); err != nil {
			log.Error("could not record an interrupted command", "error", err)
			return
		}
	}
	if len(orphans) > 0 {
		log.Info("recorded commands interrupted by a restart", "count", len(orphans))
	}
}
