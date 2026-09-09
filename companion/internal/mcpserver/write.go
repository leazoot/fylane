package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// changeOutput is the shared structured result of every write tool,
// mirroring txn.Result plus pending_approval.
type changeOutput struct {
	Status      string         `json:"status" jsonschema:"applied | conflict | denied | failed | pending_approval"`
	ChangeSetID string         `json:"change_set_id,omitempty"`
	Operations  []txn.OpResult `json:"operations,omitempty"`
	// RollbackAvailableUntil is RFC 3339; present for applied results.
	RollbackAvailableUntil string        `json:"rollback_available_until,omitempty"`
	Conflict               *txn.Conflict `json:"conflict,omitempty"`
	Reason                 string        `json:"reason,omitempty"`
	// Effects says, per path, what actually happened when a change set did
	// not complete: not_started | state_changed | outcome_unknown.
	Effects []txn.OpEffect `json:"effects,omitempty" jsonschema:"Present on failed results: what actually happened to each path."`
	// Action tells the model what to do next for non-applied statuses.
	Action string `json:"action,omitempty"`
	// Next is the same answer in a fixed vocabulary, for callers that branch
	// on it rather than read it.
	Next nextstep.Step `json:"next,omitempty" jsonschema:"What to do next, as a fixed value: ask_user | stop | fix_input | reconcile | reobserve. Absent means the change was applied and nothing further is needed."`
}

func toChangeOutput(res *txn.Result) changeOutput {
	out := changeOutput{
		Status:      res.Status,
		ChangeSetID: res.ChangeSetID,
		Operations:  res.Operations,
		Conflict:    res.Conflict,
		Reason:      res.Reason,
		Effects:     res.Effects,
	}
	if !res.RollbackAvailableUntil.IsZero() {
		out.RollbackAvailableUntil = res.RollbackAvailableUntil.Format("2006-01-02T15:04:05Z07:00")
	}
	switch res.Status {
	case txn.StatusPending:
		out.Action = "the change is waiting for local user approval; call the same tool again with the same change_set_id to check the outcome"
	case txn.StatusConflict:
		if res.Conflict != nil {
			out.Action = res.Conflict.Action
		}
	case txn.StatusFailed:
		out.Action = failedAction(res.Effects)
	}
	out.Next = changeStep(res)
	return out
}

// failedAction says what a failed change set left behind.
//
// It used to say one sentence for every failure: re-read the affected files.
// That was the honest answer while the result was the only thing being
// consulted, and it is no longer the best one — the engine reconciles each
// path against the hashes it planned with, so most failures now have a
// definite answer and only the genuinely undecidable ones are handed back.
// The sentence still names the unknown paths rather than the whole set,
// because "some of this is unknown" and "all of this is unknown" ask the
// caller for very different amounts of work.
func failedAction(effects []txn.OpEffect) string {
	const fallback = "the change did not complete and the engine's restore is not guaranteed; re-read the affected files before deciding what to do"
	if len(effects) == 0 {
		return fallback
	}
	var unknown, changed []string
	for _, e := range effects {
		if !e.Effect.Valid() {
			// An effect the engine never decided. Saying anything specific
			// on top of it would be inventing the part that is missing.
			return fallback
		}
		switch e.Effect {
		case txn.OutcomeUnknown:
			unknown = append(unknown, e.Path)
		case txn.StateChanged:
			changed = append(changed, e.Path)
		}
	}
	switch {
	case len(unknown) == 0 && len(changed) == 0:
		return "the change did not complete and nothing was left behind: every path is as it was before the call"
	case len(unknown) == 0:
		return "the change did not complete, but these paths were left changed and were not restored: " +
			strings.Join(changed, ", ")
	case len(changed) == 0:
		return "the change did not complete; every other path is as it was before, but these could not be determined and must be re-read: " +
			strings.Join(unknown, ", ")
	default:
		return "the change did not complete; these paths were left changed: " +
			strings.Join(changed, ", ") +
			"; and these could not be determined and must be re-read: " +
			strings.Join(unknown, ", ")
	}
}

// execute runs a change set through the transaction engine and maps the
// result. All write tools funnel through here — there is no other write path.
func (t *toolset) execute(ctx context.Context, workspaceID, changeSetID, summary string, ops ...txn.Operation) (changeOutput, error) {
	var zero changeOutput
	if t.engine == nil {
		return zero, fmt.Errorf("write operations are not available: transaction engine not configured")
	}
	ws, err := t.open(ctx, workspaceID)
	if err != nil {
		return zero, err
	}
	res, err := t.engine.Execute(ctx, ws, txn.Request{
		ChangeSetID: changeSetID,
		Provider:    t.provider,
		Summary:     summary,
		Operations:  ops,
		MustAsk:     t.mustAsk(ops),
	})
	if err != nil {
		return zero, err
	}
	return toChangeOutput(res), nil
}

