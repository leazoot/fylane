package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/cmdrule"
	"github.com/leazoot/fylane/companion/internal/redact"
	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// git_query answers the questions a model asks before and after it edits:
// what changed, what is staged, what the history says. It needs no
// approval because nothing here writes — the verbs are a closed list and
// this Companion composes the command line itself; the caller picks a
// verb, a path, a ref and a count, and each is checked before it is
// placed. The rule table, the sandbox and the audit log still run: the
// ladder decides whether the user is asked, never whether the checks do.
//
// Git output can name files the listing hides. Entries for sensitive or
// excluded paths are dropped before the answer leaves, and counted, so the
// model knows something was there.

const (
	gitTimeout    = 30 * time.Second
	gitLogDefault = 20
	gitLogMax     = 100
	gitRefMax     = 200
)

// gitRefPattern is what a ref, a range or a commit may look like: never
// starting with a dash, so nothing here can become an option.
var gitRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./^~@{}-]*$`)

type gitQueryInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Op          string `json:"op" jsonschema:"status | diff | log | show | blame"`
	Path        string `json:"path,omitempty" jsonschema:"Workspace-relative path to restrict the answer to. Required for blame."`
	Ref         string `json:"ref,omitempty" jsonschema:"A commit, branch, tag or range such as HEAD~3..HEAD. show: the commit to show (default HEAD). diff: what to compare the working tree against. log: where history starts. blame: the revision to blame."`
	Staged      bool   `json:"staged,omitempty" jsonschema:"diff only: show what is staged (index against HEAD) instead of the working tree against the index."`
	Limit       int    `json:"limit,omitempty" jsonschema:"log only: how many commits, default 20, maximum 100."`
	StartLine   int    `json:"start_line,omitempty" jsonschema:"blame only: first line to blame, 1-based."`
	EndLine     int    `json:"end_line,omitempty" jsonschema:"blame only: last line to blame, inclusive."`
}

type gitQueryOutput struct {
	Op        string `json:"op"`
	Output    string `json:"output" jsonschema:"git's own output. status is porcelain v1 with a branch header line."`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"The output was cut at the inline budget; narrow it with path, ref or limit."`
	Hidden    int    `json:"hidden_entries,omitempty" jsonschema:"Entries for sensitive or excluded files that were left out of the output."`
}

