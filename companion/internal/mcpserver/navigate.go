package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/lsp"
	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// code_navigate. Three questions a language server can answer exactly
// and a text search can only approximate: where is this defined, who uses it,
// what is in this file.
//
// Two things about it are deliberate and neither is convenience.
//
// It never falls back to searching. A server that is not installed, a file it
// does not handle, a symbol it cannot find — each says so and stops. A
// navigation answer that quietly turned into a grep result is worse than no
// answer, because nothing downstream can tell which one it received.
//
// Starting a server asks the local user once per workspace. gopls is not a
// reader; it invokes the Go toolchain, which is a real subprocess doing real
// work on this machine, and calling that "just reading" would be untrue. The
// question is shaped like the one-time workspace authorization, and it is
// its own question: approving it authorizes this language server here, and
// nothing else.

// navigateActions are the three questions and no fourth.
const (
	actionDefinition = "definition"
	actionReferences = "references"
	actionSymbols    = "symbols"
)

// navigateRule is the rule id the desktop prompt keys off. Like the gateway
// and delegation rules it is not a cmdrule entry: the table judges commands a
// caller chose, and this program was chosen by the local user's settings.
const navigateRule = "language-server-starts-here"

// NavigateWarning is shown with the prompt. It says the two things that make
// this a question rather than a formality.
const NavigateWarning = "a language server reads every file in this folder and may run the language's own toolchain to do it"

// Navigators is the language-server supervisor, as this package uses it.
type Navigators interface {
	Registry() *lsp.Registry
	Running(workspaceID, server string) bool
	Approved(workspaceID, server string) bool
	Approve(workspaceID, server string)
	Definition(ctx context.Context, q lsp.Query) ([]lsp.Ref, error)
	References(ctx context.Context, q lsp.Query) ([]lsp.Ref, error)
	Symbols(ctx context.Context, q lsp.Query) ([]lsp.Symbol, error)
}

type navigateInput struct {
	Action string `json:"action" jsonschema:"definition | references | symbols"`
	Path   string `json:"path" jsonschema:"Workspace-relative path of the file to ask about."`
	Symbol string `json:"symbol,omitempty" jsonschema:"The name to look up. Required for definition and references unless line is given."`
	Line   int    `json:"line,omitempty" jsonschema:"1-based line, to pick between several symbols with the same name or to point at a position directly."`

	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
}

type navigateOutput struct {
	Status string `json:"status" jsonschema:"ok | pending_approval | refused | unavailable | ambiguous"`
	// Server names the language server that answered, so a caller can tell an
	// empty answer from an answer nobody was asked for.
	Server string `json:"server,omitempty"`
	// Locations are the definitions or references found, workspace-relative
	// with 1-based lines.
	Locations []lsp.Ref `json:"locations,omitempty"`
	// Symbols is what a file contains, for action=symbols.
	Symbols []lsp.Symbol `json:"symbols,omitempty"`
	// Candidates lists the places a repeated name occurs, when the caller has
	// to say which one they meant.
	Candidates []lsp.Symbol `json:"candidates,omitempty"`
	// Hidden counts results dropped because the file they are in is one this
	// workspace does not show — an excluded path, or a sensitive file. They
	// are counted rather than silently missing: a reference list must not be
	// the one surface where a hidden file appears, and must not pretend the
	// hidden file does not use the symbol either.
	Hidden int           `json:"hidden,omitempty"`
	Reason string        `json:"reason,omitempty"`
	Action string        `json:"action,omitempty"`
	Next   nextstep.Step `json:"next,omitempty" jsonschema:"What to do next, as a fixed value: ask_user | stop | fix_input | reconcile | reobserve."`
}

