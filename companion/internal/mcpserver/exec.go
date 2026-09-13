package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/cmdrule"
	"github.com/leazoot/fylane/companion/internal/codeagent"
	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/redact"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// maxCommandArgs caps one argv. A command longer than this is not a build
// step; it is someone trying to fit a script into an argument list.
const maxCommandArgs = 64

// maxCommandTimeout bounds what a caller may ask for, independently of the
// engine's own clamp, so a model cannot pin a process open for half an hour
// by passing a large number.
const maxCommandTimeout = 15 * time.Minute

type runCommandInput struct {
	WorkspaceID string   `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Command     []string `json:"command" jsonschema:"The program followed by its arguments, as separate strings. Not a shell line: quoting, pipes, redirection, and && are not interpreted, and shell interpreters are refused."`
	Dir         string   `json:"dir,omitempty" jsonschema:"Workspace-relative directory to run in. Omit for the workspace root."`
	TimeoutSecs int      `json:"timeout_seconds,omitempty" jsonschema:"How long the command may run before it is killed. Default 50, maximum 900."`
}

type runCommandOutput struct {
	Status string `json:"status" jsonschema:"completed | running | pending_approval | refused"`
	TaskID string `json:"task_id,omitempty" jsonschema:"Poll task_status with this id when status is running."`
	State  string `json:"state,omitempty" jsonschema:"succeeded | failed | timed_out | canceled | interrupted | running. interrupted means Fylane stopped while the command was running: its effect is unknown."`

	// ExitCode is a pointer so that "the command exited 0" and "there is no
	// exit code yet" are different values on the wire. With a plain int and
	// omitempty the most common outcome — success — was the one case that
	// sent no field at all, and every platform tested had to infer 0 from
	// State instead of reading it.
	ExitCode        *int   `json:"exit_code,omitempty" jsonschema:"Process exit status. Present once the command has finished; 0 means success."`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutCursor    int    `json:"stdout_cursor,omitempty" jsonschema:"Pass to task_status to receive only later output."`
	StderrCursor    int    `json:"stderr_cursor,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	DurationMS      int64  `json:"duration_ms,omitempty"`

	// ReadBoundary appears on a failed command when this Companion is
	// bounding what its subprocesses may read. A denied read surfaces as the
	// program's own "operation not permitted", which reads like a broken
	// machine unless something says a boundary is in force.
	ReadBoundary string `json:"read_boundary,omitempty" jsonschema:"Present on a failed command when subprocess reads are bounded: says what was readable, in case the failure was a denied read."`

	// NetworkBoundary appears on the same terms for the outbound boundary. A
	// denied connect surfaces as a DNS or socket error from the program's own
	// library — measured, one of them reads "isc_socket_bind: unexpected
	// error" — which names nothing a caller could act on.
	NetworkBoundary string `json:"network_boundary,omitempty" jsonschema:"Present on a failed command when this workspace denies outbound network access, in case the failure was a refused connection."`

	Rule   string `json:"rule,omitempty" jsonschema:"Identifier of the safety rule that refused or gated this command."`
	Reason string `json:"reason,omitempty"`
	Action string `json:"action,omitempty" jsonschema:"What to do next when the command did not simply run."`
	// Next is the same answer as Action in a fixed vocabulary, for callers
	// that branch on it rather than read it.
	Next nextstep.Step `json:"next,omitempty" jsonschema:"What to do next, as a fixed value: wait | ask_user | stop | fix_input | reconcile | reobserve. Absent means the call succeeded and nothing further is needed."`
}

type taskStatusInput struct {
	TaskID       string `json:"task_id"`
	StdoutCursor int    `json:"stdout_cursor,omitempty" jsonschema:"Return only stdout after this offset; use the cursor from the previous response."`
	StderrCursor int    `json:"stderr_cursor,omitempty" jsonschema:"Return only stderr after this offset."`
}

