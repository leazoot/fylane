package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls until cond holds, so tests assert on a condition rather than
// on a sleep long enough to be flaky on a loaded machine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// blocking returns Work that waits for release, plus the release func.
func blocking() (Work, func()) {
	release := make(chan struct{})
	var once sync.Once
	work := func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error) {
		select {
		case <-release:
			return Outcome{}, nil
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
	}
	return work, func() { once.Do(func() { close(release) }) }
}

func instant(exit int) Work {
	return func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error) {
		fmt.Fprint(stdout, "done\n")
		return Outcome{ExitCode: exit}, nil
	}
}

func TestRunReturnsSynchronouslyWhenWorkFitsTheBudget(t *testing.T) {
	m := New(Options{})
	defer m.Close()

	got, err := m.Run(context.Background(), Meta{Label: "echo"}, instant(0))
	if err != nil {
		t.Fatal(err)
	}
	// A short command must cost one round trip, not a poll loop.
	if got.State != Succeeded {
		t.Fatalf("got state %s, want succeeded", got.State)
	}
	if got.Stdout != "done\n" {
		t.Fatalf("got stdout %q", got.Stdout)
	}
	if got.ID == "" {
		t.Fatal("even a synchronous result needs an id, so the caller has one code path")
	}
}

func TestRunBackgroundsWorkThatOutlivesTheBudget(t *testing.T) {
	m := New(Options{Budget: 50 * time.Millisecond})
	defer m.Close()
	work, release := blocking()
	defer release()

	started := time.Now()
	got, err := m.Run(context.Background(), Meta{Label: "npm test"}, work)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Running {
		t.Fatalf("got state %s, want running", got.State)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run waited %s; the budget is what stops the platform hanging up", elapsed)
	}

	release()
	waitFor(t, "the task to finish", func() bool {
		s, err := m.Status(got.ID, 0, 0)
		return err == nil && s.State == Succeeded
	})
}

func TestTaskSurvivesTheCallThatStartedIt(t *testing.T) {
	// The whole reason this package exists: the tool call dies at 60s and the
	// work must not die with it.
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()
	work, release := blocking()
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	got, err := m.Run(ctx, Meta{Label: "long"}, work)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	time.Sleep(50 * time.Millisecond)
	s, err := m.Status(got.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != Running {
		t.Fatalf("the task died with its caller: state %s", s.State)
	}
	release()
	waitFor(t, "completion", func() bool {
		s, _ := m.Status(got.ID, 0, 0)
		return s.State == Succeeded
	})
}

func TestRepeatedKeyReturnsTheSameTask(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()

	var starts atomic.Int32
	work, release := blocking()
	defer release()
	counted := func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error) {
		starts.Add(1)
		return work(ctx, stdout, stderr)
	}

	first, err := m.Run(context.Background(), Meta{Key: "req-1", Label: "npm test"}, counted)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Run(context.Background(), Meta{Key: "req-1", Label: "npm test"}, counted)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("a retried call started a second task: %s vs %s", first.ID, second.ID)
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("work ran %d times; a slow response must not run the build twice", n)
	}
}

func TestConcurrentRetriesOfTheSameKeyStartOneTask(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()

	var starts atomic.Int32
	work, release := blocking()
	defer release()

	const callers = 8
	ids := make([]string, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := m.Run(context.Background(), Meta{Key: "same"}, func(ctx context.Context, o, e io.Writer) (Outcome, error) {
				starts.Add(1)
				return work(ctx, o, e)
			})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = s.ID
		}(i)
	}
	wg.Wait()

	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("concurrent retries produced different tasks: %v", ids)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("work ran %d times under concurrent retry", n)
	}
}

