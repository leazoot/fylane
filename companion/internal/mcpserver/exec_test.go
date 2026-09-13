package mcpserver

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/codeagent"
	"github.com/leazoot/fylane/companion/internal/redact"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// capturingAudit records what reached the audit layer.
type capturingAudit struct {
	mu   sync.Mutex
	recs []cmdexec.Record
}

func (c *capturingAudit) ExecAttempt(_ context.Context, rec cmdexec.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, rec)
}

func (c *capturingAudit) all() []cmdexec.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]cmdexec.Record(nil), c.recs...)
}

// scriptedApprover answers with a fixed decision and records the request.
type scriptedApprover struct {
	decision txn.Decision
	mu       sync.Mutex
	seen     []*txn.ApprovalRequest
}

func (a *scriptedApprover) Approve(_ context.Context, req *txn.ApprovalRequest) (txn.Decision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, req)
	return a.decision, nil
}

func (a *scriptedApprover) requests() []*txn.ApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*txn.ApprovalRequest(nil), a.seen...)
}

type execFixture struct {
	tools    *toolset
	audit    *capturingAudit
	approver *scriptedApprover
	manager  *tasks.Manager
	gate     *cmdgate.Gate
	store    *store.Store
	src      *workspace.Manager
	root     string
}

func newExecFixture(t *testing.T, decision txn.Decision) *execFixture {
	t.Helper()
	f := newExecFixtureAt(t, decision, cmdgate.Workspace)
	// Most tests are about the command path, not the rung, so the workspace
	// starts authorized: otherwise every one of them would first have to
	// answer the one-time question.
	rec, err := f.src.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.gate.Grant(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

func newExecFixtureAt(t *testing.T, decision txn.Decision, rung cmdgate.Rung) *execFixture {
	t.Helper()
	root := t.TempDir()
	src, st := testSource(t, root)
	audit := &capturingAudit{}
	approver := &scriptedApprover{decision: decision}
	manager := tasks.New(tasks.Options{Budget: 5 * time.Second})
	t.Cleanup(manager.Close)
	gate := cmdgate.New(st, string(rung), nil)

	return &execFixture{
		tools: &toolset{
			src:       src,
			provider:  "claude",
			exec:      cmdexec.New(audit, nil),
			tasks:     manager,
			approver:  approver,
			gate:      gate,
			execAudit: audit,
			runs:      st,
		},
		gate:     gate,
		audit:    audit,
		approver: approver,
		manager:  manager,
		store:    st,
		src:      src,
		root:     root,
	}
}

func (f *execFixture) run(t *testing.T, in runCommandInput) runCommandOutput {
	t.Helper()
	_, out, err := f.tools.runCommand(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("runCommand(%v): %v", in.Command, err)
	}
	return out
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixtures below run POSIX utilities")
	}
}

func TestRunCommandReturnsAShortCommandInOneRoundTrip(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})

	out := f.run(t, runCommandInput{Command: []string{"echo", "hello"}})
	if out.Status != "completed" || out.State != string(tasks.Succeeded) {
		t.Fatalf("got %+v", out)
	}
	if strings.TrimSpace(out.Stdout) != "hello" {
		t.Fatalf("got stdout %q", out.Stdout)
	}
	// An allowed command must not have asked anyone anything.
	if n := len(f.approver.requests()); n != 0 {
		t.Fatalf("an allowed command raised %d approvals", n)
	}
}

// success is the most common outcome and used to be the only one that
// sent no exit_code at all, so every platform tested inferred 0 from State
// instead of reading it. A finished command now always carries its code, and
// one that has not finished still carries none.
func TestRunCommandAlwaysReportsTheExitCodeOfAFinishedCommand(t *testing.T) {
	skipOnWindows(t)

	for _, tc := range []struct {
		name string
		argv []string
		want int
	}{
		{"success", []string{"echo", "hello"}, 0},
		// Not a shell one-liner: the rule table refuses shell interpreters,
		// so the failing case has to be a program that exits non-zero itself.
		{"failure", []string{"false"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecFixture(t, txn.Decision{Approved: true})
			out := f.run(t, runCommandInput{Command: tc.argv})
			if out.ExitCode == nil {
				t.Fatalf("a finished command reported no exit code: %+v", out)
			}
			if *out.ExitCode != tc.want {
				t.Fatalf("exit code %d, want %d", *out.ExitCode, tc.want)
			}
		})
	}
}

