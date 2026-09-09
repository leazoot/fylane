package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/txn"
)

func journal(t *testing.T, st *store.Store, runID, outcome string, exit int) {
	t.Helper()
	e := &store.ExecEvent{Argv: []string{"npm", "install"}, Outcome: outcome,
		RunID: runID, Provider: "claude", ExitCode: exit}
	if err := st.AppendExecEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func (f *execFixture) status(t *testing.T, id string) (runCommandOutput, error) {
	t.Helper()
	_, out, err := f.tools.taskStatus(context.Background(), nil, taskStatusInput{TaskID: id})
	return out, err
}

// The task table is memory. After a restart the caller is still holding a
// task id from before, and answering "unknown task id" is not merely
// unhelpful — the command existed, it ran, and it may have changed the
// workspace.
func TestTaskStatusAnswersForACommandTheRestartInterrupted(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	journal(t, f.store, "run_1", store.OutcomeStarted, 0)
	journal(t, f.store, "run_1", store.OutcomeInterrupted, -1)

	out, err := f.status(t, "run_1")
	if err != nil {
		t.Fatalf("task_status: %v", err)
	}
	if out.State != string(tasks.Interrupted) {
		t.Errorf("state = %q, want %q", out.State, tasks.Interrupted)
	}
	if out.Next != nextstep.Reobserve {
		t.Errorf("next = %q, want %q", out.Next, nextstep.Reobserve)
	}
	if out.Next.EffectKnown() {
		t.Error("an interrupted command reported its effect as known")
	}
	if out.Action == "" {
		t.Error("nothing explains to the caller what interrupted means")
	}
	if out.ExitCode == nil || *out.ExitCode == 0 {
		t.Errorf("exit code = %v, want a code that does not read as success", out.ExitCode)
	}
}

// Output lived in the process that died. Reporting an empty stdout would say
// the command printed nothing, which is a different claim from not knowing.
func TestARecalledCommandDoesNotPretendToHaveOutput(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	journal(t, f.store, "run_1", store.OutcomeStarted, 0)
	journal(t, f.store, "run_1", store.OutcomeInterrupted, -1)

	out, err := f.status(t, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "" || out.Stderr != "" {
		t.Errorf("recalled output = %q / %q, want none", out.Stdout, out.Stderr)
	}
	if out.StdoutCursor != 0 || out.StderrCursor != 0 {
		t.Error("a recalled run handed back cursors into a stream that no longer exists")
	}
}

// A finished command is recalled too, with what actually happened. Only the
// interrupted case gets the warning sentence.
func TestARecalledCommandThatFinishedReportsHowItFinished(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	journal(t, f.store, "run_1", store.OutcomeStarted, 0)
	journal(t, f.store, "run_1", "ok", 0)

	out, err := f.status(t, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != string(tasks.Succeeded) {
		t.Errorf("state = %q, want %q", out.State, tasks.Succeeded)
	}
	if out.Next != "" {
		t.Errorf("next = %q, want no instruction for a command that simply finished", out.Next)
	}
	if out.Action != "" {
		t.Errorf("action = %q, want nothing for a command that simply finished", out.Action)
	}
}

// A run still open has only a 'started' row. Recalling it as finished would
// tell the caller its live command was over.
func TestAnOpenRunIsNotRecalledAsFinished(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	journal(t, f.store, "run_1", store.OutcomeStarted, 0)

	if _, err := f.status(t, "run_1"); !errors.Is(err, tasks.ErrUnknownTask) {
		t.Fatalf("err = %v, want the unknown-task error rather than an invented outcome", err)
	}
}

// An id that was never a run is still an error. The journal answers for work
// that happened, not for anything a caller cares to type.
func TestAnIDThatWasNeverARunIsStillUnknown(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})

	if _, err := f.status(t, "not_a_task"); !errors.Is(err, tasks.ErrUnknownTask) {
		t.Fatalf("err = %v, want the unknown-task error", err)
	}
}

// A live task must be answered from memory, which is the only place its
// output and cursors exist. Reading the journal first would silently drop
// both for every running command.
func TestALiveTaskIsStillAnsweredFromMemory(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})

	started := f.run(t, runCommandInput{Command: []string{"echo", "hello"}})
	if started.TaskID == "" {
		t.Fatal("the command produced no task id to poll")
	}
	out, err := f.status(t, started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != string(tasks.Succeeded) || out.Stdout == "" {
		t.Fatalf("live task = %+v, want the in-memory answer with its output", out)
	}
}

// The task id the caller polls has to be the id the journal files the run
// under. Two identifiers with a mapping between them is exactly the mapping a
// restart destroys.
func TestTheTaskIDAndTheJournalIDAreTheSameID(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})

	out := f.run(t, runCommandInput{Command: []string{"echo", "hello"}})
	if out.TaskID == "" {
		t.Fatal("no task id")
	}
	var journalled bool
	for _, rec := range f.audit.all() {
		if rec.RunID == out.TaskID {
			journalled = true
		}
	}
	if !journalled {
		t.Errorf("nothing was journalled under the task id %q the caller was handed", out.TaskID)
	}
}