// runCommand runs a command in a workspace, subject to the rule table and
// local approval.
//
// The status ladder exists because a tool call cannot outlive the platform's
// wall clock (~60s on ChatGPT and Grok). A command that finishes inside the
// budget returns "completed" in one round trip; one that does not returns
// "running" with a task id and keeps going.
// readBoundaryNote and networkBoundaryNote are said only when a command failed
// and the boundary in question was in force for it. Neither claims to have
// caused the failure — nothing here can know that — they say the boundary
// exists, which is the part the caller cannot find out any other way.
const readBoundaryNote = "this machine denies subprocess reads outside the workspace and the toolchain caches; if the command was reading somewhere else, that is why"

const networkBoundaryNote = "this workspace denies its commands outbound network access; if the command was trying to reach the network, that is why"

// reach is what the prompt states about this run's outbound network. Only the
// policy's own answer is needed, so the full workspace policy — which stats
// every toolchain cache — is not built for a word on a screen.
func (t *toolset) reach(ws *workspace.Workspace) string {
	return string(t.box.Reach(readbox.Policy{Network: ws.Network()}))
}

func (t *toolset) runCommand(ctx context.Context, _ *mcp.CallToolRequest, in runCommandInput) (*mcp.CallToolResult, runCommandOutput, error) {
	var zero runCommandOutput
	if t.exec == nil || t.tasks == nil || t.gate == nil {
		return nil, zero, fmt.Errorf("running commands is not available on this Companion")
	}
	switch {
	case len(in.Command) == 0:
		return nil, zero, fmt.Errorf("command must contain at least the program to run")
	case len(in.Command) > maxCommandArgs:
		return nil, zero, fmt.Errorf("command has %d arguments; the limit is %d", len(in.Command), maxCommandArgs)
	}

	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	// A command can write, so a read-only workspace refuses it for the same
	// reason it refuses write_file. There is no read-only subset of "run".
	if !ws.Writable() {
		return nil, zero, fmt.Errorf("workspace %q is read-only; commands can modify files, so they are not available in it", ws.Name())
	}

	rule := cmdrule.Check(cmdrule.Request{Argv: in.Command, Root: ws.Root(), Dir: in.Dir})
	gated, err := t.gate.Decide(ctx, ws.ID(), rule)
	if err != nil {
		return nil, zero, err
	}
	switch gated.Need {
	case cmdgate.Refused:
		t.auditExec(ctx, ws.ID(), in.Dir, in.Command, cmdexec.OutcomeRefused, rule)
		return nil, runCommandOutput{
			Status: "refused",
			Rule:   rule.Rule,
			Reason: rule.Reason,
			Action: "this command is refused by a local safety rule; no approval rung overrides it, so use a different command",
			Next:   nextstep.Stop,
		}, nil
	case cmdgate.Approval:
		out, ok, err := t.confirmCommand(ctx, ws, in, gated)
		if err != nil {
			return nil, zero, err
		}
		if !ok {
			return nil, out, nil
		}
	}

	timeout := time.Duration(in.TimeoutSecs) * time.Second
	if in.TimeoutSecs <= 0 {
		timeout = cmdexec.DefaultTimeout
	}
	if timeout > maxCommandTimeout {
		return nil, zero, fmt.Errorf("timeout_seconds may not exceed %d", int(maxCommandTimeout.Seconds()))
	}
	// The user's ceiling (settings › execution) binds whatever the caller
	// asked for. It is a ceiling and not a default: a model that wants less
	// still gets less, and one that wants more does not get it.
	if t.taskCeiling != nil {
		if max := t.taskCeiling(); max > 0 && timeout > max {
			timeout = max
		}
	}

	// One identifier for the task and for its journal rows. Keeping them the
	// same is what lets task_status answer after a restart: the caller holds
	// a task id, and the only thing that survived is a row filed under it.
	runID, err := tasks.NewID()
	if err != nil {
		return nil, zero, err
	}
	spec := cmdexec.Spec{
		WorkspaceID: ws.ID(),
		Root:        ws.Root(),
		Dir:         in.Dir,
		Argv:        in.Command,
		Provider:    t.provider,
		Timeout:     timeout,
		RunID:       runID,
		Network:     ws.Network(),
	}
	reach := t.reach(ws)
	// Resolution errors — an unknown program, a directory outside the
	// workspace — surface before anything is backgrounded, so the model gets
	// them as an error rather than having to poll to learn its command was
	// never going to run.
	snap, err := t.tasks.Run(ctx, tasks.Meta{
		ID:       runID,
		Key:      commandKey(t.provider, ws.ID(), in.Dir, in.Command),
		Label:    strings.Join(in.Command, " "),
		Dir:      in.Dir,
		Provider: t.provider,
		Network:  reach,
	}, func(runCtx context.Context, stdout, stderr io.Writer) (tasks.Outcome, error) {
		s := spec
		s.Stdout, s.Stderr = stdout, stderr
		res, err := t.exec.Run(runCtx, s)
		if err != nil {
			return tasks.Outcome{ExitCode: -1}, err
		}
		return tasks.Outcome{ExitCode: res.ExitCode, TimedOut: res.TimedOut}, nil
	})
	if err != nil {
		return nil, zero, err
	}
	return nil, t.fromSnapshot(snap), nil
}

