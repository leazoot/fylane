//go:build !windows

// Execution tests. They spawn real processes on purpose: the guarantees this
// package makes — the tree dies on timeout, a noisy child never blocks, the
// environment does not leak — cannot be established against a fake.

package cmdexec

import (
	"bufio"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/sandbox"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	root := newWorkspace(t)
	r, rec := newRunner(t)

	res, err := r.Run(context.Background(), Spec{Root: root, Argv: []string{"echo", "hello", "world"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "hello world" {
		t.Fatalf("got stdout %q", got)
	}
	if res.ExitCode != 0 || res.TimedOut || res.StdoutTruncated {
		t.Fatalf("unexpected result %+v", res)
	}
	if got := rec.last(t); got.Outcome != OutcomeOK {
		t.Fatalf("got outcome %q, want ok", got.Outcome)
	}
}

func TestRunReportsANonZeroExitAsAResultNotAnError(t *testing.T) {
	root := newWorkspace(t)
	r, rec := newRunner(t)

	// A failing test suite is the case this engine exists to report; it must
	// not come back as a Go error the caller has to unwrap.
	res, err := r.Run(context.Background(), Spec{Root: root, Argv: []string{"false"}})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatal("expected a non-zero exit code")
	}
	if got := rec.last(t); got.Outcome != OutcomeFailed || got.ExitCode != res.ExitCode {
		t.Fatalf("audit did not record the failure: %+v", got)
	}
}

func TestRunDoesNotBlockOnAnOversizedStream(t *testing.T) {
	root := newWorkspace(t)
	r, _ := newRunner(t)

	// 2 MiB into a 64 KiB cap. If the cap made the writer report short
	// writes, dd would block on a full pipe and this would only end at the
	// timeout — so the assertion that matters is that dd exited by itself.
	res, err := r.Run(context.Background(), Spec{
		Root:           root,
		Argv:           []string{"dd", "if=/dev/zero", "bs=1024", "count=2048"},
		Timeout:        20 * time.Second,
		MaxOutputBytes: 64 << 10,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.TimedOut {
		t.Fatal("the writer blocked the child instead of discarding overflow")
	}
	if res.ExitCode != 0 {
		t.Fatalf("dd exited %d: %s", res.ExitCode, res.Stderr)
	}
	if len(res.Stdout) != 64<<10 {
		t.Fatalf("kept %d bytes, want exactly the 64 KiB cap", len(res.Stdout))
	}
	if !res.StdoutTruncated {
		t.Fatal("truncation was not reported")
	}
}

func TestRunTimesOutAndReportsIt(t *testing.T) {
	root := newWorkspace(t)
	r, rec := newRunner(t)

	started := time.Now()
	res, err := r.Run(context.Background(), Spec{
		Root:    root,
		Argv:    []string{"sleep", "30"},
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("took %s; the timeout did not stop the process", elapsed)
	}
	if got := rec.last(t); got.Outcome != OutcomeTimeout {
		t.Fatalf("got outcome %q, want timeout", got.Outcome)
	}
}

func TestRunDistinguishesCancellationFromTimeout(t *testing.T) {
	root := newWorkspace(t)
	r, rec := newRunner(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	res, err := r.Run(ctx, Spec{Root: root, Argv: []string{"sleep", "30"}, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The Core shutting down is not the command running too long; conflating
	// them would tell the user their build timed out when it did not.
	if res.TimedOut {
		t.Fatal("a cancelled parent context was reported as a timeout")
	}
	if got := rec.last(t); got.Outcome != OutcomeCanceled {
		t.Fatalf("got outcome %q, want canceled", got.Outcome)
	}
}

func TestKillReachesGrandchildren(t *testing.T) {
	// Exercises procGroup directly: Run refuses a shell as argv[0], but a
	// real build spawns exactly this shape of tree, and killing only the
	// direct child is what leaves orphans holding the output pipe.
	g := newProcGroup()
	defer g.close()

	cmd := osexec.Command("/bin/sh", "-c", "sleep 30 & echo $!; sleep 30")
	g.prepare(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := g.adopt(cmd); err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the grandchild pid: %v", err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parsing pid %q: %v", line, err)
	}
	if !processAlive(grandchild) {
		t.Fatal("the grandchild was never running")
	}

	if err := g.kill(cmd); err != nil {
		t.Fatalf("kill: %v", err)
	}
	cmd.Wait()

	deadline := time.Now().Add(killGrace + 5*time.Second)
	for processAlive(grandchild) {
		if time.Now().After(deadline) {
			syscall.Kill(grandchild, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the group kill", grandchild)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// processAlive reports whether pid still exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestRunGivesTheChildOnlyWhitelistedEnvironment(t *testing.T) {
	root := newWorkspace(t)
	r, _ := newRunner(t)

	t.Setenv("FYLANE_TEST_SECRET", "leaked-value")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leaked-value")

	res, err := r.Run(context.Background(), Spec{Root: root, Argv: []string{"env"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(res.Stdout, "leaked-value") {
		t.Fatalf("the parent environment leaked into the child:\n%s", res.Stdout)
	}
	for _, want := range []string{"TERM=dumb", "NO_COLOR=1", "PATH="} {
		if !strings.Contains(res.Stdout, want) {
			t.Fatalf("missing %q in the child environment:\n%s", want, res.Stdout)
		}
	}
}

func TestRunUsesTheRequestedSubdirectory(t *testing.T) {
	root := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "packages", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, rec := newRunner(t)

	res, err := r.Run(context.Background(), Spec{Root: root, Dir: "packages/web", Argv: []string{"pwd"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != filepath.Join(root, "packages", "web") {
		t.Fatalf("ran in %q", got)
	}
	// The audit row carries the relative directory only; an absolute path
	// there would end up in the database and the logs.
	got := rec.last(t)
	if got.Dir != "packages/web" {
		t.Fatalf("audited dir %q, want packages/web", got.Dir)
	}
	if strings.Contains(got.Dir, root) {
		t.Fatal("the audit record leaked the absolute workspace path")
	}
}

func TestRunExecutesAScriptInsideTheWorkspace(t *testing.T) {
	root := newWorkspace(t)
	script := filepath.Join(root, "scripts", "build.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho built\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := newRunner(t)

	// Deliberate: refusing shells as argv[0] is about the caller's grammar,
	// not about scripts the repository already contains. A repo's own
	// build.sh must stay runnable — see the package doc for what that means.
	res, err := r.Run(context.Background(), Spec{Root: root, Argv: []string{"./scripts/build.sh"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "built" {
		t.Fatalf("got %q", res.Stdout)
	}
}

// The executable bit only carries meaning on Unix; resolveProgram skips this
// check on Windows, where PATHEXT decides what is runnable.
func TestResolveProgramRejectsANonExecutableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores the executable bit")
	}
	root := newWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProgram(root, "", "./notes.txt"); !errors.Is(err, ErrProgramNotFile) {
		t.Fatalf("got %v, want ErrProgramNotFile", err)
	}
}

func TestRunRefusesAProgramReachedThroughASymlinkOutOfTheWorkspace(t *testing.T) {
	root := newWorkspace(t)
	outside := newWorkspace(t)
	target := filepath.Join(outside, "evil.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "evil.sh")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r, _ := newRunner(t)

	_, err := r.Run(context.Background(), Spec{Root: root, Argv: []string{"./evil.sh"}})
	if !errors.Is(err, sandbox.ErrSymlink) && !errors.Is(err, sandbox.ErrPathTraversal) {
		t.Fatalf("got %v, want a sandbox refusal", err)
	}
}

func TestRunRefusesAWorkingDirectoryReachedThroughASymlink(t *testing.T) {
	root := newWorkspace(t)
	outside := newWorkspace(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r, rec := newRunner(t)

	_, err := r.Run(context.Background(), Spec{Root: root, Dir: "escape", Argv: []string{"pwd"}})
	if !errors.Is(err, sandbox.ErrSymlink) && !errors.Is(err, sandbox.ErrPathTraversal) {
		t.Fatalf("got %v, want a sandbox refusal", err)
	}
	if got := rec.last(t); got.Outcome != OutcomeRefused {
		t.Fatalf("the refusal was not audited: %+v", got)
	}
}

func TestRunStreamsOutputToAnObserverWhileItRuns(t *testing.T) {
	root := newWorkspace(t)
	r, _ := newRunner(t)

	// The async task table needs to show progress before the process exits,
	// so the observer must see lines as they are produced, not at the end.
	seen := &syncBuffer{}
	res, err := r.Run(context.Background(), Spec{
		Root:   root,
		Argv:   []string{"echo", "streamed"},
		Stdout: seen,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(seen.String()); got != "streamed" {
		t.Fatalf("observer saw %q", got)
	}
	if strings.TrimSpace(res.Stdout) != "streamed" {
		t.Fatal("streaming must not replace the captured copy")
	}
}

func TestRunSurvivesAnObserverThatFails(t *testing.T) {
	root := newWorkspace(t)
	r, _ := newRunner(t)

	// io.MultiWriter would propagate this error, stop os/exec draining the
	// pipe, and hang the child on a problem that is none of its business.
	res, err := r.Run(context.Background(), Spec{
		Root:    root,
		Argv:    []string{"echo", "fine"},
		Stdout:  failingWriter{},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("a broken observer killed the run: %v", err)
	}
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != "fine" {
		t.Fatalf("got %q", res.Stdout)
	}
}

// syncBuffer is safe for the goroutine os/exec writes from.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("observer is broken") }