// mustAsk reports whether a route rule marked any of these files
// always-confirm.
//
// Only the "ask" action reaches an MCP write. A "route" rule must not: the
// model named this path itself and will read it back by that name, so
// silently landing the file somewhere else would break the next call and
// contradict the result we just reported. Route rules answer "where does an
// answer with no home go", which is a question only the browser saves ask.
// "ask" has no such problem — it relocates nothing and can only add a
// confirmation the user asked for.
func (t *toolset) mustAsk(ops []txn.Operation) bool {
	if t.rules == nil {
		return false
	}
	rules, err := t.rules.Load()
	if err != nil {
		// A preference file we cannot read must not silently weaken a
		// decision, but it also cannot invent one: the policy table still
		// applies underneath.
		return false
	}
	for _, op := range ops {
		target := op.Path
		if op.Type == txn.OpMove {
			target = op.To
		}
		if routerule.Apply(rules, t.provider, target).Ask {
			return true
		}
	}
	return false
}

// changeManageInput is the union input of the low-frequency change actions
// consolidated into one tool. Which fields are required
// depends on action; the handler validates per action and the engine
// re-validates hashes and paths.
type changeManageInput struct {
	Action      string `json:"action" jsonschema:"One of: move | delete | rollback | apply_change_set."`
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	// move
	From string `json:"from,omitempty" jsonschema:"move: workspace-relative source file path."`
	To   string `json:"to,omitempty" jsonschema:"move: workspace-relative destination path."`
	// delete
	Path      string `json:"path,omitempty" jsonschema:"delete: workspace-relative path to delete."`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"delete: required to delete a non-empty directory."`
	// move + delete
	ExpectedSHA256 string `json:"expected_sha256,omitempty" jsonschema:"move/delete: SHA-256 of the file as last read; required for files, omitted for directories."`
	// apply_change_set
	Summary    string          `json:"summary,omitempty" jsonschema:"apply_change_set: short human-readable description of the whole change."`
	Operations []txn.Operation `json:"operations,omitempty" jsonschema:"apply_change_set: operations to apply atomically, each {type: create|update|move|delete, ...}."`
	// rollback target; also the pending_approval retry handle for every action
	ChangeSetID string `json:"change_set_id,omitempty" jsonschema:"rollback: the change set to roll back, from a previous applied result. Any action: pass back the change_set_id from a pending_approval result to retry."`
}

func (t *toolset) changeManage(ctx context.Context, _ *mcp.CallToolRequest, in changeManageInput) (*mcp.CallToolResult, changeOutput, error) {
	var zero changeOutput
	switch in.Action {
	case "move":
		if in.From == "" || in.To == "" {
			return nil, zero, fmt.Errorf("action move requires from and to")
		}
		out, err := t.execute(ctx, in.WorkspaceID, in.ChangeSetID, "Move "+in.From+" to "+in.To, txn.Operation{
			Type: txn.OpMove, From: in.From, To: in.To, ExpectedSHA256: in.ExpectedSHA256,
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, out, nil
	case "delete":
		if in.Path == "" {
			return nil, zero, fmt.Errorf("action delete requires path")
		}
		out, err := t.execute(ctx, in.WorkspaceID, in.ChangeSetID, "Delete "+in.Path, txn.Operation{
			Type: txn.OpDelete, Path: in.Path, Recursive: in.Recursive, ExpectedSHA256: in.ExpectedSHA256,
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, out, nil
	case "rollback":
		if t.engine == nil {
			return nil, zero, fmt.Errorf("write operations are not available: transaction engine not configured")
		}
		if in.ChangeSetID == "" {
			return nil, zero, fmt.Errorf("action rollback requires change_set_id")
		}
		ws, err := t.open(ctx, in.WorkspaceID)
		if err != nil {
			return nil, zero, err
		}
		res, err := t.engine.Rollback(ctx, ws, in.ChangeSetID)
		if err != nil {
			return nil, zero, err
		}
		return nil, toChangeOutput(res), nil
	case "apply_change_set":
		if len(in.Operations) == 0 {
			return nil, zero, fmt.Errorf("action apply_change_set requires operations")
		}
		out, err := t.execute(ctx, in.WorkspaceID, in.ChangeSetID, in.Summary, in.Operations...)
		if err != nil {
			return nil, zero, err
		}
		return nil, out, nil
	default:
		return nil, zero, fmt.Errorf("unknown action %q; use move, delete, rollback, or apply_change_set", in.Action)
	}
}

type writeFileInput struct {
	WorkspaceID    string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Path           string `json:"path" jsonschema:"Workspace-relative file path to create or replace."`
	Content        string `json:"content" jsonschema:"Full new file content."`
	ExpectedSHA256 string `json:"expected_sha256,omitempty" jsonschema:"SHA-256 of the file as last read. Required when replacing an existing file; omit when creating a new one."`
	ChangeSetID    string `json:"change_set_id,omitempty" jsonschema:"Pass back the change_set_id from a pending_approval result to retry; omit for a new change."`
}

func (t *toolset) writeFile(ctx context.Context, _ *mcp.CallToolRequest, in writeFileInput) (*mcp.CallToolResult, changeOutput, error) {
	op := txn.Operation{Path: in.Path, Content: in.Content, ExpectedSHA256: in.ExpectedSHA256}
	// expected_sha256 decides create vs update; the engine re-validates
	// against the actual disk state either way.
	if in.ExpectedSHA256 == "" {
		op.Type = txn.OpCreate
	} else {
		op.Type = txn.OpUpdate
		op.Content = t.matchTargetConvention(ctx, in.WorkspaceID, in.Path, in.Content)
	}
	out, err := t.execute(ctx, in.WorkspaceID, in.ChangeSetID, "Write "+in.Path, op)
	if err != nil {
		return nil, changeOutput{}, err
	}
	return nil, out, nil
}

type applyPatchInput struct {
	WorkspaceID    string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Path           string `json:"path" jsonschema:"Workspace-relative file path to patch."`
	Patch          string `json:"patch" jsonschema:"Unified diff to apply to the current file content."`
	ExpectedSHA256 string `json:"expected_sha256" jsonschema:"SHA-256 of the file as last read."`
	ChangeSetID    string `json:"change_set_id,omitempty" jsonschema:"Pass back the change_set_id from a pending_approval result to retry; omit for a new change."`
}

func (t *toolset) applyPatch(ctx context.Context, _ *mcp.CallToolRequest, in applyPatchInput) (*mcp.CallToolResult, changeOutput, error) {
	var zero changeOutput
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	abs, canonical, err := ws.Resolve(in.Path, sandbox.OpWrite)
	if err != nil {
		return nil, zero, err
	}
	old, conflict, err := loadPatchBase(ws, abs, canonical, in.ExpectedSHA256)
	if err != nil {
		return nil, zero, err
	}
	if conflict != nil {
		return nil, toChangeOutput(&txn.Result{Status: txn.StatusConflict, Conflict: conflict}), nil
	}
	// Patching happens in the same decoded text space that read_file
	// returns; the result is written back in the file's original encoding
	// and line-ending convention.
	text, prof, ok := decodeTextFile([]byte(old))
	if !ok {
		return nil, zero, fmt.Errorf("%s is not a patchable text file", canonical)
	}
	patched, err := applyUnified(prof.normalizeInput(in.Patch), text)
	if err != nil {
		return nil, zero, fmt.Errorf("patch does not apply to %s: %v; read the file and regenerate the patch", canonical, err)
	}
	encoded, err := prof.encode(patched)
	if err != nil {
		return nil, zero, err
	}
	out, err := t.execute(ctx, in.WorkspaceID, in.ChangeSetID, "Patch "+canonical, txn.Operation{
		Type: txn.OpUpdate, Path: in.Path, Content: encoded, ExpectedSHA256: in.ExpectedSHA256,
	})
	if err != nil {
		return nil, zero, err
	}
	return nil, out, nil
}

// matchTargetConvention re-encodes full replacement content into the target
// file's current encoding and line-ending convention. It is best-effort by
// design: on any failure — unreadable target, binary target, or content not
// representable in the target's encoding — the content is written as given
// (UTF-8), because a full replacement is authoritative and every access
// error here resurfaces as a proper sandbox or conflict result when the
// engine re-validates the operation.
func (t *toolset) matchTargetConvention(ctx context.Context, workspaceID, path, content string) string {
	ws, err := t.open(ctx, workspaceID)
	if err != nil {
		return content
	}
	abs, _, err := ws.Resolve(path, sandbox.OpWrite)
	if err != nil {
		return content
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return content
	}
	_, prof, ok := decodeTextFile(data)
	if !ok {
		return content
	}
	encoded, err := prof.encode(prof.normalizeInput(content))
	if err != nil {
		return content
	}
	return encoded
}

// loadPatchBase reads the patch target and enforces the base-hash contract
// before the patch is even computed.
func loadPatchBase(ws *workspace.Workspace, abs, canonical, expected string) (string, *txn.Conflict, error) {
	if expected == "" {
		return "", &txn.Conflict{Path: canonical, Reason: "expected_sha256_missing",
			Action: "read the file and pass its sha256 as expected_sha256"}, nil
	}
	data, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return "", &txn.Conflict{Path: canonical, Reason: "target_missing",
			Action: "the file does not exist; use write_file to create it"}, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", canonical, err)
	}
	sum := sha256.Sum256(data)
	current := hex.EncodeToString(sum[:])
	if !strings.EqualFold(expected, current) {
		return "", &txn.Conflict{Path: canonical, Reason: "base_hash_mismatch", CurrentSHA256: current,
			Action: "read the latest file and regenerate the change"}, nil
	}
	return string(data), nil, nil
}
