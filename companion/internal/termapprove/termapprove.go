// Package termapprove answers approval prompts from a terminal.
//
// It exists for a Companion with no window: `share` on a machine without
// the desktop app, or `serve` over SSH on a server. Until it did, a write or
// a command that needed a decision there waited for a window that was never
// going to open, while the help text promised it would "stop here".
//
// One prompt is shown at a time. A second request waits its turn; its
// platform meanwhile receives pending_approval and retries, so nothing is
// lost by the wait. A request the desktop app answers first is dropped from
// the terminal without consuming what was typed for it.
package termapprove

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// diffLines is how much of a diff is shown before it is cut. Enough to read
// a normal edit; a rewrite is better judged from its size than its body.
const diffLines = 40

// Prompter reads decisions from one input and writes prompts to one output.
type Prompter struct {
	out   io.Writer
	lines <-chan string
	mu    sync.Mutex
}

// New starts reading in line by line. Reading is owned by the Prompter from
// here on; nothing else may read from in.
func New(in io.Reader, out io.Writer) *Prompter {
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			lines <- strings.TrimSpace(sc.Text())
		}
	}()
	return &Prompter{out: out, lines: lines}
}

// Ask shows one request and blocks until it is answered here or settled
// elsewhere. resolve is the approval service's own Resolve.
func (p *Prompter) Ask(pend *approval.Pending, resolve func(changeSetID string, approved bool, reason string) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-pend.Done():
		return
	default:
	}
	req := pend.Request
	render(p.out, req)
	strict := needsFullYes(req)
	if strict {
		fmt.Fprint(p.out, "  type yes to approve, anything else rejects: ")
	} else {
		fmt.Fprint(p.out, "  approve? [y/N] ")
	}
	select {
	case <-pend.Done():
		fmt.Fprintln(p.out, "\n  answered elsewhere")
	case line, ok := <-p.lines:
		if !ok {
			// Input is gone: this prompt and every later one can only be
			// answered from the desktop app, which the pending list serves.
			fmt.Fprintln(p.out, "\n  input closed; approve from the Fylane app")
			return
		}
		approved := decide(line, strict)
		if !resolve(req.ChangeSetID, approved, "") {
			fmt.Fprintln(p.out, "  answered elsewhere")
			return
		}
		if approved {
			fmt.Fprintln(p.out, "  approved")
		} else {
			fmt.Fprintln(p.out, "  rejected")
		}
		// The platform stops waiting after its own budget and retries; an
		// answer that took longer is collected by that retry, which is worth
		// saying so a quiet chat window is not read as a lost decision.
		if time.Since(pend.CreatedAt) > 45*time.Second {
			fmt.Fprintln(p.out, "  (the platform will pick this up on its next try)")
		}
	}
}

// needsFullYes reports whether one key is too little for what is asked:
// deleting a whole directory, or a delete whose copy will not fit the
// recycle area. The desktop asks these twice; here the second ask is the
// whole word.
func needsFullYes(req *txn.ApprovalRequest) bool {
	for _, op := range req.Operations {
		if op.RecursiveDelete || op.BeyondUndo {
			return true
		}
	}
	return false
}

func decide(line string, strict bool) bool {
	line = strings.ToLower(line)
	if strict {
		return line == "yes"
	}
	return line == "y" || line == "yes"
}

func render(out io.Writer, req *txn.ApprovalRequest) {
	fmt.Fprintln(out)
	who := providerName(req.Provider)
	where := req.WorkspaceName
	if where == "" {
		where = req.WorkspaceID
	}
	switch req.Kind {
	case txn.KindCommand:
		fmt.Fprintf(out, "%s asks to run a command in %s\n", who, where)
		fmt.Fprintf(out, "  $ %s\n", strings.Join(req.Command, " "))
		if req.Dir != "" && req.Dir != "." {
			fmt.Fprintf(out, "  in ./%s\n", req.Dir)
		}
		if req.Reason != "" {
			fmt.Fprintf(out, "  %s\n", req.Reason)
		} else if req.Rule != "" {
			fmt.Fprintf(out, "  rule: %s\n", req.Rule)
		}
		if req.Network != "" {
			fmt.Fprintf(out, "  network: %s\n", req.Network)
		}
	case txn.KindDisclosure:
		fmt.Fprintf(out, "%s asks to read something outside the workspace %s\n", who, where)
		summary(out, req)
		operations(out, req)
	case txn.KindDelegation:
		fmt.Fprintf(out, "%s asks to hand work in %s to an agent that runs unattended\n", who, where)
		summary(out, req)
	case txn.KindProxy:
		fmt.Fprintf(out, "%s asks to use a tool on this machine that Fylane cannot check\n", who)
		summary(out, req)
	default:
		fmt.Fprintf(out, "%s asks to write in %s\n", who, where)
		summary(out, req)
		operations(out, req)
	}
}

func summary(out io.Writer, req *txn.ApprovalRequest) {
	if req.Summary != "" {
		fmt.Fprintf(out, "  %s\n", req.Summary)
	}
}

func operations(out io.Writer, req *txn.ApprovalRequest) {
	for _, op := range req.Operations {
		target := op.Path
		if op.To != "" {
			target = op.Path + " → " + op.To
		}
		var flags []string
		if op.Sensitive {
			flags = append(flags, "sensitive file")
		}
		if op.RecursiveDelete {
			flags = append(flags, "deletes a whole directory")
		}
		if op.BeyondUndo {
			flags = append(flags, "cannot be undone")
		}
		line := fmt.Sprintf("  %-7s %s", op.Type, target)
		if len(flags) > 0 {
			line += "   (" + strings.Join(flags, ", ") + ")"
		}
		fmt.Fprintln(out, line)
		if op.Diff != "" {
			lines := strings.Split(strings.TrimRight(op.Diff, "\n"), "\n")
			shown := lines
			if len(shown) > diffLines {
				shown = shown[:diffLines]
			}
			for _, l := range shown {
				fmt.Fprintf(out, "    %s\n", l)
			}
			if rest := len(lines) - len(shown); rest > 0 {
				fmt.Fprintf(out, "    … %d more lines\n", rest)
			}
		}
	}
}

func providerName(p string) string {
	switch p {
	case "chatgpt":
		return "ChatGPT"
	case "claude":
		return "Claude"
	case "grok":
		return "Grok"
	case "gemini":
		return "Gemini"
	case "":
		return "A platform"
	}
	return p
}
