package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/textenc"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

const (
	// maxReadBytes is the content size returned by read_file before
	// truncation when the caller is not a platform we have measured
	//.
	maxReadBytes = 1 << 20 // 1 MiB
	// maxFileBytes is the hard cap on files the Stage 1 implementation
	// will load for hashing; larger files are rejected outright.
	maxFileBytes = 64 << 20 // 64 MiB
)

type toolset struct {
	src    WorkspaceSource
	engine *txn.Engine
	reads  ReadApprover
	rules  RuleSource
	// remotes lists workspaces on other machines for workspace_info; nil
	// when there are none.
	remotes func(ctx context.Context) []RemoteWorkspace
	// exec, tasks, approver, and execAudit back run_command and task_status;
	// all four are nil together when command execution is not configured.
	exec      CommandRunner
	tasks     TaskTracker
	approver  CommandApprover
	gate      CommandGate
	execAudit cmdexec.Auditor
	// runs answers task_status for work the in-memory table no longer holds.
	// After a restart that is every run, which is exactly when a caller most
	// needs an answer other than "unknown task id".
	runs RunJournal
	// activityLog backs the last-activity answer of workspace_info.
	activityLog ActivityJournal
	// memory backs the memory_* tools; nil leaves them unregistered.
	memory MemoryStore
	// agents backs code_task; nil means no delegation is configured.
	agents AgentRegistry
	// providers backs mcp_gateway; nil means no local MCP server is
	// configured, and the tool is not registered at all.
	providers ProviderRegistry
	// box is the kernel read boundary applied to every program this Companion
	// starts. Nil means the platform has none, which changes nothing else.
	box *readbox.Box
	// navigators backs code_navigate; nil, or a registry with no installed
	// server, leaves the tool unregistered rather than advertising a
	// navigation this machine cannot perform.
	navigators Navigators
	// provider identifies the calling platform for approval budgets.
	// Connection-level identification arrives with OAuth; until
	// then every caller gets the conservative default budget.
	provider string
	// inlineBudget caps the content bytes returned inline by one read;
	// zero means the maxReadBytes default. Content beyond it is reachable
	// through line ranges and the fylane:// resource of the same file.
	inlineBudget int
	// taskCeiling is the user's ceiling on command runtime; nil means the
	// engine's default.
	taskCeiling func() time.Duration
}

// inlineBudgets is what one read may return inline, per platform. The
// numbers come from measurements against the live platforms
// (04_TECH_STACK compatibility table): a response body of about 8 MB gets
// through Claude, 256 KB through ChatGPT and 64 KB through Grok — a spread of
// 128×, which is why one global figure could not be right for all three.
//
// These are halved from the measured ceiling and then given room, because the
// measurement is of the response on the wire and a file arrives there roughly
// twice its own size once it is JSON-encoded.
//
// The two directions are not symmetric, and that is what sets these numbers.
// Under budget, an oversized file is truncated with Truncated: true and the
// rest is reachable by line range or through its fylane:// resource — the
// caller pages. Over the platform's own limit, the whole response is rejected
// and the call fails. Too small costs a round trip; too large costs the
// answer. So every entry here sits well under what was measured.
var inlineBudgets = map[string]int{
	"claude":  3 << 20,  // measured ~8 MB
	"chatgpt": 96 << 10, // measured 256 KB
	"grok":    24 << 10, // measured 64 KB
}

func (t *toolset) budget() int {
	// An operator who set -max-inline-bytes gets what they asked for, on
	// every platform. It is one number for all of them, which is the wrong
	// shape for this — but an explicit setting that quietly does not apply is
	// worse than one that applies too broadly.
	if t.inlineBudget > 0 {
		return t.inlineBudget
	}
	// An unrecognised caller keeps the old global figure rather than the
	// smallest measured one. Nothing is known about its limits, and the
	// callers that land here are as likely to have none (a local client in
	// direct mode) as to be a Grok whose header went missing.
	if b, ok := inlineBudgets[t.provider]; ok {
		return b
	}
	return maxReadBytes
}