// End to end for what no rung waives (narrowed 2026-08-30): reading
// the store the machine keeps its secrets in. The open rung is the user
// saying not to be interrupted while work happens in a folder; handing over
// the environment is not that work, and no workspace grant covers it either.
func TestRunCommandAsksBeforeReadingTheCredentialStoreEvenOnTheOpenRung(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixtureAt(t, txn.Decision{Approved: false}, cmdgate.Open)

	out := f.run(t, runCommandInput{Command: []string{"printenv"}})
	if out.Status == "completed" {
		t.Fatalf("the environment was read without asking: %+v", out)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 {
		t.Fatalf("the open rung skipped the question: %d approvals raised", len(reqs))
	}
	// Nothing is being written, so a prompt labelled as a pending write would
	// ask the wrong question and offer a diff that does not exist.
	if reqs[0].Kind != txn.KindDisclosure {
		t.Fatalf("the prompt asks as kind %q, want %q", reqs[0].Kind, txn.KindDisclosure)
	}
	if !strings.Contains(reqs[0].Reason, "secrets") {
		t.Fatalf("the prompt does not say what is being handed over: %q", reqs[0].Reason)
	}
	if out.Rule != "reads-credential-store" {
		t.Fatalf("the user was asked without being told why: rule %q", out.Rule)
	}
	// Audited by the decision recorder, not here (app.audit.go). This asserts
	// the execution path stays out of it: both wrote a row until 2026-08-30,
	// and one refusal drew two lines on the task screen.
	if recs := f.audit.all(); len(recs) != 0 {
		t.Fatalf("the execution path audited a refusal it does not own: %+v", recs)
	}
}

// The change the user asked for, pinned: a process listing is ordinary
// development work and follows the rung. It is still gated at the rungs that
// gate — and still redacted and audited at every one of them.
func TestMachineStateFollowsTheRung(t *testing.T) {
	skipOnWindows(t)
	open := newExecFixtureAt(t, txn.Decision{Approved: false}, cmdgate.Open)
	if out := open.run(t, runCommandInput{Command: []string{"ps", "-eo", "pid"}}); out.Status != "completed" {
		t.Fatalf("the open rung still interrupted a process listing: %+v", out)
	}
	if n := len(open.approver.requests()); n != 0 {
		t.Fatalf("the open rung raised %d prompts for ordinary machine state", n)
	}

	gated := newExecFixtureAt(t, txn.Decision{Approved: false}, cmdgate.Workspace)
	out := gated.run(t, runCommandInput{Command: []string{"ps", "-eo", "pid"}})
	if out.Status == "completed" {
		t.Fatalf("a gating rung let a process listing through unasked: %+v", out)
	}
	if out.Rule != "reads-machine-state" {
		t.Fatalf("rule = %q", out.Rule)
	}
}

// The other half: a command that discloses nothing still runs untouched on
// the open rung. Without this the new tier could quietly become "ask about
// everything", which is the strict rung wearing a different name.
func TestRunCommandStillRunsOrdinaryWorkUnaskedOnTheOpenRung(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixtureAt(t, txn.Decision{Approved: true}, cmdgate.Open)

	out := f.run(t, runCommandInput{Command: []string{"echo", "hello"}})
	if out.Status != "completed" {
		t.Fatalf("got %+v", out)
	}
	if n := len(f.approver.requests()); n != 0 {
		t.Fatalf("ordinary work raised %d approvals on the open rung", n)
	}
}

// Redaction is wired into the reply the caller receives, not into what the
// local audit surface keeps. A secret that reaches the model is out of our
// hands the moment it is sent, so the check belongs on that path.
func TestRunCommandRedactsCredentialsOnTheWayOut(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})

	const secret = "eyJhIjoiZjkwZmQ3MTdiYTBiMWVjMDNjY2Y3YThjYzZlM2MzZmIifQ"
	out := f.run(t, runCommandInput{Command: []string{"echo", secret}})
	if strings.Contains(out.Stdout, secret) {
		t.Fatalf("a credential-shaped value was sent to the caller: %q", out.Stdout)
	}
	if !strings.Contains(out.Stdout, redact.Placeholder) {
		t.Fatalf("the caller was not told something was withheld: %q", out.Stdout)
	}
}

