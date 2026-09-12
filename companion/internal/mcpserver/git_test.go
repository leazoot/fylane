package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// gitRepo turns the fixture's workspace into a repository with one commit
// and a dirty tree: a.txt changed, .env changed, new.txt untracked.
func gitRepo(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "hello\n")
	write(".env", "SECRET=abc\n")
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	write("a.txt", "hello\nworld\n")
	write(".env", "SECRET=xyz\n")
	write("new.txt", "new\n")
}

func (f *execFixture) git(t *testing.T, in gitQueryInput) (gitQueryOutput, error) {
	t.Helper()
	_, out, err := f.tools.gitQuery(context.Background(), nil, in)
	return out, err
}

func TestGitQueryAnswersWithoutApprovalAndHidesWhatTheWorkspaceHides(t *testing.T) {
	// The approver would refuse: the tool must never reach it.
	f := newExecFixture(t, txn.Decision{Approved: false, Reason: "no"})
	gitRepo(t, f.root)

	status, err := f.git(t, gitQueryInput{Op: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.HasPrefix(status.Output, "## main") || !strings.Contains(status.Output, " M a.txt\n") || !strings.Contains(status.Output, "?? new.txt\n") {
		t.Errorf("status output:\n%s", status.Output)
	}
	if strings.Contains(status.Output, ".env") || status.Hidden != 1 {
		t.Errorf("the sensitive file shows in status (hidden=%d):\n%s", status.Hidden, status.Output)
	}

	diff, err := f.git(t, gitQueryInput{Op: "diff"})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(diff.Output, "+++ b/a.txt") || !strings.Contains(diff.Output, "+world") {
		t.Errorf("diff output:\n%s", diff.Output)
	}
	if strings.Contains(diff.Output, "xyz") || strings.Contains(diff.Output, ".env") || diff.Hidden != 1 {
		t.Errorf("the sensitive file's diff leaked (hidden=%d):\n%s", diff.Hidden, diff.Output)
	}
	if _, err := f.git(t, gitQueryInput{Op: "diff", Path: ".env"}); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Errorf("diff of a sensitive file was not held for confirmation: %v", err)
	}

	log, err := f.git(t, gitQueryInput{Op: "log", Limit: 1})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(log.Output), "\n"); len(lines) != 1 || !strings.HasSuffix(lines[0], " t: first") {
		t.Errorf("log output:\n%s", log.Output)
	}

	show, err := f.git(t, gitQueryInput{Op: "show"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(show.Output, "first") || !strings.Contains(show.Output, "+hello") || strings.Contains(show.Output, "SECRET") || show.Hidden != 1 {
		t.Errorf("show output (hidden=%d):\n%s", show.Hidden, show.Output)
	}

	blame, err := f.git(t, gitQueryInput{Op: "blame", Path: "a.txt", StartLine: 1, EndLine: 1})
	if err != nil {
		t.Fatalf("blame: %v", err)
	}
	if !strings.Contains(blame.Output, "hello") || strings.Contains(blame.Output, "world") {
		t.Errorf("blame output:\n%s", blame.Output)
	}

	if n := len(f.approver.seen); n != 0 {
		t.Errorf("git_query asked for approval %d times", n)
	}
	ok := 0
	for _, rec := range f.audit.all() {
		if rec.Argv[0] == "git" && rec.Outcome == cmdexec.OutcomeOK {
			ok++
		}
		if strings.Contains(strings.Join(rec.Argv, " "), f.root) {
			t.Errorf("an absolute path reached the audit log: %v", rec.Argv)
		}
	}
	if ok != 5 {
		t.Errorf("%d git runs were audited, want 5", ok)
	}
}

func TestGitQueryRefusesWhatCouldBecomeAnOption(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	gitRepo(t, f.root)
	for _, in := range []gitQueryInput{
		{Op: "show", Ref: "--output=owned"},
		{Op: "log", Ref: "-n1"},
		{Op: "diff", Path: "../outside"},
		{Op: "blame"},
		{Op: "log", Limit: 500},
		{Op: "checkout"},
		{Op: "status", Ref: "HEAD"},
	} {
		if _, err := f.git(t, in); err == nil {
			t.Errorf("%+v was accepted", in)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "owned")); err == nil {
		t.Fatal("a ref was read as an option and wrote a file")
	}
	if _, err := f.git(t, gitQueryInput{Op: "show", Ref: "no-such-ref"}); err == nil || !strings.Contains(err.Error(), "git show:") {
		t.Errorf("a bad ref did not surface git's complaint: %v", err)
	}
}