func (t *toolset) codeNavigate(ctx context.Context, _ *mcp.CallToolRequest, in navigateInput) (*mcp.CallToolResult, navigateOutput, error) {
	var zero navigateOutput
	switch in.Action {
	case actionDefinition, actionReferences, actionSymbols:
	default:
		return nil, zero, fmt.Errorf("unknown action %q; use %s, %s or %s",
			in.Action, actionDefinition, actionReferences, actionSymbols)
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, zero, fmt.Errorf("path is required")
	}
	if in.Line < 0 {
		return nil, zero, fmt.Errorf("line is 1-based; %d is not a line", in.Line)
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	// The file being asked about goes through the same sandbox and the same
	// exclude and sensitive rules as a read, because that is what it is.
	_, canonical, err := ws.Resolve(in.Path, sandbox.OpRead)
	if err != nil {
		return nil, zero, err
	}
	if err := t.checkReadable(ctx, ws, canonical); err != nil {
		return nil, zero, err
	}

	server, ok := t.navigators.Registry().For(canonical)
	if !ok {
		return nil, navigateOutput{
			Status: "unavailable",
			Reason: fmt.Sprintf("no language server on this machine handles %s", suffixOf(canonical)),
			Action: "use search_files or read_file for this file; there is no navigation for it",
			Next:   nextstep.FixInput,
		}, nil
	}

	if out, ok, err := t.confirmNavigator(ctx, ws, server); err != nil || !ok {
		return nil, out, err
	}

	q := lsp.Query{WorkspaceID: ws.ID(), Root: ws.Root(), Path: canonical, Symbol: in.Symbol, Line: in.Line}
	out := navigateOutput{Status: "ok", Server: server.Name}

	switch in.Action {
	case actionSymbols:
		syms, err := t.navigators.Symbols(ctx, q)
		if err != nil {
			return nil, navigateFailure(err), nil
		}
		out.Symbols = syms
	case actionDefinition:
		refs, err := t.navigators.Definition(ctx, q)
		if err != nil {
			return nil, navigateFailure(err), nil
		}
		out.Locations, out.Hidden = visibleRefs(ws, refs)
	case actionReferences:
		refs, err := t.navigators.References(ctx, q)
		if err != nil {
			return nil, navigateFailure(err), nil
		}
		out.Locations, out.Hidden = visibleRefs(ws, refs)
	}
	return nil, out, nil
}

// confirmNavigator asks the local user before this Companion starts a language
// server in this workspace for the first time. A server that is already
// running was already asked about; reclaiming it for idleness does not undo
// the answer, because the answer was about the server, not the process.
func (t *toolset) confirmNavigator(ctx context.Context, ws *workspace.Workspace, server lsp.Server) (navigateOutput, bool, error) {
	nav := t.navigators
	if nav.Approved(ws.ID(), server.Name) || nav.Running(ws.ID(), server.Name) {
		return navigateOutput{}, true, nil
	}
	if t.approver == nil {
		return navigateOutput{}, false, fmt.Errorf("starting a language server needs local approval, but no approver is configured")
	}
	verdict, err := t.approver.Approve(ctx, &txn.ApprovalRequest{
		ChangeSetID:   "lsp-start:" + ws.ID() + ":" + server.Name,
		WorkspaceID:   ws.ID(),
		WorkspaceName: ws.Name(),
		Provider:      t.provider,
		Summary:       "start the " + server.Name + " language server in this folder",
		Kind:          txn.KindCommand,
		Command:       server.Command,
		Rule:          navigateRule,
		Reason:        NavigateWarning,
	})
	if err != nil {
		return navigateOutput{}, false, err
	}
	switch {
	case verdict.Approved:
		nav.Approve(ws.ID(), server.Name)
		return navigateOutput{}, true, nil
	case verdict.Pending:
		return navigateOutput{
			Status: "pending_approval",
			Server: server.Name,
			Reason: NavigateWarning,
			Action: "starting the language server is waiting for local user approval; call code_navigate again with the same arguments to check the outcome",
			Next:   nextstep.AskUser,
		}, false, nil
	default:
		return navigateOutput{
			Status: "refused",
			Server: server.Name,
			Reason: verdict.Reason,
			Action: "the local user declined to start this language server; use search_files and read_file instead",
			Next:   nextstep.Stop,
		}, false, nil
	}
}

// navigateFailure turns the supervisor's errors into the answers a caller can
// act on. Ambiguity is the interesting one: it is not a failure of the tool
// but a question back, and it carries the lines that would settle it.
func navigateFailure(err error) navigateOutput {
	var amb *lsp.AmbiguousError
	if errors.As(err, &amb) {
		return navigateOutput{
			Status:     "ambiguous",
			Candidates: amb.Candidates,
			Reason:     amb.Error(),
			Action:     "call code_navigate again with line set to the one you meant",
			Next:       nextstep.FixInput,
		}
	}
	var missing *lsp.NotFoundError
	if errors.As(err, &missing) {
		return navigateOutput{
			Status: "unavailable",
			Reason: missing.Error(),
			Action: "check the spelling, or use search_files — this tool does not guess",
			Next:   nextstep.FixInput,
		}
	}
	return navigateOutput{
		Status: "unavailable",
		Reason: err.Error(),
		Action: "the language server could not answer; try again, or use search_files for this file",
		Next:   nextstep.Reobserve,
	}
}

// visibleRefs drops results in files this workspace does not show and counts
// them. A file hidden from listings and skipped in search must not reappear
// here, and the count says one was dropped rather than leaving the caller to
// believe the symbol is used in fewer places than it is.
func visibleRefs(ws *workspace.Workspace, refs []lsp.Ref) ([]lsp.Ref, int) {
	out := make([]lsp.Ref, 0, len(refs))
	hidden := 0
	for _, r := range refs {
		if r.Path != "" && (ws.Excluded(r.Path, false) || ws.Sensitive(r.Path)) {
			hidden++
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, hidden
	}
	return out, hidden
}

func suffixOf(rel string) string {
	if i := strings.LastIndex(rel, "."); i >= 0 && i < len(rel)-1 {
		return rel[i:] + " files"
	}
	return "files without a suffix"
}