// confirmCommand asks the local approver. It returns ok=true when the command
// may run; otherwise the output is what the caller should receive.
func (t *toolset) confirmCommand(ctx context.Context, ws *workspace.Workspace, in runCommandInput, gated cmdgate.Verdict) (runCommandOutput, bool, error) {
	if t.approver == nil {
		return runCommandOutput{}, false, fmt.Errorf("this command needs local approval, but no approver is configured")
	}
	// A grant prompt is keyed to the workspace, not the command: whichever
	// command triggered it, the user is being asked the same question, and a
	// retry must land on the prompt already on screen. It is also the one key
	// that stays free of the provider, because the answer is too: a workspace
	// grant authorizes the folder for every caller by design, so two
	// platforms racing to raise it are asking one question, not two.
	key := "cmd:" + commandKey(t.provider, ws.ID(), in.Dir, in.Command)
	if gated.Grant {
		key = "cmd-grant:" + ws.ID()
	}
	// A disclosure is a different question from "may this command run": nothing
	// is being changed, so the answer the user is giving is about data leaving
	// the machine. The prompt has to ask that one.
	kind := txn.KindCommand
	if gated.Rule.Verdict == cmdrule.Disclose {
		kind = txn.KindDisclosure
	}
	verdict, err := t.approver.Approve(ctx, &txn.ApprovalRequest{
		ChangeSetID:   key,
		WorkspaceID:   ws.ID(),
		WorkspaceName: ws.Name(),
		Provider:      t.provider,
		Summary:       strings.Join(in.Command, " "),
		Kind:          kind,
		Command:       in.Command,
		Dir:           in.Dir,
		Rule:          gated.Rule.Rule,
		Reason:        gated.Rule.Reason,
		Grant:         gated.Grant,
		Network:       t.reach(ws),
	})
	if err != nil {
		return runCommandOutput{}, false, err
	}
	decision := gated.Rule
	switch {
	case verdict.Approved:
		if gated.Grant {
			// Recorded after the answer, never before: a grant written
			// optimistically would authorize a workspace the user then
			// declined.
			if err := t.gate.Grant(ctx, ws.ID()); err != nil {
				return runCommandOutput{}, false, fmt.Errorf("recording the workspace authorization: %w", err)
			}
		}
		return runCommandOutput{}, true, nil
	case verdict.Pending:
		// Never a denial: the same call again re-attaches to the same
		// prompt rather than raising a second one.
		return runCommandOutput{
			Status: "pending_approval",
			Rule:   decision.Rule,
			Reason: decision.Reason,
			Action: "the command is waiting for local user approval; call run_command again with the same command to check the outcome",
			Next:   nextstep.AskUser,
		}, false, nil
	default:
		// The refusal is audited where the decision is made, not here. Both
		// wrote a row until 2026-08-30, and the platform's call is only
		// sometimes still waiting when the answer lands — so one refusal
		// produced one row or two depending on timing, and the task screen
		// drew it twice.
		return runCommandOutput{
			Status: "refused",
			Rule:   decision.Rule,
			Reason: verdict.Reason,
			Action: "the local user declined this command; do not retry it without being asked to",
			Next:   nextstep.Stop,
		}, false, nil
	}
}

