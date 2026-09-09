package mcpserver

import (
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// A denied read reaches the caller as the program's own "operation not
// permitted", which reads like a broken machine. The note is the only way a
// caller can find out that a boundary exists at all.
func TestAFailedCommandSaysWhetherReadsAreBounded(t *testing.T) {
	box := readbox.New(true)
	if !box.Enforcing() {
		t.Skipf("no read boundary on this machine: %s", box.Why())
	}
	bounded := &toolset{box: box}
	unbounded := &toolset{}

	failed := tasks.Snapshot{ID: "tsk_1", State: tasks.Failed, ExitCode: 1}
	if got := bounded.fromSnapshot(failed).ReadBoundary; got == "" {
		t.Fatal("a failed command said nothing about the boundary in force")
	}
	if got := unbounded.fromSnapshot(failed).ReadBoundary; got != "" {
		t.Fatalf("a Companion with no boundary claimed one: %q", got)
	}

	// It appears on failure only. Saying it after every successful command
	// would be noise, and noise is how a line that matters stops being read.
	ok := tasks.Snapshot{ID: "tsk_1", State: tasks.Succeeded, ExitCode: 0}
	if got := bounded.fromSnapshot(ok).ReadBoundary; got != "" {
		t.Fatalf("a successful command carried the boundary note: %q", got)
	}

	// And it does not claim to have caused this failure, because nothing here
	// can know that.
	note := bounded.fromSnapshot(failed).ReadBoundary
	for _, forbidden := range []string{"caused", "because of this", "was blocked"} {
		if strings.Contains(note, forbidden) {
			t.Fatalf("the note claims more than it knows: %q", note)
		}
	}
}

// The same three rules for the outbound boundary, and one more that is only
// true of this one: the note is said on the answer the run recorded, not on
// the workspace's current setting, and "unbounded" is silent because there is
// no boundary to point at.
func TestAFailedCommandSaysWhetherTheNetworkWasBounded(t *testing.T) {
	tools := &toolset{}
	failed := func(reach readbox.Reach) tasks.Snapshot {
		return tasks.Snapshot{ID: "tsk_1", State: tasks.Failed, ExitCode: 1, Network: string(reach)}
	}

	for _, reach := range []readbox.Reach{readbox.ReachDenied, readbox.ReachPartial} {
		if got := tools.fromSnapshot(failed(reach)).NetworkBoundary; got == "" {
			t.Errorf("a command that failed under %s said nothing about it", reach)
		}
	}
	for _, reach := range []readbox.Reach{readbox.ReachAllowed, readbox.ReachUnbounded, ""} {
		if got := tools.fromSnapshot(failed(reach)).NetworkBoundary; got != "" {
			t.Errorf("reach %q carried a boundary note: %q", reach, got)
		}
	}

	ok := tasks.Snapshot{ID: "tsk_1", State: tasks.Succeeded, ExitCode: 0,
		Network: string(readbox.ReachDenied)}
	if got := tools.fromSnapshot(ok).NetworkBoundary; got != "" {
		t.Errorf("a successful command carried the note: %q", got)
	}

	note := tools.fromSnapshot(failed(readbox.ReachDenied)).NetworkBoundary
	for _, forbidden := range []string{"caused", "because of this", "was blocked"} {
		if strings.Contains(note, forbidden) {
			t.Fatalf("the note claims more than it knows: %q", note)
		}
	}
}

// The prompt states what the run gets and never asks about it. Two questions
// in one prompt is the merge the approval rules forbid, and nothing here could answer the
// second one anyway: whether a command needs the network is not knowable
// before it runs.
func TestThePromptStatesTheReachWithoutAskingAboutIt(t *testing.T) {
	f := newExecFixtureAt(t, txn.Decision{Approved: true}, cmdgate.Strict)
	f.tools.box = readbox.New(true)

	deny(t, f, true)
	if _, _, err := f.tools.runCommand(t.Context(), nil, runCommandInput{Command: []string{"true"}}); err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	reqs := f.approver.requests()
	if len(reqs) == 0 {
		t.Fatal("the command never reached an approval prompt")
	}
	got := reqs[len(reqs)-1].Network
	// Whatever this machine can do, a denying workspace is never reported to
	// the prompt as allowed — that is the one answer that would be false.
	if got == "" || got == string(readbox.ReachAllowed) {
		t.Fatalf("the prompt said %q for a workspace that denies the network", got)
	}

	// And the prompt asks one question. A second decision about the network
	// would show up as a second request for the same command.
	before := len(f.approver.requests())
	deny(t, f, false)
	if _, _, err := f.tools.runCommand(t.Context(), nil, runCommandInput{Command: []string{"true"}}); err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	reqs = f.approver.requests()
	if len(reqs)-before != 1 {
		t.Fatalf("one command raised %d prompts", len(reqs)-before)
	}
	if reqs[len(reqs)-1].Network != string(readbox.ReachAllowed) {
		t.Errorf("a workspace that allows the network said %q", reqs[len(reqs)-1].Network)
	}
}

// deny records the workspace's answer the way the settings page does, through
// the manager, so the test exercises the path the product uses.
func deny(t *testing.T, f *execFixture, on bool) {
	t.Helper()
	rec, err := f.src.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.src.SetNetwork(t.Context(), rec.ID, !on); err != nil {
		t.Fatal(err)
	}
}