func TestRunCommandRefusesABlockedCommandAndAuditsIt(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})

	out := f.run(t, runCommandInput{Command: []string{"sudo", "rm", "-rf", "/"}})
	if out.Status != "refused" {
		t.Fatalf("got %+v", out)
	}
	if out.Rule != "privilege-escalation" {
		t.Fatalf("got rule %q", out.Rule)
	}
	if !strings.Contains(out.Action, "no approval rung overrides it") {
		t.Fatalf("the refusal does not say it is final: %q", out.Action)
	}
	// A blocked command must never reach the approver — offering to approve
	// it would turn a block into a confirm.
	if n := len(f.approver.requests()); n != 0 {
		t.Fatalf("a blocked command was offered for approval %d times", n)
	}
	recs := f.audit.all()
	if len(recs) != 1 || recs[0].Outcome != cmdexec.OutcomeRefused || recs[0].Rule != "privilege-escalation" {
		t.Fatalf("the refusal was not audited: %+v", recs)
	}
}

func TestRunCommandAsksBeforeAGatedCommand(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})

	out := f.run(t, runCommandInput{Command: []string{"rm", "-rf", "build"}})
	if out.Status != "completed" {
		t.Fatalf("got %+v", out)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d approval requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Rule != "recursive-delete" {
		t.Fatalf("the prompt does not say why: rule %q", req.Rule)
	}
	if req.Kind != txn.KindCommand {
		t.Fatalf("the prompt asks as kind %q, want %q", req.Kind, txn.KindCommand)
	}
	// The rule id is for the code; the sentence is for the person deciding.
	if !strings.Contains(req.Reason, "everything under it") {
		t.Fatalf("the prompt carries no reason a person can read: %q", req.Reason)
	}
	if len(req.Command) != 3 || req.Command[0] != "rm" {
		t.Fatalf("the prompt does not carry the command: %v", req.Command)
	}
	// The prompt keys off the command, so a retry re-attaches instead of
	// raising a second one.
	if !strings.HasPrefix(req.ChangeSetID, "cmd:") {
		t.Fatalf("approval key %q is not namespaced", req.ChangeSetID)
	}
}

func TestRunCommandStopsWhenTheUserDeclines(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: false, Reason: "user_rejected"})

	out := f.run(t, runCommandInput{Command: []string{"git", "push", "--force"}})
	if out.Status != "refused" {
		t.Fatalf("got %+v", out)
	}
	if out.Reason != "user_rejected" {
		t.Fatalf("got reason %q", out.Reason)
	}
	// The refusal is audited where the decision is made — one writer, so a
	// refusal answered after the platform stopped waiting is recorded once
	// and not twice (app.TestARefusedCommandKeepsItsArgv). Nothing is written
	// from here, and asserting an empty audit is what keeps the second writer
	// from coming back.
	if recs := f.audit.all(); len(recs) != 0 {
		t.Fatalf("the execution path audited a refusal it does not own: %+v", recs)
	}
}

func TestRunCommandDegradesToPendingWithoutDenying(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Pending: true, Reason: "approval_budget_exceeded"})

	out := f.run(t, runCommandInput{Command: []string{"rm", "-rf", "dist"}})
	if out.Status != "pending_approval" {
		t.Fatalf("got %+v", out)
	}
	// Pending is not a denial: the model must be told to come back, not that
	// it was refused.
	if !strings.Contains(out.Action, "call run_command again") {
		t.Fatalf("no retry instruction: %q", out.Action)
	}
	if out.State != "" || out.ExitCode != nil {
		t.Fatal("a pending command reported an execution result it never had")
	}
}