// taskStatus reports on work that outlived the call that started it.
func (t *toolset) taskStatus(ctx context.Context, _ *mcp.CallToolRequest, in taskStatusInput) (*mcp.CallToolResult, runCommandOutput, error) {
	var zero runCommandOutput
	if t.tasks == nil {
		return nil, zero, fmt.Errorf("background tasks are not available on this Companion")
	}
	if in.TaskID == "" {
		return nil, zero, fmt.Errorf("task_id is required")
	}
	snap, err := t.tasks.Status(in.TaskID, in.StdoutCursor, in.StderrCursor)
	if err != nil {
		// The task table is memory, so a restart empties it while the caller
		// is still holding a task id from before. Answering "unknown task id"
		// there is not just unhelpful, it is wrong: the command existed, it
		// ran, and it may have changed the workspace.
		if past, found := t.recallRun(ctx, in.TaskID); found {
			return nil, past, nil
		}
		return nil, zero, err
	}
	return nil, t.fromSnapshot(snap), nil
}

// recallRun answers from the journal for a task the manager has forgotten.
// Output is not recoverable — it lived in the process that died — so the
// result carries the outcome and says so, rather than implying an empty
// command printed nothing.
func (t *toolset) recallRun(ctx context.Context, id string) (runCommandOutput, bool) {
	if t.runs == nil {
		return runCommandOutput{}, false
	}
	e, err := t.runs.FindRun(ctx, id)
	if err != nil || e == nil {
		return runCommandOutput{}, false
	}
	state := tasks.StateFromOutcome(e.Outcome)
	code := e.ExitCode
	out := runCommandOutput{
		Status:     "completed",
		TaskID:     id,
		State:      string(state),
		ExitCode:   &code,
		DurationMS: e.DurationMS,
		Reason:     e.Reason,
		Next:       taskStep(tasks.Snapshot{State: state}),
	}
	if state == tasks.Interrupted {
		out.Action = "Fylane restarted while this command was running; its output is gone and its effect is unknown, so check the workspace before acting on it"
	}
	return out, true
}

func (t *toolset) fromSnapshot(s tasks.Snapshot) runCommandOutput {
	out := runCommandOutput{
		Status: "completed",
		TaskID: s.ID,
		State:  string(s.State),
		// Redaction happens here, at the boundary where output leaves the
		// machine, and nowhere earlier: the local audit surface keeps the
		// real bytes because the user is entitled to see their own machine.
		// The cursors stay offsets into the unredacted stream, which is what
		// makes them safe to hand back — the caller only echoes them.
		Stdout:          redact.Text(s.Stdout),
		Stderr:          redact.Text(s.Stderr),
		StdoutCursor:    s.StdoutCursor,
		StderrCursor:    s.StderrCursor,
		StdoutTruncated: s.StdoutTruncated,
		StderrTruncated: s.StderrTruncated,
		DurationMS:      s.Duration.Milliseconds(),
		Reason:          s.Error,
	}
	if s.State.Terminal() {
		code := s.ExitCode
		out.ExitCode = &code
		if code != 0 && t.box.Enforcing() {
			out.ReadBoundary = readBoundaryNote
		}
		// Said on the run's own recorded answer, not on the workspace's
		// current one: a setting changed while a command was in flight must
		// not rewrite what that command ran under. "unbounded" is left silent
		// here — the workspace asked for no network and did not get it, so
		// naming a boundary that is not in force would be the false half of
		// the same sentence.
		if code != 0 && (s.Network == string(readbox.ReachDenied) || s.Network == string(readbox.ReachPartial)) {
			out.NetworkBoundary = networkBoundaryNote
		}
	} else {
		out.Status = "running"
		out.Action = "the command is still running; call task_status with this task_id and the returned cursors to get later output"
	}
	out.Next = taskStep(s)
	// A killed command is the one terminal state whose disk effect nobody
	// knows, and until now the result said so nowhere: the caller saw
	// state=timed_out with no instruction and no sentence.
	if out.Next == nextstep.Reobserve && out.Action == "" {
		out.Action = "the command was stopped part-way; anything it had already written is still there, so check the workspace before acting on this"
	}
	return out
}

