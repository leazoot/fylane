package mcpserver

import (
	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// The closed next-step vocabulary is mapped here rather than inside
// internal/nextstep so that the vocabulary package stays free of the domain
// packages, and so every emission site in this file can be read at once. The
// prose `action` on each result is unchanged; this is the machine-readable
// half of the same answer.

// changeStep maps a write result. Conflicts are read from the conflict's
// reason, which is already the wire value the caller sees.
func changeStep(res *txn.Result) nextstep.Step {
	switch res.Status {
	case txn.StatusApplied:
		return ""
	case txn.StatusPending:
		return nextstep.AskUser
	case txn.StatusDenied:
		return nextstep.Stop
	case txn.StatusFailed:
		// Not "the change did not happen". The engine restores what it
		// already applied, but a restore can fail too — apply() folds
		// "restore also failed" into the same error.
		//
		// The result also carries per-path effects, and most
		// failures now report every path as not_started. This still answers
		// Reobserve rather than gaining a fourth value for that case: the
		// vocabulary is closed on purpose (see the package comment), and
		// re-observing an unchanged world is wasted work, never a wrong
		// instruction. The precision lives in `effects` and in the `action`
		// sentence, which names the paths that were left changed and the
		// ones the engine could not determine.
		return nextstep.Reobserve
	case txn.StatusConflict:
		if res.Conflict != nil && res.Conflict.Reason == "base_hash_mismatch" {
			// The only conflict where the caller's request was well-formed
			// and the world moved instead. Everything else below is the
			// request being wrong about what exists.
			return nextstep.Reconcile
		}
		return nextstep.FixInput
	}
	return ""
}

// taskStep maps a task snapshot. A task that is still running is the one case
// where nothing has gone wrong and the caller still has work to do.
func taskStep(s tasks.Snapshot) nextstep.Step {
	switch s.State {
	case tasks.Running:
		return nextstep.Wait
	case tasks.Denied:
		return nextstep.Stop
	case tasks.TimedOut, tasks.Canceled, tasks.Interrupted:
		// The process was killed part-way, or died with the Core. Whatever
		// it had already written to disk is still there, and neither Fylane
		// nor the caller knows what that was — an interrupted install or
		// migration is the whole reason this value exists.
		return nextstep.Reobserve
	}
	// Succeeded and Failed are both complete answers: the command ran to its
	// own end and the exit code says which. A non-zero exit is the command's
	// result, not a Fylane failure, and needs no instruction.
	return ""
}
