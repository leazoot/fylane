package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// editOp is one quick edit applied to the file's current text. Edits are
// applied in order; each operates on the result of the previous one.
type editOp struct {
	Type      string `json:"type" jsonschema:"One of replace_exact | insert_before | insert_after | delete_exact | replace_range."`
	Match     string `json:"match,omitempty" jsonschema:"Exact text to locate, for the four match-based edit types. Must occur exactly once in the file; include more surrounding lines to disambiguate."`
	Content   string `json:"content,omitempty" jsonschema:"Replacement or inserted text. May be empty only for replace_exact and replace_range (which then act as deletions)."`
	StartLine int    `json:"start_line,omitempty" jsonschema:"replace_range only: first line to replace, 1-based inclusive."`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"replace_range only: last line to replace, 1-based inclusive."`
}

type editFileInput struct {
	WorkspaceID    string   `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Path           string   `json:"path" jsonschema:"Workspace-relative file path to edit."`
	Edits          []editOp `json:"edits" jsonschema:"Edits applied in order; each operates on the result of the previous one. All succeed or none are applied."`
	ExpectedSHA256 string   `json:"expected_sha256" jsonschema:"SHA-256 of the file as last read."`
	ChangeSetID    string   `json:"change_set_id,omitempty" jsonschema:"Pass back the change_set_id from a pending_approval result to retry; omit for a new change."`
}

func (t *toolset) editFile(ctx context.Context, _ *mcp.CallToolRequest, in editFileInput) (*mcp.CallToolResult, changeOutput, error) {
	var zero changeOutput
	if len(in.Edits) == 0 {
		return nil, zero, fmt.Errorf("edits must contain at least one edit")
	}
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
	// Edits are applied in the same decoded text space that read_file
	// returns; the result is written back in the file's original encoding
	// and line-ending convention.
	text, prof, ok := decodeTextFile([]byte(old))
	if !ok {
		return nil, zero, fmt.Errorf("%s is not an editable text file", canonical)
	}
	edits := make([]editOp, len(in.Edits))
	for i, e := range in.Edits {
		e.Match = prof.normalizeInput(e.Match)
		e.Content = prof.normalizeInput(e.Content)
		edits[i] = e
	}
	edited, err := applyEdits(text, edits)
	if err != nil {
		return nil, zero, fmt.Errorf("edit does not apply to %s: %v", canonical, err)
	}
	encoded, err := prof.encode(edited)
	if err != nil {
		return nil, zero, err
	}
	out, err := t.execute(ctx, in.WorkspaceID, in.ChangeSetID, "Edit "+canonical, txn.Operation{
		Type: txn.OpUpdate, Path: in.Path, Content: encoded, ExpectedSHA256: in.ExpectedSHA256,
	})
	if err != nil {
		return nil, zero, err
	}
	return nil, out, nil
}

func applyEdits(text string, edits []editOp) (string, error) {
	for i, e := range edits {
		next, err := applyEdit(text, e)
		if err != nil {
			return "", fmt.Errorf("edit %d (%s): %w", i+1, e.Type, err)
		}
		text = next
	}
	return text, nil
}

func applyEdit(cur string, op editOp) (string, error) {
	switch op.Type {
	case "replace_exact":
		return spliceUnique(cur, op.Match, op.Content)
	case "delete_exact":
		return spliceUnique(cur, op.Match, "")
	case "insert_before":
		if op.Content == "" {
			return "", fmt.Errorf("content is required")
		}
		return spliceUnique(cur, op.Match, op.Content+op.Match)
	case "insert_after":
		if op.Content == "" {
			return "", fmt.Errorf("content is required")
		}
		return spliceUnique(cur, op.Match, op.Match+op.Content)
	case "replace_range":
		return replaceRange(cur, op)
	default:
		return "", fmt.Errorf("unknown edit type %q", op.Type)
	}
}

// spliceUnique replaces the single occurrence of match with repl. Zero or
// multiple occurrences are rejected so an ambiguous edit can never land in
// the wrong place.
func spliceUnique(cur, match, repl string) (string, error) {
	if match == "" {
		return "", fmt.Errorf("match is required")
	}
	switch n := strings.Count(cur, match); n {
	case 0:
		return "", fmt.Errorf("match not found; re-read the file and copy the target text exactly, including whitespace")
	case 1:
		return strings.Replace(cur, match, repl, 1), nil
	default:
		return "", fmt.Errorf("match occurs %d times; include more surrounding text to make it unique", n)
	}
}

func replaceRange(cur string, op editOp) (string, error) {
	lines := splitAfterLines(cur)
	if op.StartLine < 1 || op.EndLine < op.StartLine {
		return "", fmt.Errorf("invalid line range %d-%d", op.StartLine, op.EndLine)
	}
	if op.EndLine > len(lines) {
		return "", fmt.Errorf("file has %d lines, range %d-%d is out of bounds", len(lines), op.StartLine, op.EndLine)
	}
	repl := op.Content
	// Keep the file's line structure intact: the replacement must end with a
	// newline whenever lines follow it, or when it replaces the final line of
	// a file that already ended with one.
	if repl != "" && !strings.HasSuffix(repl, "\n") &&
		(op.EndLine < len(lines) || strings.HasSuffix(cur, "\n")) {
		repl += "\n"
	}
	var b strings.Builder
	for _, l := range lines[:op.StartLine-1] {
		b.WriteString(l)
	}
	b.WriteString(repl)
	for _, l := range lines[op.EndLine:] {
		b.WriteString(l)
	}
	return b.String(), nil
}

// splitAfterLines splits text into lines that keep their trailing newline.
// A final line without a newline is still one line.
func splitAfterLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
