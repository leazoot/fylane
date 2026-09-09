package ctlapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/tasks"
)

// historyLimit bounds what the task screen loads. Enough to cover weeks of
// ordinary use without reading a year of builds into memory.
const historyLimit = 200

// matchWindow is how far apart a live task's end and its audit row's timestamp
// may be and still describe the same run. The audit is written immediately
// after the process exits, so the gap is a database write.
const matchWindow = 2 * time.Second

// mergeHistory produces the task screen's list: everything that ever ran or
// was refused, newest first.
//
// Two sources, because neither is complete on its own. The in-memory manager
// is the only one holding a running task and the only one holding output —
// but it forgets everything when the Core restarts, and it never sees a
// command the user refused, because a refusal never becomes a task. The audit
// table remembers all of it and none of the output.
//
// So the audit is the spine and live tasks are laid over it: a live row wins
// wherever both describe the same run, since it carries strictly more.
func mergeHistory(live []tasks.Snapshot, past []*store.ExecEvent) []tasks.Snapshot {
	out := make([]tasks.Snapshot, 0, len(live)+len(past))
	out = append(out, live...)
	for _, e := range past {
		if matchesLive(live, e) {
			continue
		}
		out = append(out, fromExecEvent(e))
	}
	// Newest first, the order the screen groups by.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].StartedAt.After(out[i].StartedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// matchesLive reports whether a live task already describes this audit row.
// Argv is compared as the label the task carries, because that is what the
// manager was given; a run is identified by what ran and when it ended.
func matchesLive(live []tasks.Snapshot, e *store.ExecEvent) bool {
	label := strings.Join(e.Argv, " ")
	for _, t := range live {
		if t.Label != label || t.Provider != e.Provider {
			continue
		}
		ended := t.StartedAt.Add(t.Duration)
		if within(ended, e.CreatedAt, matchWindow) {
			return true
		}
	}
	return false
}

func within(a, b time.Time, d time.Duration) bool {
	gap := a.Sub(b)
	if gap < 0 {
		gap = -gap
	}
	return gap <= d
}

// fromExecEvent renders one audit row in the shape the task screen reads.
// There is no output: a run that already ended, possibly in a previous life
// of this process, has nothing left to stream. The id is the audit row's own,
// prefixed so it can never be mistaken for something the manager can cancel.
func fromExecEvent(e *store.ExecEvent) tasks.Snapshot {
	return tasks.Snapshot{
		ID:        "audit_" + strconv.FormatInt(e.ID, 10),
		State:     historyState(e.Outcome),
		Label:     strings.Join(e.Argv, " "),
		Dir:       e.DirRelative,
		Provider:  e.Provider,
		ExitCode:  e.ExitCode,
		Error:     e.Reason,
		StartedAt: e.CreatedAt.Add(-time.Duration(e.DurationMS) * time.Millisecond),
		Duration:  time.Duration(e.DurationMS) * time.Millisecond,
	}
}

// historyState maps an audit outcome onto the states the screen already
// knows. A command the user refused is not a failure of the command: it never
// ran, and the screen says so with the same word it uses for a rejected write.
func historyState(outcome string) tasks.State { return tasks.StateFromOutcome(outcome) }