func TestStatusReturnsOnlyWhatIsNewSinceTheCursor(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()

	step := make(chan struct{})
	work := func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error) {
		fmt.Fprint(stdout, "first\n")
		<-step
		fmt.Fprint(stdout, "second\n")
		fmt.Fprint(stderr, "warning\n")
		return Outcome{}, nil
	}

	got, err := m.Run(context.Background(), Meta{}, work)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first line", func() bool {
		s, _ := m.Status(got.ID, 0, 0)
		return strings.Contains(s.Stdout, "first")
	})
	s1, _ := m.Status(got.ID, 0, 0)
	if s1.Stdout != "first\n" {
		t.Fatalf("got %q", s1.Stdout)
	}

	close(step)
	waitFor(t, "completion", func() bool {
		s, _ := m.Status(got.ID, s1.StdoutCursor, 0)
		return s.State.Terminal()
	})

	s2, _ := m.Status(got.ID, s1.StdoutCursor, 0)
	// Re-sending output the caller already paid tokens for is the thing the
	// cursor exists to prevent.
	if s2.Stdout != "second\n" {
		t.Fatalf("got %q, want only the new line", s2.Stdout)
	}
	if s2.Stderr != "warning\n" {
		t.Fatalf("got stderr %q", s2.Stderr)
	}
	if s2.StdoutCursor <= s1.StdoutCursor {
		t.Fatal("the cursor did not advance")
	}
}

func TestStatusToleratesACursorPastTheEnd(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	got, _ := m.Run(context.Background(), Meta{}, instant(0))

	s, err := m.Status(got.ID, 9999, 9999)
	if err != nil {
		t.Fatalf("an out-of-range cursor must not be an error: %v", err)
	}
	if s.Stdout != "" {
		t.Fatalf("got %q, want nothing", s.Stdout)
	}
}

func TestOutputIsCappedAndReportsTruncation(t *testing.T) {
	m := New(Options{MaxOutputBytes: 32})
	defer m.Close()

	work := func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error) {
		for i := 0; i < 100; i++ {
			fmt.Fprint(stdout, "0123456789")
		}
		return Outcome{}, nil
	}
	got, err := m.Run(context.Background(), Meta{}, work)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stdout) != 32 {
		t.Fatalf("kept %d bytes, want the 32-byte cap", len(got.Stdout))
	}
	if !got.StdoutTruncated {
		t.Fatal("truncation was not reported")
	}
}

func TestFailureAndTimeoutAndCancellationAreDistinguished(t *testing.T) {
	m := New(Options{})
	defer m.Close()

	nonZero, _ := m.Run(context.Background(), Meta{}, instant(1))
	if nonZero.State != Failed || nonZero.ExitCode != 1 {
		t.Fatalf("non-zero exit: got %s/%d", nonZero.State, nonZero.ExitCode)
	}

	timedOut, _ := m.Run(context.Background(), Meta{}, func(context.Context, io.Writer, io.Writer) (Outcome, error) {
		return Outcome{ExitCode: -1, TimedOut: true}, nil
	})
	if timedOut.State != TimedOut {
		t.Fatalf("timeout: got %s", timedOut.State)
	}

	errored, _ := m.Run(context.Background(), Meta{}, func(context.Context, io.Writer, io.Writer) (Outcome, error) {
		return Outcome{}, errors.New("program not found")
	})
	if errored.State != Failed || errored.Error != "program not found" {
		t.Fatalf("setup failure: got %s/%q", errored.State, errored.Error)
	}
}

func TestCancelStopsARunningTask(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()
	work, release := blocking()
	defer release()

	got, _ := m.Run(context.Background(), Meta{}, work)
	if err := m.Cancel(got.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancellation", func() bool {
		s, _ := m.Status(got.ID, 0, 0)
		return s.State == Canceled
	})

	// Cancelling again, after the task settled, is not an error: a caller
	// racing the task's own completion should not have to care who won.
	if err := m.Cancel(got.ID); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
}

func TestCancellationIsNotReportedAsFailure(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()

	// Work that returns a non-zero exit *because* it was cancelled — the
	// shape a killed process actually has. Reporting that as a failed build
	// sends the user hunting a bug that is not there.
	work := func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error) {
		<-ctx.Done()
		return Outcome{ExitCode: -1}, nil
	}
	got, _ := m.Run(context.Background(), Meta{}, work)
	m.Cancel(got.ID)
	waitFor(t, "settle", func() bool {
		s, _ := m.Status(got.ID, 0, 0)
		return s.State.Terminal()
	})
	s, _ := m.Status(got.ID, 0, 0)
	if s.State != Canceled {
		t.Fatalf("got %s, want canceled", s.State)
	}
}