// auditExec records an attempt that never reached the execution engine — the
// engine audits its own runs, but a command refused by the rule table or by
// the user has no engine call to hang a record on, and those are the ones
// most worth keeping.
func (t *toolset) auditExec(ctx context.Context, workspaceID, dir string, argv []string, outcome string, d cmdrule.Decision) {
	if t.execAudit == nil {
		return
	}
	t.execAudit.ExecAttempt(ctx, cmdexec.Record{
		WorkspaceID: workspaceID,
		Dir:         dir,
		Argv:        argv,
		Outcome:     outcome,
		Reason:      d.Reason,
		Rule:        d.Rule,
		Provider:    t.provider,
	})
}

// firstLine is a one-line label for a multi-line prompt, for the task list
// and the approval prompt.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

// commandKey identifies "this exact command, here, from this caller". Two
// uses: a retry that arrives while the work is still running re-attaches
// instead of starting a second copy of `npm install`, and a retry after a
// pending_approval re-attaches to the prompt the user is already looking at.
//
// The provider is part of the identity, and that is the whole point of it
// being first. Without it the key said only "this command in this
// folder", so two platforms asking the same thing were treated as one caller
// retrying: an approval the user granted to ChatGPT was replayed to Grok for
// the next fifteen minutes, and Grok's poll would have read a task ChatGPT
// started. Neither is a retry. The user approves a caller, not a string.
func commandKey(provider, workspaceID, dir string, argv []string) string {
	h := sha256.New()
	io.WriteString(h, provider)
	h.Write([]byte{0})
	io.WriteString(h, workspaceID)
	h.Write([]byte{0})
	io.WriteString(h, dir)
	for _, a := range argv {
		h.Write([]byte{0})
		io.WriteString(h, a)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

type codeTaskInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Prompt      string `json:"prompt" jsonschema:"What the agent should do, in full. It does not see this conversation, so include the context it needs."`
	Dir         string `json:"dir,omitempty" jsonschema:"Workspace-relative directory to work in. Omit for the workspace root."`
	Agent       string `json:"agent,omitempty" jsonschema:"Which local agent to delegate to. Omit to use the first one installed."`
	Model       string `json:"model,omitempty" jsonschema:"Override the agent's default model."`
}

// codeTask hands a whole piece of work to a coding agent installed on this
// machine and returns a task to poll.
//
// It is always backgrounded, never returned synchronously: a delegated task
// takes minutes, and the platform's wall clock is one. The one thing this
// path guarantees is that the user was asked first — and asked in terms that
// say what delegation costs, because after this point Fylane is not
// watching what the agent does.
func (t *toolset) codeTask(ctx context.Context, _ *mcp.CallToolRequest, in codeTaskInput) (*mcp.CallToolResult, runCommandOutput, error) {
	var zero runCommandOutput
	if t.agents == nil || t.tasks == nil || t.gate == nil {
		return nil, zero, fmt.Errorf("delegating to a coding agent is not available on this Companion")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return nil, zero, fmt.Errorf("prompt is required: the agent does not see this conversation")
	}

	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	if !ws.Writable() {
		return nil, zero, fmt.Errorf("workspace %q is read-only; a coding agent writes files, so it cannot run in it", ws.Name())
	}
	agent, err := t.agents.Lookup(in.Agent)
	if err != nil {
		return nil, zero, err
	}

	out, ok, err := t.confirmDelegation(ctx, ws, agent.Name(), in)
	if err != nil {
		return nil, zero, err
	}
	if !ok {
		return nil, out, nil
	}

	task := codeagent.Task{Prompt: in.Prompt, Root: ws.Root(), Dir: in.Dir, Model: in.Model,
		Network: ws.Network()}
	snap, err := t.tasks.Run(ctx, tasks.Meta{
		Key:      commandKey(t.provider, ws.ID(), in.Dir, []string{"code_task", agent.Name(), in.Prompt}),
		Label:    agent.Name() + ": " + firstLine(in.Prompt),
		Dir:      in.Dir,
		Provider: t.provider,
		// Delegation is never worth waiting out: returning a task id
		// immediately is strictly better than burning the platform's budget
		// to maybe catch a task that finished in under a minute.
		Budget: codeTaskBudget,
	}, func(runCtx context.Context, stdout, stderr io.Writer) (tasks.Outcome, error) {
		res, err := agent.Run(runCtx, task, stdout)
		if err != nil {
			return tasks.Outcome{ExitCode: -1}, err
		}
		if res.Summary != "" {
			fmt.Fprintln(stdout, res.Summary)
		}
		return tasks.Outcome{}, nil
	})
	if err != nil {
		return nil, zero, err
	}
	result := t.fromSnapshot(snap)
	if result.Status == "running" {
		result.Action = "the agent is working; call task_status with this task_id and the returned cursors to follow it"
	}
	return nil, result, nil
}

// codeTaskBudget is how long codeTask waits before backgrounding. Short on
// purpose: no delegated task finishes in a second, so waiting only delays the
// task id the caller needs.
const codeTaskBudget = 2 * time.Second

// confirmDelegation asks the user, in the terms delegation requires: this is not
// approval of one command but of an agent working unsupervised.
func (t *toolset) confirmDelegation(ctx context.Context, ws *workspace.Workspace, agent string, in codeTaskInput) (runCommandOutput, bool, error) {
	if t.approver == nil {
		return runCommandOutput{}, false, fmt.Errorf("delegation needs local approval, but no approver is configured")
	}
	// A yes given earlier today to this agent in this workspace still
	// stands (D37). The task itself is still recorded and still visible on
	// the tasks page; only the question is skipped.
	if t.delegations != nil && t.delegations.Granted(ws.ID(), agent) {
		return runCommandOutput{}, true, nil
	}
	summary := fmt.Sprintf("%s will work on: %s", agent, firstLine(in.Prompt))
	verdict, err := t.approver.Approve(ctx, &txn.ApprovalRequest{
		ChangeSetID:   "agent:" + commandKey(t.provider, ws.ID(), in.Dir, []string{agent, in.Prompt}),
		WorkspaceID:   ws.ID(),
		WorkspaceName: ws.Name(),
		Provider:      t.provider,
		Summary:       summary,
		Kind:          txn.KindDelegation,
		Command:       []string{agent, in.Prompt},
		Dir:           in.Dir,
		Rule:          delegationRule,
		Reason:        DelegationWarning,
		Network:       t.reach(ws),
		// The yes also covers this agent here for a while; the prompt says
		// so, the same way the command gate's one-time question does.
		Grant: t.delegations != nil,
	})
	if err != nil {
		return runCommandOutput{}, false, err
	}
	switch {
	case verdict.Approved:
		if t.delegations != nil {
			t.delegations.Grant(ws.ID(), agent)
		}
		return runCommandOutput{}, true, nil
	case verdict.Pending:
		return runCommandOutput{
			Status: "pending_approval",
			Rule:   delegationRule,
			Reason: DelegationWarning,
			Action: "delegation is waiting for local user approval; call code_task again with the same prompt to check the outcome",
			Next:   nextstep.AskUser,
		}, false, nil
	default:
		return runCommandOutput{
			Status: "refused",
			Rule:   delegationRule,
			Reason: verdict.Reason,
			Action: "the local user declined to delegate this task; do not retry it without being asked to",
			Next:   nextstep.Stop,
		}, false, nil
	}
}

// DelegationGrants remembers one yes to an agent in a workspace for a
// bounded time. Implemented by *cmdgate.Delegations.
type DelegationGrants interface {
	Granted(workspaceID, agent string) bool
	Grant(workspaceID, agent string) cmdgate.DelegationGrant
}

// delegationRule is the rule id the desktop keys the delegation prompt off.
// It is not a cmdrule entry: delegation is not a command the rule table can
// inspect, which is precisely why it always asks.
const delegationRule = "delegates-to-an-agent"

// DelegationWarning is shown with every delegation prompt. It is part of the
// decision, not copy: the user is agreeing to stop being told what
// happens next, and has to be told that in those words.
const DelegationWarning = "this agent will read, write, and run commands in this workspace on its own, using your permissions; Fylane does not review any step it takes"