// The user's ceiling (settings › execution) binds what a caller asks for. It
// is a ceiling, not a default: a caller asking for less still gets less, and
// one asking for more does not get it.
func TestRunCommandIsBoundedByTheUsersCeiling(t *testing.T) {
	skipOnWindows(t)

	for _, tc := range []struct {
		name    string
		ceiling time.Duration
		asked   int
	}{
		{"the caller asked for more than the ceiling", 300 * time.Millisecond, 30},
		{"the caller asked for nothing at all", 300 * time.Millisecond, 0},
		{"the caller asked for less than the ceiling", 5 * time.Minute, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecFixture(t, txn.Decision{Approved: true})
			f.tools.taskCeiling = func() time.Duration { return tc.ceiling }

			out := f.run(t, runCommandInput{Command: []string{"sleep", "20"}, TimeoutSecs: tc.asked})
			if out.State != string(tasks.TimedOut) {
				t.Fatalf("the command was not stopped by a timeout: %+v", out)
			}
		})
	}
}

func TestRunCommandBackgroundsWorkThatOutlastsTheBudget(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})
	f.manager.Close()
	f.manager = tasks.New(tasks.Options{Budget: 100 * time.Millisecond})
	t.Cleanup(f.manager.Close)
	f.tools.tasks = f.manager

	out := f.run(t, runCommandInput{Command: []string{"sleep", "5"}, TimeoutSecs: 30})
	if out.Status != "running" {
		t.Fatalf("got %+v", out)
	}
	if out.TaskID == "" {
		t.Fatal("a running command must come back with something to poll")
	}
	if !strings.Contains(out.Action, "task_status") {
		t.Fatalf("no polling instruction: %q", out.Action)
	}
	// The other half of the same rule: a command that has not finished has no exit
	// code, and must not report one — otherwise "absent" would stop meaning
	// anything and a caller could read a running command as a clean success.
	if out.ExitCode != nil {
		t.Fatalf("a running command reported exit code %d", *out.ExitCode)
	}

	_, status, err := f.tools.taskStatus(context.Background(), nil, taskStatusInput{TaskID: out.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if status.TaskID != out.TaskID {
		t.Fatalf("task_status returned a different task: %s", status.TaskID)
	}
}

func TestTaskStatusRejectsAnUnknownID(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	if _, _, err := f.tools.taskStatus(context.Background(), nil, taskStatusInput{TaskID: "nope"}); err == nil {
		t.Fatal("an unknown task id was accepted")
	}
	if _, _, err := f.tools.taskStatus(context.Background(), nil, taskStatusInput{}); err == nil {
		t.Fatal("a missing task id was accepted")
	}
}

func TestRunCommandRefusesInAReadOnlyWorkspace(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	ctx := context.Background()
	rec, err := f.src.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec.Mode = store.ModeReadOnly
	if err := f.store.UpdateWorkspace(ctx, rec); err != nil {
		t.Fatal(err)
	}

	_, _, err = f.tools.runCommand(ctx, nil, runCommandInput{Command: []string{"echo", "hi"}})
	if err == nil {
		t.Fatal("a read-only workspace ran a command")
	}
	// There is no read-only subset of "run": a command can write, so the
	// workspace mode has to refuse it the same way it refuses write_file.
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("got %v", err)
	}
}

func TestRunCommandValidatesItsInput(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})

	for name, in := range map[string]runCommandInput{
		"no command":   {Command: nil},
		"too many":     {Command: append([]string{"echo"}, make([]string, maxCommandArgs)...)},
		"long timeout": {Command: []string{"echo"}, TimeoutSecs: 100000},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := f.tools.runCommand(context.Background(), nil, in); err == nil {
				t.Fatal("input was accepted")
			}
		})
	}
}