// open resolves the caller-supplied workspace ID to a runtime handle. An
// empty ID falls back to the current workspace so clients can
// bootstrap without a prior workspace_info call.
func (t *toolset) open(ctx context.Context, id string) (*workspace.Workspace, error) {
	if id == "" {
		rec, err := t.src.Current(ctx)
		if err != nil {
			return nil, fmt.Errorf("no workspace selected; call workspace_info and pass a workspace_id")
		}
		id = rec.ID
	}
	ws, err := t.src.Open(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("unknown workspace_id %q; call workspace_info to see available workspaces", id)
	}
	if err != nil {
		return nil, err
	}
	return ws, nil
}

type workspaceEntry struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Mode        string `json:"mode" jsonschema:"read_only or read_write"`
	Status      string `json:"status"`
	Current     bool   `json:"current"`
	// Machine names the computer a workspace lives on when it is not this
	// one. Files, commands and approvals for it happen there.
	Machine string `json:"machine,omitempty" jsonschema:"Set when the workspace is on another machine reached over ssh; omitted for this machine."`
}

type workspaceInfoInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier. May be omitted to query the current workspace."`
}

type workspaceInfoOutput struct {
	WorkspaceID  string `json:"workspace_id"`
	Name         string `json:"name"`
	Mode         string `json:"mode" jsonschema:"read_only or read_write"`
	MaxReadBytes int64  `json:"max_read_bytes"`
	Writable     bool   `json:"writable"`
	// Workspaces lists every available workspace so clients can discover
	// IDs without a separate tool (this absorbed list_workspaces).
	Workspaces []workspaceEntry `json:"workspaces"`
	// LastActivity is where the previous session left off in the answered
	// workspace, from this Companion's own records. Absent when nothing
	// has happened in it yet, and for a workspace on another machine.
	LastActivity *lastActivity `json:"last_activity,omitempty" jsonschema:"Where work in this workspace was left: the newest changes and commands on record, bounded. Read it before asking the user what was done last time."`
	// Memory says that earlier conversations left something, and the two
	// lines of it that matter most. Absent when nothing was remembered.
	Memory *memoryHint `json:"memory,omitempty" jsonschema:"Earlier conversations remembered things about this workspace. Call memory_recall for the page and recent notes."`
}

func (t *toolset) workspaceInfo(ctx context.Context, _ *mcp.CallToolRequest, in workspaceInfoInput) (*mcp.CallToolResult, workspaceInfoOutput, error) {
	var remote []RemoteWorkspace
	if t.remotes != nil {
		remote = t.remotes(ctx)
	}
	// The current folder follows the machine the window stands on. When
	// that is another machine, its current folder is the one to answer
	// with and the one marked current; the local folders are still listed.
	// When it is this computer, or when nothing on that machine is current
	// yet, the local current folder is current as it always was. With no
	// local folder at all, a remote folder still answers the discovery
	// call, or the model could never learn the id it needs.
	var standIn *RemoteWorkspace
	for i := range remote {
		if remote[i].Current {
			standIn = &remote[i]
			break
		}
	}
	out := workspaceInfoOutput{MaxReadBytes: int64(t.budget()), Workspaces: []workspaceEntry{}}
	var ws *workspace.Workspace
	if in.WorkspaceID != "" || standIn == nil {
		var err error
		ws, err = t.open(ctx, in.WorkspaceID)
		switch {
		case err == nil:
		case in.WorkspaceID == "" && len(remote) > 0:
			standIn = &remote[0]
		default:
			return nil, workspaceInfoOutput{}, err
		}
	}
	if ws != nil {
		out.WorkspaceID, out.Name, out.Writable = ws.ID(), ws.Name(), ws.Writable()
		out.Mode = store.ModeReadOnly
		if ws.Writable() {
			out.Mode = store.ModeReadWrite
		}
		act, err := t.activity(ctx, ws)
		if err != nil {
			return nil, workspaceInfoOutput{}, err
		}
		out.LastActivity = act
		if out.Memory, err = t.memoryPreview(ctx, ws.ID()); err != nil {
			return nil, workspaceInfoOutput{}, err
		}
	} else {
		out.WorkspaceID, out.Name, out.Mode = standIn.WorkspaceID, standIn.Name, standIn.Mode
		out.Writable = standIn.Mode == store.ModeReadWrite
	}
	list, err := t.src.List(ctx)
	if err != nil {
		return nil, workspaceInfoOutput{}, err
	}
	currentID := ""
	if current, err := t.src.Current(ctx); err == nil && standIn == nil {
		currentID = current.ID
	}
	for _, w := range list {
		out.Workspaces = append(out.Workspaces, workspaceEntry{
			WorkspaceID: w.ID,
			Name:        w.Name,
			Mode:        w.Mode,
			Status:      w.Status,
			Current:     w.ID == currentID,
		})
	}
	for _, w := range remote {
		out.Workspaces = append(out.Workspaces, workspaceEntry{
			WorkspaceID: w.WorkspaceID, Name: w.Name, Mode: w.Mode, Status: w.Status, Machine: w.Machine,
			Current: standIn != nil && w.WorkspaceID == standIn.WorkspaceID,
		})
	}
	return nil, out, nil
}

type statPathInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Path        string `json:"path" jsonschema:"Workspace-relative path of a file or directory."`
}

type statPathOutput struct {
	Path      string `json:"path"`
	Type      string `json:"type" jsonschema:"file or directory"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty" jsonschema:"SHA-256 of the file content; empty for directories."`
}

func (t *toolset) statPath(ctx context.Context, _ *mcp.CallToolRequest, in statPathInput) (*mcp.CallToolResult, statPathOutput, error) {
	var zero statPathOutput
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	abs, canonical, err := ws.Resolve(in.Path, sandbox.OpRead)
	if err != nil {
		return nil, zero, err
	}
	if err := t.checkReadable(ctx, ws, canonical); err != nil {
		return nil, zero, err
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, zero, fmt.Errorf("path not found: %s", canonical)
		}
		return nil, zero, fmt.Errorf("checking %s: %w", canonical, err)
	}
	out := statPathOutput{Path: canonical, SizeBytes: info.Size()}
	switch {
	case info.IsDir():
		out.Type = "directory"
		out.SizeBytes = 0
	case info.Mode().IsRegular():
		out.Type = "file"
		if info.Size() <= maxFileBytes {
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, zero, fmt.Errorf("hashing %s: %w", canonical, err)
			}
			sum := sha256.Sum256(data)
			out.SHA256 = hex.EncodeToString(sum[:])
		}
	default:
		return nil, zero, fmt.Errorf("%s is not a regular file or directory", canonical)
	}
	return nil, out, nil
}

// checkReadable enforces the workspace exclude and sensitive rules for
// read-class access to an exact path. Excluded paths behave as if
// they do not exist. Sensitive paths block on a local confirmation
// (per-read, every mode); without a configured approver they fail closed —
// never open.
func (t *toolset) checkReadable(ctx context.Context, ws *workspace.Workspace, canonical string) error {
	if ws.Excluded(canonical, false) {
		return fmt.Errorf("path not found: %s", canonical)
	}
	if !ws.Sensitive(canonical) {
		return nil
	}
	if t.reads == nil {
		return fmt.Errorf("%s is a sensitive file; reading it requires local confirmation, which is not available", canonical)
	}
	decision, err := t.reads.ApproveRead(ctx, t.provider, ws.ID(), ws.Name(), canonical)
	if err != nil {
		return fmt.Errorf("confirming read of %s: %w", canonical, err)
	}
	switch {
	case decision.Approved:
		return nil
	case decision.Pending:
		return fmt.Errorf("%s is a sensitive file awaiting local confirmation; retry this call after the user decides", canonical)
	default:
		return fmt.Errorf("reading %s was rejected locally", canonical)
	}
}

type readFileInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Path        string `json:"path" jsonschema:"Workspace-relative file path, e.g. src/main.go."`
	StartLine   int    `json:"start_line,omitempty" jsonschema:"First line to return, 1-based. Omit to read from the beginning."`
	EndLine     int    `json:"end_line,omitempty" jsonschema:"Last line to return, 1-based inclusive. Omit to read to the end."`
}

