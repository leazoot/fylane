package mcpserver

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/txn"
)

func TestChangeStatusesMapToTheDocumentedStep(t *testing.T) {
	for _, c := range []struct {
		name string
		res  txn.Result
		want nextstep.Step
	}{
		{"applied says nothing", txn.Result{Status: txn.StatusApplied}, ""},
		{"pending waits on a person", txn.Result{Status: txn.StatusPending}, nextstep.AskUser},
		{"denied is final", txn.Result{Status: txn.StatusDenied}, nextstep.Stop},
		{"a moved file is reconciled", txn.Result{Status: txn.StatusConflict,
			Conflict: &txn.Conflict{Reason: "base_hash_mismatch"}}, nextstep.Reconcile},
		{"a missing hash is bad input", txn.Result{Status: txn.StatusConflict,
			Conflict: &txn.Conflict{Reason: "expected_sha256_missing"}}, nextstep.FixInput},
		{"an existing path is bad input", txn.Result{Status: txn.StatusConflict,
			Conflict: &txn.Conflict{Reason: "already_exists"}}, nextstep.FixInput},
		{"a missing target is bad input", txn.Result{Status: txn.StatusConflict,
			Conflict: &txn.Conflict{Reason: "target_missing"}}, nextstep.FixInput},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := changeStep(&c.res); got != c.want {
				t.Errorf("changeStep = %q, want %q", got, c.want)
			}
		})
	}
}

// The engine's apply() restores what it already wrote and folds "restore also
// failed" into the same error, so a failed change set is the case where the
// disk genuinely may hold a partial write. Calling it anything more definite
// would be a promise the result cannot keep.
func TestAFailedChangeSetDoesNotClaimTheDiskIsUntouched(t *testing.T) {
	out := toChangeOutput(&txn.Result{Status: txn.StatusFailed, Reason: "creating a.txt: disk full (restore also failed: permission denied)"})
	if out.Next != nextstep.Reobserve {
		t.Fatalf("next = %q, want %q", out.Next, nextstep.Reobserve)
	}
	if out.Next.EffectKnown() {
		t.Error("a failed change set reported its effect as known")
	}
	if out.Action == "" {
		t.Error("the failed result carries a machine step but no sentence explaining it")
	}
}

// A conflict raised before the engine is reached was built by hand at two
// call sites, which is how a field gets added in one place and forgotten in
// the other. This goes through the real tool so the reroute is what is being
// tested, not a mapper called directly.
func TestAConflictRaisedBeforeTheEngineCarriesTheStepToo(t *testing.T) {
	session, root := startSession(t)
	old := "line one\nline two\nline three\n"
	writeTree(t, root, map[string]string{"a.txt": old})
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n line one\n-line two\n+line 2\n line three\n"

	var out changeOutput
	structured(t, callTool(t, session, "apply_patch", map[string]any{
		"path": "a.txt", "patch": patch, "expected_sha256": shaOf(old),
	}), &out)
	if out.Status != txn.StatusApplied {
		t.Fatalf("setup patch = %+v", out)
	}
	// The same call again: the file is no longer at the hash it was planned
	// against, which is the one conflict the caller can resolve by re-reading.
	structured(t, callTool(t, session, "apply_patch", map[string]any{
		"path": "a.txt", "patch": patch, "expected_sha256": shaOf(old),
	}), &out)
	if out.Conflict == nil || out.Conflict.Reason != "base_hash_mismatch" {
		t.Fatalf("stale patch = %+v", out)
	}
	if out.Next != nextstep.Reconcile {
		t.Errorf("next = %q, want %q", out.Next, nextstep.Reconcile)
	}
	if out.Action == "" {
		t.Error("the conflict's own sentence was dropped by the reroute")
	}
}

// Patching a file that is not there is the other conflict the pre-engine
// path raises, and it is deliberately not Reconcile: re-reading a file that
// does not exist tells the caller nothing. The request named the wrong thing.
//
// (The third pre-engine branch, expected_sha256_missing, is unreachable from
// the MCP surface — the field has no omitempty, so the generated schema makes
// it required and the SDK rejects the call first. It stays as depth behind
// the schema rather than being deleted, and it is exercised at the mapper.)
func TestPatchingAMissingFileIsBadInputNotSomethingToReconcile(t *testing.T) {
	session, _ := startSession(t)

	var out changeOutput
	structured(t, callTool(t, session, "apply_patch", map[string]any{
		"path":            "nope.txt",
		"patch":           "--- a/nope.txt\n+++ b/nope.txt\n@@ -1,1 +1,1 @@\n-x\n+y\n",
		"expected_sha256": shaOf("x\n"),
	}), &out)
	if out.Conflict == nil || out.Conflict.Reason != "target_missing" {
		t.Fatalf("patch on a missing file = %+v", out)
	}
	if out.Next != nextstep.FixInput {
		t.Errorf("next = %q, want %q", out.Next, nextstep.FixInput)
	}
}