func (t *toolset) gitQuery(ctx context.Context, _ *mcp.CallToolRequest, in gitQueryInput) (*mcp.CallToolResult, gitQueryOutput, error) {
	var zero gitQueryOutput
	if t.exec == nil {
		return nil, zero, fmt.Errorf("running git is not available on this Companion")
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	argv, err := t.gitArgv(ctx, ws, in)
	if err != nil {
		return nil, zero, err
	}
	if rule := cmdrule.Check(cmdrule.Request{Argv: argv, Root: ws.Root()}); rule.Verdict == cmdrule.Block || rule.Verdict == cmdrule.Disclose {
		return nil, zero, fmt.Errorf("git %s is not available here: %s", in.Op, rule.Reason)
	}
	res, err := t.exec.Run(ctx, cmdexec.Spec{
		WorkspaceID:    ws.ID(),
		Root:           ws.Root(),
		Argv:           argv,
		Provider:       t.provider,
		Timeout:        gitTimeout,
		MaxOutputBytes: t.budget(),
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "executable") {
			return nil, zero, fmt.Errorf("git is not installed on this machine")
		}
		return nil, zero, err
	}
	if res.TimedOut {
		return nil, zero, fmt.Errorf("git %s did not finish within %s; narrow it with path, ref or limit", in.Op, gitTimeout)
	}
	if res.ExitCode != 0 {
		return nil, zero, fmt.Errorf("git %s: %s", in.Op, gitFailure(ws, res.Stderr))
	}
	out := gitQueryOutput{Op: in.Op, Truncated: res.StdoutTruncated}
	switch in.Op {
	case "status":
		out.Output, out.Hidden = hideStatusEntries(ws, res.Stdout)
	case "diff", "show":
		out.Output, out.Hidden = hideDiffSections(ws, res.Stdout)
	default:
		out.Output = res.Stdout
	}
	out.Output = redact.Text(out.Output)
	if len(out.Output) > t.budget() {
		out.Output, out.Truncated = truncateUTF8(out.Output, t.budget()), true
	}
	return nil, out, nil
}

// gitArgv composes the command line. Every caller-supplied value is
// checked here and placed where git cannot read it as anything else.
func (t *toolset) gitArgv(ctx context.Context, ws *workspace.Workspace, in gitQueryInput) ([]string, error) {
	if in.Ref != "" && (len(in.Ref) > gitRefMax || !gitRefPattern.MatchString(in.Ref)) {
		return nil, fmt.Errorf("ref %q is not a commit, branch, tag or range", in.Ref)
	}
	path := ""
	if in.Path != "" {
		_, canonical, err := ws.Resolve(in.Path, sandbox.OpRead)
		if err != nil {
			return nil, err
		}
		if err := t.checkReadable(ctx, ws, canonical); err != nil {
			return nil, err
		}
		path = canonical
	}
	// --no-pager and --no-optional-locks keep git from waiting on a
	// terminal and from touching the index on a read; quotepath=false
	// keeps non-ASCII paths readable so the hiding below sees them.
	argv := []string{"git", "--no-pager", "--no-optional-locks", "-c", "core.quotepath=false"}
	switch in.Op {
	case "status":
		argv = append(argv, "status", "--porcelain=v1", "-b")
		if in.Ref != "" {
			return nil, fmt.Errorf("status takes no ref")
		}
	case "diff":
		argv = append(argv, "diff", "--no-color", "--no-ext-diff")
		if in.Staged {
			argv = append(argv, "--cached")
		}
		if in.Ref != "" {
			argv = append(argv, in.Ref)
		}
	case "log":
		n := in.Limit
		switch {
		case n <= 0:
			n = gitLogDefault
		case n > gitLogMax:
			return nil, fmt.Errorf("limit may not exceed %d", gitLogMax)
		}
		argv = append(argv, "log", "--no-color", "-n", strconv.Itoa(n), "--date=short", "--format=%h %ad %an: %s")
		if in.Ref != "" {
			argv = append(argv, in.Ref)
		}
	case "show":
		ref := in.Ref
		if ref == "" {
			ref = "HEAD"
		}
		argv = append(argv, "show", "--no-color", "--no-ext-diff", "--date=short", ref)
	case "blame":
		if path == "" {
			return nil, fmt.Errorf("blame needs a path")
		}
		if in.StartLine < 0 || in.EndLine < 0 || (in.EndLine > 0 && in.StartLine > in.EndLine) {
			return nil, fmt.Errorf("invalid line range: start_line and end_line must be positive and start_line <= end_line")
		}
		argv = append(argv, "blame", "--date=short")
		if in.StartLine > 0 || in.EndLine > 0 {
			first, last := max(in.StartLine, 1), ""
			if in.EndLine > 0 {
				last = strconv.Itoa(in.EndLine)
			}
			argv = append(argv, "-L", strconv.Itoa(first)+","+last)
		}
		if in.Ref != "" {
			argv = append(argv, in.Ref)
		}
	default:
		return nil, fmt.Errorf("op must be one of status, diff, log, show, blame")
	}
	if path != "" {
		argv = append(argv, "--", path)
	}
	return argv, nil
}

// gitFailure is git's first complaint, with the workspace root taken out
// of it: git names absolute paths in some messages, and those stay here.
func gitFailure(ws *workspace.Workspace, stderr string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	line = strings.ReplaceAll(line, ws.Root(), ".")
	if line == "" {
		return "failed"
	}
	return redact.Text(line)
}

// hidden says whether a path git named is one the workspace does not show.
func hidden(ws *workspace.Workspace, p string) bool {
	p = gitUnquote(strings.TrimSuffix(p, "/"))
	return p != "" && (ws.Sensitive(p) || ws.Excluded(p, false))
}

// gitUnquote undoes git's C-style quoting of paths with special bytes.
func gitUnquote(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// hideStatusEntries drops porcelain v1 lines naming hidden paths. A rename
// line names two; either hides it.
func hideStatusEntries(ws *workspace.Workspace, out string) (string, int) {
	var kept []string
	dropped := 0
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(line) < 4 || strings.HasPrefix(line, "## ") {
			if line != "" {
				kept = append(kept, line)
			}
			continue
		}
		rest := line[3:]
		from, to, renamed := strings.Cut(rest, " -> ")
		if (renamed && (hidden(ws, from) || hidden(ws, to))) || (!renamed && hidden(ws, rest)) {
			dropped++
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return "", dropped
	}
	return strings.Join(kept, "\n") + "\n", dropped
}

// hideDiffSections drops every per-file section of a diff whose file is
// hidden. The text before the first section (a commit header) is kept.
func hideDiffSections(ws *workspace.Workspace, out string) (string, int) {
	const mark = "diff --git "
	parts := strings.Split(out, "\n"+mark)
	if strings.HasPrefix(out, mark) {
		parts = append([]string{""}, strings.Split(out[len(mark):], "\n"+mark)...)
	}
	var b strings.Builder
	b.WriteString(parts[0])
	dropped := 0
	for _, section := range parts[1:] {
		if hidden(ws, diffSectionPath(section)) {
			dropped++
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(mark)
		b.WriteString(section)
	}
	return b.String(), dropped
}

// diffSectionPath reads the file a section is about from its +++ line, or
// its --- line for a deletion. The header line itself holds two paths that
// may contain spaces, so it is not parsed.
func diffSectionPath(section string) string {
	var minus string
	for _, line := range strings.SplitN(section, "\n", 12) {
		if p, ok := strings.CutPrefix(line, "+++ b/"); ok {
			return p
		}
		if p, ok := strings.CutPrefix(line, "--- a/"); ok {
			minus = p
		}
		if strings.HasPrefix(line, "@@") {
			break
		}
	}
	if minus != "" {
		return minus
	}
	// A rename without content change has no ---/+++ lines; the header
	// names it, and a path without spaces reads cleanly from it.
	first, _, _ := strings.Cut(section, "\n")
	if _, after, ok := strings.Cut(first, " b/"); ok {
		return after
	}
	return ""
}