type readFileOutput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	SHA256     string `json:"sha256"`
	Encoding   string `json:"encoding"`
	Truncated  bool   `json:"truncated"`
	TotalLines int    `json:"total_lines"`
	SizeBytes  int64  `json:"size_bytes"`
	// NextStartLine is set when the content was truncated: pass it as
	// start_line to continue reading where this response left off.
	NextStartLine int `json:"next_start_line,omitempty"`
	// ResourceURI addresses the same file as an MCP resource for clients
	// that support resources/read; set when the content was truncated.
	ResourceURI string `json:"resource_uri,omitempty"`
}

func (t *toolset) readFile(ctx context.Context, _ *mcp.CallToolRequest, in readFileInput) (*mcp.CallToolResult, readFileOutput, error) {
	var zero readFileOutput
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	out, err := t.readOne(ctx, ws, in.Path, in.StartLine, in.EndLine)
	if err != nil {
		return nil, zero, err
	}
	return nil, out, nil
}

// readOne reads a single workspace file, enforcing sandbox, exclude, and
// sensitive rules, size limits, and encoding detection. Binary files return
// metadata (hash, size, encoding "binary") with empty content — raw binary
// bytes never go to the model.
func (t *toolset) readOne(ctx context.Context, ws *workspace.Workspace, relPath string, startLine, endLine int) (readFileOutput, error) {
	var zero readFileOutput
	abs, canonical, err := ws.Resolve(relPath, sandbox.OpRead)
	if err != nil {
		return zero, err
	}
	if err := t.checkReadable(ctx, ws, canonical); err != nil {
		return zero, err
	}
	if startLine < 0 || endLine < 0 || (endLine > 0 && startLine > endLine) {
		return zero, errors.New("invalid line range: start_line and end_line must be positive and start_line <= end_line")
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, fmt.Errorf("file not found: %s", canonical)
		}
		return zero, fmt.Errorf("reading %s: %w", canonical, err)
	}
	if info.IsDir() {
		return zero, fmt.Errorf("%s is a directory, not a file", canonical)
	}
	if info.Size() > maxFileBytes {
		return zero, fmt.Errorf("file %s is larger than the %d MiB read limit", canonical, maxFileBytes>>20)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return zero, fmt.Errorf("reading %s: %w", canonical, err)
	}
	sum := sha256.Sum256(data)

	full, encoding, ok := textenc.DetectDecode(data)
	if !ok {
		return readFileOutput{
			Path:      canonical,
			Encoding:  "binary",
			SHA256:    hex.EncodeToString(sum[:]),
			SizeBytes: info.Size(),
		}, nil
	}

	totalLines := strings.Count(full, "\n")
	if full != "" && !strings.HasSuffix(full, "\n") {
		totalLines++
	}

	content := full
	truncated := false
	if startLine > 0 || endLine > 0 {
		content = sliceLines(full, startLine, endLine)
	}
	if len(content) > t.budget() {
		content = truncateUTF8(content, t.budget())
		truncated = true
	}

	out := readFileOutput{
		Path:       canonical,
		Content:    content,
		SHA256:     hex.EncodeToString(sum[:]),
		Encoding:   encoding,
		Truncated:  truncated,
		TotalLines: totalLines,
		SizeBytes:  info.Size(),
	}
	if truncated {
		// The cut may fall mid-line; continuing from the first incomplete
		// line re-reads it whole.
		first := startLine
		if first < 1 {
			first = 1
		}
		out.NextStartLine = first + strings.Count(content, "\n")
		out.ResourceURI = fileResourceURI(ws.ID(), canonical)
	}
	return out, nil
}

// sliceLines returns lines start..end (1-based, inclusive); start<=0 means
// from the beginning, end<=0 means to the end.
func sliceLines(s string, start, end int) string {
	lines := strings.SplitAfter(s, "\n")
	// SplitAfter leaves a trailing "" element when s ends with "\n".
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return ""
	}
	return strings.Join(lines[start-1:end], "")
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
