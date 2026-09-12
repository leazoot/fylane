package termapprove

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// harness runs a real approval service whose every request goes to the
// prompter, the way the Companion wires it. What is typed arrives through
// the pipe; what is shown lands in out.
type harness struct {
	svc   *approval.Service
	in    *io.PipeWriter
	out   *lockedBuffer
	asked chan *approval.Pending
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	r, w := io.Pipe()
	out := &lockedBuffer{}
	p := New(r, out)
	h := &harness{in: w, out: out, asked: make(chan *approval.Pending, 4)}
	svc, err := approval.New(approval.ModeSafe, approval.Budgets{Default: 5 * time.Second}, func(pend *approval.Pending) {
		h.asked <- pend
		go p.Ask(pend, h.svc.Resolve)
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	t.Cleanup(func() { w.Close() })
	return h
}

func (h *harness) typeLine(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(h.in, s+"\n"); err != nil {
		t.Fatal(err)
	}
}

func write(id string) *txn.ApprovalRequest {
	return &txn.ApprovalRequest{
		ChangeSetID: id, WorkspaceName: "my-app", Provider: "chatgpt", Summary: "add a readme",
		Operations: []txn.OpPreview{{Type: "create", Path: "README.md", Diff: "+# hi\n"}},
	}
}

func TestYesApprovesAndTheCallerGetsTheDecision(t *testing.T) {
	h := newHarness(t)
	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := h.svc.Approve(context.Background(), write("chg_1"))
		done <- d
	}()
	<-h.asked
	waitFor(t, h.out, "approve? [y/N]")
	h.typeLine(t, "y")
	if d := <-done; !d.Approved {
		t.Fatalf("typed y, decision = %+v", d)
	}
	if got := h.out.String(); !strings.Contains(got, "ChatGPT asks to write in my-app") || !strings.Contains(got, "create  README.md") {
		t.Errorf("prompt did not say what was asked:\n%s", got)
	}
}

func TestAnythingButYesRejects(t *testing.T) {
	h := newHarness(t)
	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := h.svc.Approve(context.Background(), write("chg_2"))
		done <- d
	}()
	<-h.asked
	waitFor(t, h.out, "approve?")
	h.typeLine(t, "")
	if d := <-done; d.Approved {
		t.Fatal("an empty line approved a write")
	}
}

func TestDeletingADirectoryNeedsTheWholeWord(t *testing.T) {
	h := newHarness(t)
	req := write("chg_3")
	req.Operations = []txn.OpPreview{{Type: "delete", Path: "build", RecursiveDelete: true}}
	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := h.svc.Approve(context.Background(), req)
		done <- d
	}()
	<-h.asked
	waitFor(t, h.out, "type yes")
	// The one key that approves an ordinary write is not enough here.
	h.typeLine(t, "y")
	if d := <-done; d.Approved {
		t.Fatal("a single y deleted a directory")
	}
}

func TestAnAnswerFromTheAppDropsTheTerminalPrompt(t *testing.T) {
	h := newHarness(t)
	go h.svc.Approve(context.Background(), write("chg_4"))
	<-h.asked
	waitFor(t, h.out, "approve?")
	if !h.svc.Resolve("chg_4", true, "") {
		t.Fatal("the app could not resolve the request")
	}
	waitFor(t, h.out, "answered elsewhere")

	// The next request is asked afresh — the earlier prompt did not eat
	// the input for it.
	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := h.svc.Approve(context.Background(), write("chg_5"))
		done <- d
	}()
	<-h.asked
	waitForN(t, h.out, "approve? [y/N]", 2)
	h.typeLine(t, "y")
	if d := <-done; !d.Approved {
		t.Fatalf("second prompt: decision = %+v", d)
	}
}

func TestTwoRequestsAreAskedOneAfterTheOther(t *testing.T) {
	h := newHarness(t)
	var got sync.Map
	for _, id := range []string{"chg_a", "chg_b"} {
		go func() {
			d, _ := h.svc.Approve(context.Background(), write(id))
			got.Store(id, d.Approved)
		}()
		<-h.asked
	}
	waitFor(t, h.out, "approve?")
	h.typeLine(t, "y")
	waitFor(t, h.out, "approved")
	h.typeLine(t, "n")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, okA := got.Load("chg_a")
		b, okB := got.Load("chg_b")
		if okA && okB {
			// Order of arrival is the order asked; the first got y.
			if a != true || b != false {
				t.Fatalf("a=%v b=%v, want the first approved and the second rejected", a, b)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("both requests were not decided")
}

func TestALongDiffIsCut(t *testing.T) {
	var out bytes.Buffer
	req := write("chg_6")
	req.Operations[0].Diff = strings.Repeat("+line\n", 100)
	render(&out, req)
	if !strings.Contains(out.String(), "… 60 more lines") {
		t.Errorf("100-line diff was not cut at %d:\n%s", diffLines, out.String())
	}
}

func waitFor(t *testing.T, out *lockedBuffer, want string) {
	t.Helper()
	waitForN(t, out, want, 1)
}

func waitForN(t *testing.T, out *lockedBuffer, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(out.String(), want) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never saw %q %d time(s) in:\n%s", want, n, out.String())
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