func TestRunCommandReportsAMissingProgramWithoutBackgrounding(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})

	// A command that can never start should come back as an error, not as a
	// task the model has to poll to discover was doomed.
	_, out, err := f.tools.runCommand(context.Background(), nil,
		runCommandInput{Command: []string{"fylane-definitely-not-installed"}})
	if err == nil && out.State == string(tasks.Succeeded) {
		t.Fatalf("a missing program reported success: %+v", out)
	}
	if err == nil && out.Reason == "" {
		t.Fatalf("a missing program produced neither an error nor a reason: %+v", out)
	}
}

func TestCommandKeyDistinguishesWhatMatters(t *testing.T) {
	base := commandKey("chatgpt", "ws", "", []string{"npm", "test"})
	same := commandKey("chatgpt", "ws", "", []string{"npm", "test"})
	if base != same {
		t.Fatal("the same command in the same place produced different keys")
	}
	for name, got := range map[string]string{
		// Two platforms asking the same thing are two callers, not one
		// retrying — the key that could not tell them apart let an approval
		// granted to one be replayed to the other.
		"provider":  commandKey("grok", "ws", "", []string{"npm", "test"}),
		"workspace": commandKey("chatgpt", "other", "", []string{"npm", "test"}),
		"directory": commandKey("chatgpt", "ws", "packages/web", []string{"npm", "test"}),
		"arguments": commandKey("chatgpt", "ws", "", []string{"npm", "run", "test"}),
		// Argument boundaries must matter, or ["rm","-rf x"] and
		// ["rm","-rf","x"] would share an approval.
		"boundaries": commandKey("chatgpt", "ws", "", []string{"npm", "te", "st"}),
	} {
		if got == base {
			t.Fatalf("%s did not change the key", name)
		}
	}
}

func TestTwoPlatformsAreTwoCallersNotOneRetrying(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixture(t, txn.Decision{Approved: true})

	// The same gated command, asked by two platforms in turn.
	f.tools.provider = "chatgpt"
	first := f.run(t, runCommandInput{Command: []string{"rm", "-rf", "build"}})
	f.tools.provider = "grok"
	second := f.run(t, runCommandInput{Command: []string{"rm", "-rf", "build"}})
	if first.Status != "completed" || second.Status != "completed" {
		t.Fatalf("runs = %+v, %+v", first, second)
	}

	reqs := f.approver.requests()
	if len(reqs) != 2 {
		t.Fatalf("the second platform was not asked: %d prompts", len(reqs))
	}
	// Distinct keys are what keeps the approval service from treating the
	// second platform as the first one retrying.
	if reqs[0].ChangeSetID == reqs[1].ChangeSetID {
		t.Fatalf("both platforms share approval key %q", reqs[0].ChangeSetID)
	}
	if reqs[0].Provider != "chatgpt" || reqs[1].Provider != "grok" {
		t.Fatalf("prompts attributed to %q and %q", reqs[0].Provider, reqs[1].Provider)
	}
	// And the work is two tasks, so one platform's poll cannot read output
	// from a command the other one started.
	if first.TaskID == second.TaskID {
		t.Fatalf("both platforms share task %q", first.TaskID)
	}
}

