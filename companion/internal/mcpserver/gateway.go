package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/cmdrule"
	"github.com/leazoot/fylane/companion/internal/mcpgate"
	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/redact"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// The local MCP gateway. One meta-tool rather than N proxied tools
// registered alongside Fylane's own: a platform's tool list is the only place
// a model learns what it is allowed to trust, and tools Fylane vets must not
// sit in it indistinguishable from tools Fylane merely forwards. The name of
// the tool being called is where that boundary is visible.

// Gateway actions.
const (
	actionListProviders = "list_providers"
	actionListTools     = "list_tools"
	actionCallTool      = "call_tool"
)

// gatewayRule is the rule id the desktop prompt keys off. Like the delegation
// rule it is not a cmdrule entry, and for the same reason: a proxied call is
// not a command the table can inspect, which is exactly why it asks.
const gatewayRule = "proxied-tool-is-opaque"

// GatewayWarning is shown with every gateway prompt at the Ask trust. It is
// part of the decision, not copy: the user is approving an operation whose
// effect Fylane could not determine, and has to be told that in those words.
const GatewayWarning = "Fylane cannot tell what this tool does — it is not one of Fylane's own, so the rule table, the path sandbox, and the diff preview do not apply to it"

type gatewayInput struct {
	Action      string         `json:"action" jsonschema:"list_providers | list_tools | call_tool"`
	Provider    string         `json:"provider,omitempty" jsonschema:"Which configured provider to talk to. Required for list_tools and call_tool."`
	Tool        string         `json:"tool,omitempty" jsonschema:"Which of the provider's tools to call. Required for call_tool."`
	Arguments   map[string]any `json:"arguments,omitempty" jsonschema:"Arguments for that tool, matching the input_schema list_tools returned."`
	WorkspaceID string         `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info. The provider is started in this workspace."`
}

type gatewayProvider struct {
	Name string `json:"name"`
	// Trust says whether calls to this provider stop for the local user every
	// time. The caller is told so it can plan, never so it can change it.
	Trust string `json:"trust"`
	// Installed is false when the program is not on this machine right now.
	Installed bool   `json:"installed"`
	Reason    string `json:"reason,omitempty"`
}

type gatewayOutput struct {
	Status    string            `json:"status" jsonschema:"completed | pending_approval | refused"`
	Providers []gatewayProvider `json:"providers,omitempty"`
	Tools     []mcpgate.Tool    `json:"tools,omitempty"`
	// Content is the proxied tool's textual answer.
	Content string `json:"content,omitempty"`
	// ToolError reports that the provider itself refused or failed the call,
	// as opposed to Fylane refusing to forward it. Keeping the two apart is
	// what stops a model from retrying a call the user declined.
	ToolError bool          `json:"tool_error,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	Rule      string        `json:"rule,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	Action    string        `json:"action,omitempty"`
	Next      nextstep.Step `json:"next,omitempty" jsonschema:"What to do next, as a fixed value: wait | ask_user | stop | fix_input | reconcile | reobserve. Absent means the call succeeded and nothing further is needed."`
}