func TestTaskStatesMapToTheDocumentedStep(t *testing.T) {
	for state, want := range map[tasks.State]nextstep.Step{
		tasks.Running:   nextstep.Wait,
		tasks.Succeeded: "",
		tasks.Failed:    "",
		tasks.TimedOut:  nextstep.Reobserve,
		tasks.Canceled:  nextstep.Reobserve,
		tasks.Denied:    nextstep.Stop,
	} {
		if got := taskStep(tasks.Snapshot{State: state}); got != want {
			t.Errorf("%s -> %q, want %q", state, got, want)
		}
	}
}

// A command that exited non-zero ran to its own end: the exit code is the
// answer and there is nothing for Fylane to instruct. Telling the caller to
// "fix input" because a test suite failed would be the tool inventing a
// diagnosis it does not have.
func TestANonZeroExitIsAResultNotAnInstruction(t *testing.T) {
	out := (&toolset{}).fromSnapshot(tasks.Snapshot{ID: "tsk_1", State: tasks.Failed, ExitCode: 1,
		Stderr: "2 tests failed", Duration: time.Second})
	if out.Next != "" {
		t.Errorf("next = %q, want no instruction", out.Next)
	}
}

// The one that matters. A killed command has written whatever it had written,
// and neither side knows what that was.
func TestAKilledCommandTellsTheCallerTheEffectIsUnknown(t *testing.T) {
	for _, state := range []tasks.State{tasks.TimedOut, tasks.Canceled} {
		out := (&toolset{}).fromSnapshot(tasks.Snapshot{ID: "tsk_1", State: state, ExitCode: -1,
			Duration: 50 * time.Second})
		if out.Next != nextstep.Reobserve {
			t.Errorf("%s: next = %q, want %q", state, out.Next, nextstep.Reobserve)
		}
		if out.Next.EffectKnown() {
			t.Errorf("%s: reported its effect as known", state)
		}
		if out.Action == "" {
			t.Errorf("%s: carries a machine step but no sentence explaining it", state)
		}
	}
}

func TestARunningTaskIsToldToPoll(t *testing.T) {
	out := (&toolset{}).fromSnapshot(tasks.Snapshot{ID: "tsk_1", State: tasks.Running})
	if out.Next != nextstep.Wait {
		t.Fatalf("next = %q, want %q", out.Next, nextstep.Wait)
	}
	if out.ExitCode != nil {
		t.Error("a running task reported an exit code")
	}
}

// Every step this package can emit has to be one the vocabulary admits.
// Without this, a typo in a mapping ships a value no caller can branch on.
func TestNoMappingEmitsAValueOutsideTheVocabulary(t *testing.T) {
	for _, status := range []string{txn.StatusApplied, txn.StatusConflict, txn.StatusDenied,
		txn.StatusFailed, txn.StatusPending, "some_future_status"} {
		for _, conflict := range []*txn.Conflict{nil, {Reason: "base_hash_mismatch"}, {Reason: "target_missing"}} {
			got := changeStep(&txn.Result{Status: status, Conflict: conflict})
			if got != "" && !got.Valid() {
				t.Errorf("status %q conflict %v emitted %q, which is not in the vocabulary", status, conflict, got)
			}
		}
	}
	for _, state := range []tasks.State{tasks.Running, tasks.Succeeded, tasks.Failed,
		tasks.TimedOut, tasks.Canceled, tasks.Denied, "some_future_state"} {
		got := taskStep(tasks.Snapshot{State: state})
		if got != "" && !got.Valid() {
			t.Errorf("state %q emitted %q, which is not in the vocabulary", state, got)
		}
	}
}

// Success is the most common result and the one whose payload we measured
// against three platform ceilings. It must not grow a field.
func TestASuccessfulResultSendsNoNextField(t *testing.T) {
	applied, err := json.Marshal(toChangeOutput(&txn.Result{Status: txn.StatusApplied, ChangeSetID: "chg_1"}))
	if err != nil {
		t.Fatal(err)
	}
	if bytesContain(applied, "next") {
		t.Errorf("an applied change set carries a next field: %s", applied)
	}
	done, err := json.Marshal((&toolset{}).fromSnapshot(tasks.Snapshot{ID: "tsk_1", State: tasks.Succeeded}))
	if err != nil {
		t.Fatal(err)
	}
	if bytesContain(done, `"next"`) {
		t.Errorf("a succeeded task carries a next field: %s", done)
	}
}

func bytesContain(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
