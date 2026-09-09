package ctlapi

import (
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/tasks"
)

var base = time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)

func execEvent(argv []string, outcome string, at time.Time, ms int64) *store.ExecEvent {
	return &store.ExecEvent{Argv: argv, Outcome: outcome, Provider: "chatgpt",
		CreatedAt: at, DurationMS: ms}
}

// The task screen read the in-memory list only, so it forgot every command
// when the Core restarted and could never show one the user refused — a
// refusal never becomes a task (reported from a real machine, 2026-08-30).
func TestHistorySurvivesARestart(t *testing.T) {
	past := []*store.ExecEvent{
		execEvent([]string{"go", "test"}, "ok", base, 4000),
		execEvent([]string{"ps"}, "denied", base.Add(time.Minute), 0),
	}
	got := mergeHistory(nil, past)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want the audit log to stand in for a manager that forgot", len(got))
	}
	if got[0].Label != "ps" || got[0].State != tasks.Denied {
		t.Errorf("newest row = %q/%s, want the refused ps first", got[0].Label, got[0].State)
	}
	if got[1].Provider != "chatgpt" {
		t.Error("a history row cannot say who asked")
	}
}

func TestALiveRunIsNotListedTwice(t *testing.T) {
	// The same run is in both sources for as long as the manager keeps it.
	live := []tasks.Snapshot{{
		ID: "tsk_1", Label: "go test", Provider: "chatgpt", State: tasks.Succeeded,
		Stdout: "ok  fylane", StartedAt: base, Duration: 4 * time.Second,
	}}
	past := []*store.ExecEvent{execEvent([]string{"go", "test"}, "ok", base.Add(4*time.Second), 4000)}

	got := mergeHistory(live, past)
	if len(got) != 1 {
		t.Fatalf("rows = %d, want one run to appear once", len(got))
	}
	// The live one wins because it is the only one carrying output.
	if got[0].Stdout == "" || got[0].ID != "tsk_1" {
		t.Error("the merged row dropped the output only the live task has")
	}
}

func TestTwoRealRunsOfTheSameCommandBothSurvive(t *testing.T) {
	// Deduplication keys on when a run ended. Two builds minutes apart are
	// two rows, however identical their argv.
	live := []tasks.Snapshot{{
		ID: "tsk_1", Label: "go test", Provider: "chatgpt", State: tasks.Succeeded,
		StartedAt: base, Duration: time.Second,
	}}
	past := []*store.ExecEvent{
		execEvent([]string{"go", "test"}, "ok", base.Add(time.Second), 1000),
		execEvent([]string{"go", "test"}, "ok", base.Add(-10*time.Minute), 1000),
	}
	if got := mergeHistory(live, past); len(got) != 2 {
		t.Fatalf("rows = %d, want the earlier run kept", len(got))
	}
}

func TestARunningTaskIsNeverHiddenByHistory(t *testing.T) {
	live := []tasks.Snapshot{{
		ID: "tsk_1", Label: "sleep 60", Provider: "chatgpt", State: tasks.Running,
		StartedAt: base,
	}}
	got := mergeHistory(live, nil)
	if len(got) != 1 || got[0].State != tasks.Running {
		t.Fatalf("got %+v, want the running task", got)
	}
}

func TestOutcomesMapToStatesTheScreenKnows(t *testing.T) {
	for outcome, want := range map[string]tasks.State{
		"ok":      tasks.Succeeded,
		"timeout": tasks.TimedOut,
		"denied":  tasks.Denied,
		"refused": tasks.Denied,
		"failed":  tasks.Failed,
	} {
		if got := historyState(outcome); got != want {
			t.Errorf("%q -> %s, want %s", outcome, got, want)
		}
	}
}