// mcpGateway forwards one request to an MCP server installed on this machine.
func (t *toolset) mcpGateway(ctx context.Context, _ *mcp.CallToolRequest, in gatewayInput) (*mcp.CallToolResult, gatewayOutput, error) {
	var zero gatewayOutput
	if t.providers == nil {
		return nil, zero, fmt.Errorf("no local MCP providers are configured on this Companion")
	}
	switch in.Action {
	case actionListProviders:
		return nil, t.gatewayProviders(), nil
	case actionListTools, actionCallTool:
	case "":
		return nil, zero, fmt.Errorf("action is required: %s, %s, or %s", actionListProviders, actionListTools, actionCallTool)
	default:
		return nil, zero, fmt.Errorf("unknown action %q; use %s, %s, or %s", in.Action, actionListProviders, actionListTools, actionCallTool)
	}

	p, err := t.providers.Lookup(in.Provider)
	if err != nil {
		return nil, zero, err
	}
	if err := p.Available(); err != nil {
		return nil, zero, fmt.Errorf("provider %q: %w", p.Name, err)
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	// A proxied tool can write, and nothing here can prove otherwise, so a
	// read-only workspace refuses it for the same reason it refuses write_file.
	if !ws.Writable() {
		return nil, zero, fmt.Errorf("workspace %q is read-only; a proxied tool may modify files, so the gateway is not available in it", ws.Name())
	}

	if in.Action == actionListTools {
		out, err := t.gatewayTools(ctx, p, ws)
		return nil, out, err
	}
	if strings.TrimSpace(in.Tool) == "" {
		return nil, zero, fmt.Errorf("tool is required for %s; call %s first", actionCallTool, actionListTools)
	}
	out, err := t.gatewayCall(ctx, p, ws, in)
	return nil, out, err
}

// gatewayProviders answers what is configured. It never reports the program
// behind a provider: which binaries are installed here is machine state, and
// the caller's business is only whether the name works.
func (t *toolset) gatewayProviders() gatewayOutput {
	out := gatewayOutput{Status: "completed"}
	for _, p := range t.providers.List() {
		info := gatewayProvider{Name: p.Name, Trust: string(p.Trusted()), Installed: true}
		if err := p.Available(); err != nil {
			info.Installed = false
			info.Reason = err.Error()
		}
		out.Providers = append(out.Providers, info)
	}
	if len(out.Providers) == 0 {
		out.Action = "no MCP providers are configured; the local user adds them in the Companion's settings"
	}
	return out
}

// gatewayTools lists a provider's tools. Listing is not gated: it starts the
// server and reads its manifest, which is the one gateway operation whose
// effect is known — and a caller that cannot see the schema can only guess at
// arguments, which makes every later call worse.
func (t *toolset) gatewayTools(ctx context.Context, p mcpgate.Provider, ws *workspace.Workspace) (gatewayOutput, error) {
	sess, err := mcpgate.Open(ctx, p, ws.Root(), t.box)
	if err != nil {
		t.auditGateway(ctx, ws.ID(), p, "", cmdexec.OutcomeFailed, err.Error())
		return gatewayOutput{}, gatewayStartError(p)
	}
	defer sess.Close()
	tools, err := sess.Tools(ctx)
	if err != nil {
		t.auditGateway(ctx, ws.ID(), p, "", cmdexec.OutcomeFailed, err.Error())
		return gatewayOutput{}, fmt.Errorf("provider %q did not list its tools", p.Name)
	}
	return gatewayOutput{Status: "completed", Tools: tools}, nil
}

// gatewayCall asks the local user, then forwards.
func (t *toolset) gatewayCall(ctx context.Context, p mcpgate.Provider, ws *workspace.Workspace, in gatewayInput) (gatewayOutput, error) {
	out, ok, err := t.confirmProxy(ctx, p, ws, in.Tool)
	if err != nil {
		return gatewayOutput{}, err
	}
	if !ok {
		return out, nil
	}

	sess, err := mcpgate.Open(ctx, p, ws.Root(), t.box)
	if err != nil {
		t.auditGateway(ctx, ws.ID(), p, in.Tool, cmdexec.OutcomeFailed, err.Error())
		return gatewayOutput{}, gatewayStartError(p)
	}
	defer sess.Close()
	res, err := sess.Call(ctx, in.Tool, in.Arguments)
	if err != nil {
		t.auditGateway(ctx, ws.ID(), p, in.Tool, cmdexec.OutcomeFailed, err.Error())
		return gatewayOutput{}, fmt.Errorf("provider %q failed to run %q", p.Name, in.Tool)
	}
	outcome := cmdexec.OutcomeOK
	if res.IsError {
		outcome = cmdexec.OutcomeFailed
	}
	t.auditGateway(ctx, ws.ID(), p, in.Tool, outcome, "")
	answer := gatewayOutput{
		Status: "completed",
		// Redaction happens here, at the boundary where the answer leaves the
		// machine, for the same reason command output is redacted here and not
		// earlier: the local audit surface is entitled to the real bytes.
		Content:   redact.Text(res.Text),
		ToolError: res.IsError,
		Truncated: res.Truncated,
	}
	if res.IsError {
		// The provider's own refusal. Fylane forwarded it, so retrying the
		// same arguments will fail the same way.
		answer.Action = "the provider rejected this call; read its message and change the arguments"
		answer.Next = nextstep.FixInput
	}
	return answer, nil
}

// confirmProxy asks the local user. Trust decides which question is asked, not
// whether the checks run: at Ask the request goes to the approver directly, so
// no rung and no workspace grant can waive it (that tier); at Workspace
// the user has classified this provider as ordinary work and the ordinary gate
// applies.
func (t *toolset) confirmProxy(ctx context.Context, p mcpgate.Provider, ws *workspace.Workspace, tool string) (gatewayOutput, bool, error) {
	if t.approver == nil {
		return gatewayOutput{}, false, fmt.Errorf("a proxied call needs local approval, but no approver is configured")
	}
	grant := false
	if p.Trusted() == mcpgate.Workspace {
		if t.gate == nil {
			return gatewayOutput{}, false, fmt.Errorf("this Companion has no approval gate configured")
		}
		gated, err := t.gate.Decide(ctx, ws.ID(), cmdrule.Decision{Verdict: cmdrule.Allow})
		if err != nil {
			return gatewayOutput{}, false, err
		}
		if gated.Need == cmdgate.Allowed {
			return gatewayOutput{}, true, nil
		}
		grant = gated.Grant
	}

	key := "proxy:" + commandKey(t.provider, ws.ID(), p.Name, []string{tool})
	if grant {
		key = "cmd-grant:" + ws.ID()
	}
	verdict, err := t.approver.Approve(ctx, &txn.ApprovalRequest{
		ChangeSetID:   key,
		WorkspaceID:   ws.ID(),
		WorkspaceName: ws.Name(),
		Provider:      t.provider,
		Summary:       p.Name + ": " + tool,
		Kind:          txn.KindProxy,
		Command:       []string{p.Name, tool},
		Rule:          gatewayRule,
		Reason:        GatewayWarning,
		Grant:         grant,
	})
	if err != nil {
		return gatewayOutput{}, false, err
	}
	switch {
	case verdict.Approved:
		if grant {
			if err := t.gate.Grant(ctx, ws.ID()); err != nil {
				return gatewayOutput{}, false, fmt.Errorf("recording the workspace authorization: %w", err)
			}
		}
		return gatewayOutput{}, true, nil
	case verdict.Pending:
		return gatewayOutput{
			Status: "pending_approval",
			Rule:   gatewayRule,
			Reason: GatewayWarning,
			Action: "the call is waiting for local user approval; call mcp_gateway again with the same provider and tool to check the outcome",
			Next:   nextstep.AskUser,
		}, false, nil
	default:
		t.auditGateway(ctx, ws.ID(), p, tool, cmdexec.OutcomeRefused, verdict.Reason)
		return gatewayOutput{
			Status: "refused",
			Rule:   gatewayRule,
			Reason: verdict.Reason,
			Action: "the local user declined this call; do not retry it without being asked to",
			Next:   nextstep.Stop,
		}, false, nil
	}
}

// gatewayStartError is what the caller is told when a provider will not run.
// The provider's own stderr stays on this machine: it is a foreign program's
// diagnostics, it is in the audit record the user can read, and forwarding it
// would put whatever that program prints on the wire.
func gatewayStartError(p mcpgate.Provider) error {
	return fmt.Errorf("provider %q could not be started; the local user can see why in Fylane's history", p.Name)
}

// auditGateway records one gateway operation. Every call is audited at every
// trust, which is the invariant the trust setting does not touch: it decides
// whether the user is asked, never whether the record is written.
func (t *toolset) auditGateway(ctx context.Context, workspaceID string, p mcpgate.Provider, tool, outcome, reason string) {
	if t.execAudit == nil {
		return
	}
	argv := []string{"mcp:" + p.Name}
	if tool != "" {
		argv = append(argv, tool)
	}
	t.execAudit.ExecAttempt(ctx, cmdexec.Record{
		WorkspaceID: workspaceID,
		Argv:        argv,
		Outcome:     outcome,
		Reason:      reason,
		Rule:        gatewayRule,
		Provider:    t.provider,
	})
}