func TestCloseStopsEverythingStillRunning(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	work, release := blocking()
	defer release()

	got, _ := m.Run(context.Background(), Meta{}, work)
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return; the Core would hang on shutdown")
	}
	s, err := m.Status(got.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != Canceled {
		t.Fatalf("got %s, want canceled after Close", s.State)
	}
}

func TestRunAfterCloseIsRefused(t *testing.T) {
	m := New(Options{})
	m.Close()
	if _, err := m.Run(context.Background(), Meta{}, instant(0)); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want ErrClosed", err)
	}
}

func TestStatusOfAnUnknownTask(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	if _, err := m.Status("nope", 0, 0); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("got %v, want ErrUnknownTask", err)
	}
}

func TestTaskIDsAreUnguessable(t *testing.T) {
	m := New(Options{})
	defer m.Close()

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := m.Run(context.Background(), Meta{}, instant(0))
		if err != nil {
			t.Fatal(err)
		}
		if seen[s.ID] {
			t.Fatalf("duplicate id %s", s.ID)
		}
		if len(s.ID) < 16 {
			t.Fatalf("id %q is too short to be unguessable", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestRetentionDropsFinishedTasksButNeverRunningOnes(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := New(Options{Budget: 20 * time.Millisecond, Retention: time.Minute, now: clock.now})
	defer m.Close()

	finished, _ := m.Run(context.Background(), Meta{Key: "old"}, instant(0))
	work, release := blocking()
	defer release()
	running, _ := m.Run(context.Background(), Meta{}, work)

	clock.advance(2 * time.Minute)

	if _, err := m.Status(finished.ID, 0, 0); !errors.Is(err, ErrUnknownTask) {
		t.Fatal("a finished task past retention should have been dropped")
	}
	// Forgetting a running task would leave a process nothing can report on
	// or cancel.
	if _, err := m.Status(running.ID, 0, 0); err != nil {
		t.Fatalf("the running task was dropped: %v", err)
	}
	// The idempotency key must go with the task, or a later retry of the same
	// key maps to an id that no longer exists.
	again, err := m.Run(context.Background(), Meta{Key: "old"}, instant(0))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == finished.ID {
		t.Fatal("a purged task was resurrected by its key")
	}
}

func TestClearDropsFinishedTasksButNeverRunningOnes(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()

	finished, _ := m.Run(context.Background(), Meta{Key: "done"}, instant(0))
	work, release := blocking()
	defer release()
	running, _ := m.Run(context.Background(), Meta{}, work)

	if cleared := m.Clear(); cleared != 1 {
		t.Fatalf("cleared %d tasks, want 1", cleared)
	}
	if _, err := m.Status(finished.ID, 0, 0); !errors.Is(err, ErrUnknownTask) {
		t.Fatal("a finished task should have been cleared")
	}
	// Clearing the list must not amount to abandoning a live process: the
	// user asked to forget what ran, not to lose the handle on what is running.
	if _, err := m.Status(running.ID, 0, 0); err != nil {
		t.Fatalf("the running task was cleared: %v", err)
	}
	// The key goes with the record, or a later retry maps to a dead id.
	again, err := m.Run(context.Background(), Meta{Key: "done"}, instant(0))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == finished.ID {
		t.Fatal("a cleared task was resurrected by its key")
	}
}

func TestTheTableIsTrimmedToItsCap(t *testing.T) {
	m := New(Options{MaxTasks: 5})
	defer m.Close()

	var ids []string
	for i := 0; i < 20; i++ {
		s, err := m.Run(context.Background(), Meta{}, instant(0))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	if n := len(m.List()); n > 5 {
		t.Fatalf("retained %d tasks, want at most 5", n)
	}
	// The newest must survive; trimming the wrong end would drop the result
	// the caller is about to poll for.
	if _, err := m.Status(ids[len(ids)-1], 0, 0); err != nil {
		t.Fatalf("the newest task was trimmed: %v", err)
	}
}

// Regression: two tasks started in the same clock tick have equal
// startedAt, and a sort with no tie-break then puts them in any order — so a
// trim "from the oldest end" could evict the task whose result the caller is
// about to read. Windows ticks coarsely enough to hit this in a tight loop;
// a stopped clock reproduces it on every platform, every time.
func TestTrimBreaksTimestampTiesByArrival(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := New(Options{MaxTasks: 5, now: clock.now})
	defer m.Close()

	var ids []string
	for i := 0; i < 20; i++ {
		s, err := m.Run(context.Background(), Meta{}, instant(0))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	if _, err := m.Status(ids[len(ids)-1], 0, 0); err != nil {
		t.Fatalf("the newest of equal-timestamp tasks was trimmed: %v", err)
	}
	// And the list agrees on which one is newest.
	if got := m.List(); len(got) == 0 || got[0].ID != ids[len(ids)-1] {
		t.Fatalf("list head = %v, want the last-started task first", got)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := New(Options{now: clock.now})
	defer m.Close()

	for i := 0; i < 3; i++ {
		if _, err := m.Run(context.Background(), Meta{Label: fmt.Sprint(i)}, instant(0)); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Second)
	}
	got := m.List()
	if len(got) != 3 {
		t.Fatalf("got %d tasks", len(got))
	}
	if got[0].Label != "2" || got[2].Label != "0" {
		t.Fatalf("got order %s %s %s", got[0].Label, got[1].Label, got[2].Label)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAKeyReAttachesOnlyWhileTheWorkIsInFlight(t *testing.T) {
	m := New(Options{Budget: 20 * time.Millisecond})
	defer m.Close()

	first, err := m.Run(context.Background(), Meta{Key: "npm-test"}, instant(0))
	if err != nil {
		t.Fatal(err)
	}
	if first.State != Succeeded {
		t.Fatalf("got %s", first.State)
	}
	// Running the same suite again is an ordinary thing to do; replaying the
	// first result would be a lie about work that never happened.
	second, err := m.Run(context.Background(), Meta{Key: "npm-test"}, instant(1))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("a finished task was replayed for a repeated key")
	}
	if second.ExitCode != 1 {
		t.Fatalf("the second run did not actually run: exit %d", second.ExitCode)
	}
	// The first is still retained and still reports its own outcome.
	s, err := m.Status(first.ID, 0, 0)
	if err != nil || s.ExitCode != 0 {
		t.Fatalf("the earlier task was disturbed: %+v (%v)", s, err)
	}
}

func TestATaskIsStoppedAtTheRuntimeCeiling(t *testing.T) {
	m := New(Options{Budget: 5 * time.Millisecond, MaxRuntime: 40 * time.Millisecond})
	defer m.Close()

	work, release := blocking()
	defer release()
	started, _ := m.Run(context.Background(), Meta{}, work)
	if started.State != Running {
		t.Fatalf("expected the work to outlive the budget, got %s", started.State)
	}

	waitFor(t, "the ceiling to stop the task", func() bool {
		s, err := m.Status(started.ID, 0, 0)
		return err == nil && s.State.Terminal()
	})

	final, err := m.Status(started.ID, 0, 0)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// TimedOut and not Canceled: nobody asked for this, and reporting a
	// ceiling as a cancellation would send the user looking for who did.
	if final.State != TimedOut {
		t.Fatalf("state at the ceiling: got %s, want %s", final.State, TimedOut)
	}
	if !strings.Contains(final.Error, "ceiling") {
		t.Fatalf("the reason should say what stopped it, got %q", final.Error)
	}
}

func TestAnExplicitCancelIsStillReportedAsCancellation(t *testing.T) {
	// The ceiling must not swallow the distinction it was added next to.
	m := New(Options{Budget: 5 * time.Millisecond, MaxRuntime: time.Hour})
	defer m.Close()

	work, release := blocking()
	defer release()
	started, _ := m.Run(context.Background(), Meta{}, work)
	if err := m.Cancel(started.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitFor(t, "the task to end", func() bool {
		s, err := m.Status(started.ID, 0, 0)
		return err == nil && s.State.Terminal()
	})
	final, _ := m.Status(started.ID, 0, 0)
	if final.State != Canceled {
		t.Fatalf("state after cancel: got %s, want %s", final.State, Canceled)
	}
}