func TestTheFirstCommandInAnUnauthorizedWorkspaceIsTheOneTimeQuestion(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixtureAt(t, txn.Decision{Approved: true}, cmdgate.Workspace)

	out := f.run(t, runCommandInput{Command: []string{"echo", "hi"}})
	if out.Status != "completed" {
		t.Fatalf("got %+v", out)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 || !reqs[0].Grant {
		t.Fatalf("the first command did not ask the workspace question: %+v", reqs)
	}
	// Approving it authorizes the workspace, so the next ordinary command
	// runs without asking again — that is what "one-time" means.
	f.run(t, runCommandInput{Command: []string{"echo", "again"}})
	if n := len(f.approver.requests()); n != 1 {
		t.Fatalf("the grant did not stick: %d prompts", n)
	}
}

func TestADeclinedWorkspaceQuestionGrantsNothing(t *testing.T) {
	f := newExecFixtureAt(t, txn.Decision{Approved: false, Reason: "user_rejected"}, cmdgate.Workspace)

	if out := f.run(t, runCommandInput{Command: []string{"echo", "hi"}}); out.Status != "refused" {
		t.Fatalf("got %+v", out)
	}
	// A grant written before the answer would authorize a workspace the user
	// had just declined.
	rec, err := f.src.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CommandGrantFor(context.Background(), rec.ID); err == nil {
		t.Fatal("declining the question still authorized the workspace")
	}
}

func TestTheStrictRungAsksBeforeOrdinaryCommands(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixtureAt(t, txn.Decision{Approved: true}, cmdgate.Strict)

	f.run(t, runCommandInput{Command: []string{"echo", "hi"}})
	f.run(t, runCommandInput{Command: []string{"echo", "hi"}})
	if n := len(f.approver.requests()); n != 2 {
		t.Fatalf("strict asked %d times for 2 commands", n)
	}
	// Strict never hands out a grant, so it cannot quietly become the
	// middle rung after one approval.
	for _, req := range f.approver.requests() {
		if req.Grant {
			t.Fatal("the strict rung offered a workspace grant")
		}
	}
}

func TestTheOpenRungStillRefusesBlockedCommands(t *testing.T) {
	skipOnWindows(t)
	f := newExecFixtureAt(t, txn.Decision{Approved: true}, cmdgate.Open)

	// Open turns off asking, not checking.
	if out := f.run(t, runCommandInput{Command: []string{"sudo", "id"}}); out.Status != "refused" {
		t.Fatalf("the open rung ran a blocked command: %+v", out)
	}
	// A gated command runs without a prompt — that is what the rung is for.
	f.run(t, runCommandInput{Command: []string{"rm", "-rf", "build"}})
	if n := len(f.approver.requests()); n != 0 {
		t.Fatalf("the open rung asked %d times", n)
	}
}

// stubRegistry hands out one agent so the delegation path can be exercised
// without a real one installed.
type stubRegistry struct {
	agent codeagent.Agent
	err   error
}

func (s stubRegistry) Lookup(string) (codeagent.Agent, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.agent, nil
}
func (s stubRegistry) Installed() []string { return []string{"stub"} }

type stubAgent struct {
	mu      sync.Mutex
	task    codeagent.Task
	summary string
	err     error
}

func (a *stubAgent) Name() string     { return "stub" }
func (a *stubAgent) Available() error { return nil }
func (a *stubAgent) Run(_ context.Context, t codeagent.Task, out io.Writer) (codeagent.Result, error) {
	a.mu.Lock()
	a.task = t
	a.mu.Unlock()
	if a.err != nil {
		return codeagent.Result{}, a.err
	}
	io.WriteString(out, "working\n")
	return codeagent.Result{Summary: a.summary}, nil
}

func (a *stubAgent) seen() codeagent.Task {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.task
}

func (f *execFixture) withAgent(agent codeagent.Agent) *execFixture {
	f.tools.agents = stubRegistry{agent: agent}
	return f
}

func TestCodeTaskAsksBeforeDelegatingAndSaysWhatItCosts(t *testing.T) {
	agent := &stubAgent{summary: "done"}
	f := newExecFixture(t, txn.Decision{Approved: true}).withAgent(agent)

	_, out, err := f.tools.codeTask(context.Background(), nil,
		codeTaskInput{Prompt: "refactor the parser", Dir: ""})
	if err != nil {
		t.Fatal(err)
	}
	if out.TaskID == "" {
		t.Fatalf("no task to follow: %+v", out)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 {
		t.Fatalf("delegation asked %d times, want 1", len(reqs))
	}
	// A workspace grant covers commands, not handing the workspace to
	// another agent: that question is always asked.
	if reqs[0].Rule != delegationRule {
		t.Fatalf("got rule %q", reqs[0].Rule)
	}
	if !strings.Contains(reqs[0].Summary, "stub") {
		t.Fatalf("the prompt does not name the agent: %q", reqs[0].Summary)
	}
	if reqs[0].Kind != txn.KindDelegation {
		t.Fatalf("the prompt asks as kind %q, want %q", reqs[0].Kind, txn.KindDelegation)
	}
	// this is a term of the decision, so it has to reach the screen and not
	// only the MCP response the user never sees.
	if reqs[0].Reason != DelegationWarning {
		t.Fatalf("the prompt does not carry the delegation warning: %q", reqs[0].Reason)
	}
}

func TestOneYesToAnAgentLastsAWhileAndNoLonger(t *testing.T) {
	agent := &stubAgent{summary: "done"}
	f := newExecFixture(t, txn.Decision{Approved: true}).withAgent(agent)
	grants := cmdgate.NewDelegations()
	f.tools.delegations = grants
	ctx := context.Background()

	if _, _, err := f.tools.codeTask(ctx, nil, codeTaskInput{Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 || !reqs[0].Grant {
		t.Fatalf("the first delegation did not ask as the one-time question: %+v", reqs)
	}
	// The second task in the same workspace by the same agent runs on the
	// first yes; the user is not asked again.
	if _, out, err := f.tools.codeTask(ctx, nil, codeTaskInput{Prompt: "second"}); err != nil || out.TaskID == "" {
		t.Fatalf("second delegation: %+v %v", out, err)
	}
	if len(f.approver.requests()) != 1 {
		t.Fatalf("asked %d times, want 1", len(f.approver.requests()))
	}
	// Withdrawn, or run out: the question comes back.
	rec, err := f.src.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Revoke(rec.ID, "stub") {
		t.Fatal("nothing to withdraw: the yes was not recorded against this workspace and agent")
	}
	if _, _, err := f.tools.codeTask(ctx, nil, codeTaskInput{Prompt: "third"}); err != nil {
		t.Fatal(err)
	}
	if len(f.approver.requests()) != 2 {
		t.Fatalf("asked %d times after withdrawal, want 2", len(f.approver.requests()))
	}
}

func TestCodeTaskIsRefusedWhenTheUserDeclines(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: false, Reason: "user_rejected"}).
		withAgent(&stubAgent{})

	_, out, err := f.tools.codeTask(context.Background(), nil, codeTaskInput{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "refused" || out.TaskID != "" {
		t.Fatalf("a declined delegation still started: %+v", out)
	}
}

func TestCodeTaskRequiresAPromptAndAWritableWorkspace(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true}).withAgent(&stubAgent{})
	if _, _, err := f.tools.codeTask(context.Background(), nil, codeTaskInput{Prompt: "   "}); err == nil {
		t.Fatal("an empty prompt was accepted")
	}

	ctx := context.Background()
	rec, err := f.src.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec.Mode = store.ModeReadOnly
	if err := f.store.UpdateWorkspace(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.tools.codeTask(ctx, nil, codeTaskInput{Prompt: "do it"}); err == nil {
		t.Fatal("a read-only workspace accepted a coding agent")
	}
}

func TestCodeTaskReportsAnUnavailableAgentDirectly(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	f.tools.agents = stubRegistry{err: errors.New("opencode is not reachable")}

	_, _, err := f.tools.codeTask(context.Background(), nil, codeTaskInput{Prompt: "do it"})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("got %v; a missing agent must say so rather than fail silently", err)
	}
	// Nothing should have been asked of the user for a task that cannot run.
	if n := len(f.approver.requests()); n != 0 {
		t.Fatalf("an impossible delegation raised %d prompts", n)
	}
}

func TestCodeTaskPassesTheWorkspaceRootAndDirectoryThrough(t *testing.T) {
	agent := &stubAgent{summary: "ok"}
	f := newExecFixture(t, txn.Decision{Approved: true}).withAgent(agent)

	_, out, err := f.tools.codeTask(context.Background(), nil,
		codeTaskInput{Prompt: "work", Dir: "packages/web"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTask(t, f, out.TaskID)
	got := agent.seen()
	if got.Dir != "packages/web" || got.Prompt != "work" {
		t.Fatalf("got %+v", got)
	}
	if got.Root == "" {
		t.Fatal("the agent was given no workspace root")
	}
}

func waitForTask(t *testing.T, f *execFixture, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := f.manager.Status(id, 0, 0)
		if err == nil && s.State.Terminal() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not finish", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
