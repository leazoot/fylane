package approval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/txn"
)

func testBudgets(d time.Duration) Budgets {
	return Budgets{Default: d, PerProvider: map[string]time.Duration{"claude": 4 * d}}
}

func writeRequest(id string, ops ...txn.OpPreview) *txn.ApprovalRequest {
	if len(ops) == 0 {
		ops = []txn.OpPreview{{Type: txn.OpUpdate, Path: "a.txt", Diff: "-a\n+b\n"}}
	}
	return &txn.ApprovalRequest{
		ChangeSetID: id, WorkspaceID: "ws_x", WorkspaceName: "demo",
		Provider: "grok", Summary: "test", Operations: ops,
	}
}

func TestBudgetsFor(t *testing.T) {
	b := DefaultBudgets()
	// ChatGPT's stateless pipeline times out around 60s (measured
	// 2026-08-07), so it shares the default budget with grok.
	if b.For("grok") != 50*time.Second || b.For("chatgpt") != 50*time.Second {
		t.Errorf("grok/chatgpt budgets = %v/%v", b.For("grok"), b.For("chatgpt"))
	}
	if b.For("claude") != 240*time.Second {
		t.Errorf("claude budget = %v", b.For("claude"))
	}
	if b.For("unknown-platform") != 50*time.Second {
		t.Errorf("unknown platform budget = %v", b.For("unknown-platform"))
	}
}

func TestModeValidation(t *testing.T) {
	if _, err := New("always-allow", testBudgets(time.Second), nil); err == nil {
		t.Fatal("unknown mode accepted — there must be no always-allow mode")
	}
	if _, err := New("", Budgets{}, nil); err == nil {
		t.Fatal("zero budget accepted")
	}
	s, err := New("", testBudgets(time.Second), nil)
	if err != nil || s.mode != ModeSafe {
		t.Fatalf("default mode = %q, %v; want safe", s.mode, err)
	}
}

func TestSafeModeNeverAutoApproves(t *testing.T) {
	s, _ := New(ModeSafe, testBudgets(30*time.Millisecond), nil)
	d, err := s.Approve(context.Background(), writeRequest("chg_1",
		txn.OpPreview{Type: txn.OpCreate, Path: "new.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved || !d.Pending {
		t.Fatalf("safe-mode create decision = %+v, want pending (confirmation required)", d)
	}
}

func TestBalancedModeAutoApprovesSafeCreatesOnly(t *testing.T) {
	s, _ := New(ModeBalanced, testBudgets(30*time.Millisecond), nil)
	ctx := context.Background()

	d, err := s.Approve(ctx, writeRequest("chg_1",
		txn.OpPreview{Type: txn.OpCreate, Path: "new.txt"},
		txn.OpPreview{Type: txn.OpCreate, Path: "other.txt"}))
	if err != nil || !d.Approved {
		t.Fatalf("pure create decision = %+v, %v; want auto-approved", d, err)
	}

	// A sensitive create must not auto-approve.
	d, _ = s.Approve(ctx, writeRequest("chg_2",
		txn.OpPreview{Type: txn.OpCreate, Path: ".env", Sensitive: true}))
	if d.Approved {
		t.Fatal("sensitive create auto-approved")
	}
	// A mixed change set with an update must not auto-approve.
	d, _ = s.Approve(ctx, writeRequest("chg_3",
		txn.OpPreview{Type: txn.OpCreate, Path: "new.txt"},
		txn.OpPreview{Type: txn.OpUpdate, Path: "a.txt"}))
	if d.Approved {
		t.Fatal("create+update auto-approved")
	}
}

func TestResolveApproveWhileBlocked(t *testing.T) {
	notified := make(chan *Pending, 1)
	s, _ := New(ModeSafe, testBudgets(5*time.Second), func(p *Pending) { notified <- p })

	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := s.Approve(context.Background(), writeRequest("chg_1"))
		done <- d
	}()
	select {
	case p := <-notified:
		if p.Request.ChangeSetID != "chg_1" {
			t.Fatalf("onRequest got %+v", p.Request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onRequest not called")
	}

	if !s.Resolve("chg_1", true, "") {
		t.Fatal("Resolve returned false")
	}
	d := <-done
	if !d.Approved || d.Reason != "user_approved" {
		t.Fatalf("decision = %+v", d)
	}
	if len(s.Pending()) != 0 {
		t.Fatal("pending entry not consumed")
	}
}

func TestResolveDeny(t *testing.T) {
	s, _ := New(ModeSafe, testBudgets(5*time.Second), nil)
	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := s.Approve(context.Background(), writeRequest("chg_1"))
		done <- d
	}()
	waitFor(t, func() bool { return len(s.Pending()) == 1 })
	s.Resolve("chg_1", false, "")
	d := <-done
	if d.Approved || d.Pending || d.Reason != "user_rejected" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestBudgetExpiryDegradesToPending(t *testing.T) {
	s, _ := New(ModeSafe, testBudgets(30*time.Millisecond), nil)
	d, err := s.Approve(context.Background(), writeRequest("chg_1"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Pending || d.Approved || d.Reason != "approval_budget_exceeded" {
		t.Fatalf("decision = %+v, want pending", d)
	}
	// The approval survives the budget: it is still listed and resolvable.
	if len(s.Pending()) != 1 {
		t.Fatal("pending approval dropped at budget expiry")
	}
	if !s.Resolve("chg_1", true, "") {
		t.Fatal("late Resolve failed")
	}
	// The retry attaches and consumes the stored decision immediately.
	d, err = s.Approve(context.Background(), writeRequest("chg_1"))
	if err != nil || !d.Approved {
		t.Fatalf("retry decision = %+v, %v", d, err)
	}
	if len(s.Pending()) != 0 {
		t.Fatal("entry not consumed after retry")
	}
}

func TestContextCancelIsPendingNotDenial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel as soon as the prompt is raised — the platform disconnecting
	// while approval is open.
	s, _ := New(ModeSafe, testBudgets(5*time.Second), func(*Pending) { cancel() })
	d, err := s.Approve(ctx, writeRequest("chg_1"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Pending || d.Approved {
		t.Fatalf("decision on disconnect = %+v, want pending", d)
	}
	if len(s.Pending()) != 1 {
		t.Fatal("approval dropped when caller disconnected")
	}
}

func TestRetryShapesSinglePrompt(t *testing.T) {
	prompts := make(chan struct{}, 8)
	s, _ := New(ModeSafe, testBudgets(20*time.Millisecond), func(*Pending) { prompts <- struct{}{} })
	for range 3 {
		d, err := s.Approve(context.Background(), writeRequest("chg_1"))
		if err != nil || !d.Pending {
			t.Fatalf("decision = %+v, %v", d, err)
		}
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want exactly 1 across retries", len(prompts))
	}
}

func TestOnDecisionReportsWait(t *testing.T) {
	type decided struct {
		id       string
		approved bool
		wait     time.Duration
	}
	got := make(chan decided, 8)
	s, _ := New(ModeSafe, testBudgets(20*time.Millisecond), nil)
	s.OnDecision = func(p *Pending, approved bool, wait time.Duration) {
		got <- decided{p.Request.ChangeSetID, approved, wait}
	}

	// The wait spans platform retries: the first Approve degrades to pending,
	// the decision arrives later, and OnDecision fires exactly once with the
	// wall-clock time since the prompt was created.
	d, err := s.Approve(context.Background(), writeRequest("chg_1"))
	if err != nil || !d.Pending {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	time.Sleep(30 * time.Millisecond)
	if !s.Resolve("chg_1", false, "") {
		t.Fatal("Resolve failed")
	}
	dec := <-got
	if dec.id != "chg_1" || dec.approved || dec.wait < 30*time.Millisecond {
		t.Fatalf("OnDecision = %+v", dec)
	}
	if s.Resolve("chg_1", true, "") {
		t.Fatal("second Resolve accepted")
	}
	if len(got) != 0 {
		t.Fatalf("OnDecision fired %d extra times", len(got))
	}

	// Auto-approved operations never report — their wait is zero.
	sb, _ := New(ModeBalanced, testBudgets(20*time.Millisecond), nil)
	sb.OnDecision = func(*Pending, bool, time.Duration) { t.Error("OnDecision called for auto-approval") }
	d, err = sb.Approve(context.Background(),
		writeRequest("chg_2", txn.OpPreview{Type: txn.OpCreate, Path: "new.txt"}))
	if err != nil || !d.Approved {
		t.Fatalf("auto-approve = %+v, %v", d, err)
	}
}

func TestApproveReadKeyedByPath(t *testing.T) {
	s, _ := New(ModeBalanced, testBudgets(20*time.Millisecond), nil)

	// Sensitive reads require confirmation in every mode.
	d, err := s.ApproveRead(context.Background(), "grok", "ws_x", "demo", ".env")
	if err != nil || !d.Pending {
		t.Fatalf("read decision = %+v, %v, want pending", d, err)
	}
	if !s.Resolve("read:grok:ws_x:.env", true, "") {
		t.Fatal("Resolve by read key failed")
	}
	d, err = s.ApproveRead(context.Background(), "grok", "ws_x", "demo", ".env")
	if err != nil || !d.Approved {
		t.Fatalf("read retry = %+v, %v", d, err)
	}
}

func TestAnUncollectedDecisionIsNotReplayedToAnotherPlatform(t *testing.T) {
	// The hole: the caller gave up waiting, so the decision sat in
	// `decided` for fifteen minutes. A second platform arriving with the same
	// key was treated as the first one retrying and was handed an approval
	// the user granted to somebody else. A key carrying the provider is the
	// first line; this is the backstop behind it.
	s, _ := New(ModeSafe, testBudgets(20*time.Millisecond), nil)
	req := func(provider string) *txn.ApprovalRequest {
		return &txn.ApprovalRequest{
			ChangeSetID: "cmd:shared", WorkspaceID: "ws_x", WorkspaceName: "demo",
			Provider: provider, Summary: "npm test",
			Kind: txn.KindCommand, Command: []string{"npm", "test"},
		}
	}

	// ChatGPT asks, times out waiting, and the user approves afterwards.
	if d, _ := s.Approve(context.Background(), req("chatgpt")); !d.Pending {
		t.Fatalf("first call = %+v, want pending", d)
	}
	if !s.Resolve("cmd:shared", true, "") {
		t.Fatal("Resolve failed")
	}

	// Grok now asks the same thing. It must be asked, not let through.
	d, err := s.Approve(context.Background(), req("grok"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Fatal("an approval granted to chatgpt was replayed to grok")
	}
	if !d.Pending {
		t.Fatalf("grok = %+v, want a fresh prompt", d)
	}
	if len(s.Pending()) != 1 {
		t.Fatalf("the second platform did not raise its own prompt: %d pending", len(s.Pending()))
	}

	// What the backstop does not do, deliberately: with one key for two
	// callers there is one slot, so grok's prompt now occupies it and a
	// chatgpt retry would attach to grok's question. Real callers do not
	// reach that state because their keys carry the provider; this is the
	// difference between "asked again" and "let through", and only the
	// second one is a security failure.
}

func TestOneCallerStillCollectsItsOwnUncollectedDecision(t *testing.T) {
	// The replay path is load-bearing: a platform that gave up waiting comes
	// back with the same key and must not make the user answer twice.
	s, _ := New(ModeSafe, testBudgets(20*time.Millisecond), nil)
	req := &txn.ApprovalRequest{
		ChangeSetID: "cmd:chatgpt:npm-test", WorkspaceID: "ws_x", WorkspaceName: "demo",
		Provider: "chatgpt", Summary: "npm test",
		Kind: txn.KindCommand, Command: []string{"npm", "test"},
	}
	if d, _ := s.Approve(context.Background(), req); !d.Pending {
		t.Fatalf("first call = %+v, want pending", d)
	}
	if !s.Resolve(req.ChangeSetID, true, "") {
		t.Fatal("Resolve failed")
	}
	if d, _ := s.Approve(context.Background(), req); !d.Approved {
		t.Fatalf("the caller lost its own decision: %+v", d)
	}
}

func TestADecisionIsCollectedOnceAndThenGone(t *testing.T) {
	// The decision exists so the retry we told the model to make finds the
	// answer the user already gave, and it must not outlive that retry: a
	// refusal that stuck would leave the user unable to change their mind
	// while the platform told them to "allow it again", and an approval that
	// stuck would silently cover every identical command until it expired.
	//
	// The property already held, as a side effect of remove() clearing both
	// maps. Nothing tested it, and it is the kind of property that survives
	// only while the side effect does.
	var prompts int
	s, _ := New(ModeSafe, testBudgets(20*time.Millisecond), func(*Pending) { prompts++ })
	req := &txn.ApprovalRequest{
		ChangeSetID: "cmd:chatgpt:ps", WorkspaceID: "ws_x", WorkspaceName: "demo",
		Provider: "chatgpt", Summary: "ps", Kind: txn.KindCommand, Command: []string{"ps"},
	}

	s.Approve(context.Background(), req)
	s.Resolve(req.ChangeSetID, false, "")
	if d, _ := s.Approve(context.Background(), req); d.Approved || d.Pending {
		t.Fatalf("the retry did not collect the refusal: %+v", d)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want the retry to be answered from the decision", prompts)
	}

	// A third call is a fresh request, minutes later or seconds later. The
	// user gets to change their mind.
	if d, _ := s.Approve(context.Background(), req); !d.Pending {
		t.Fatalf("a later request reused a spent decision: %+v", d)
	}
	if prompts != 2 {
		t.Fatalf("prompts = %d, want a new question rather than a replay", prompts)
	}
}

func TestApproveReadAsksAsADisclosureNotAsAWrite(t *testing.T) {
	// Reading .env changes nothing on disk. Asked as a pending write, the
	// prompt offered to show a diff that cannot exist and never said the
	// thing at stake: the file's contents go to the platform.
	var got *txn.ApprovalRequest
	s, _ := New(ModeSafe, testBudgets(20*time.Millisecond), func(p *Pending) { got = p.Request })

	if _, err := s.ApproveRead(context.Background(), "grok", "ws_x", "demo", ".env"); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no prompt was raised")
	}
	if got.Kind != txn.KindDisclosure {
		t.Errorf("kind = %q, want %q", got.Kind, txn.KindDisclosure)
	}
	if !strings.Contains(got.Reason, "grok") {
		t.Errorf("the prompt does not name who receives the file: %q", got.Reason)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A decision the user has already made must leave the pending list at once,
// even when the caller that asked for it has gone away. Otherwise the gate
// stays visibly shut over an approval that was granted (found on a real
// machine: pending_approvals stuck at 1 after Approve).
func TestDecidedApprovalLeavesThePendingList(t *testing.T) {
	svc, err := New(ModeSafe, DefaultBudgets(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &txn.ApprovalRequest{
		ChangeSetID: "chg_orphan",
		Operations:  []txn.OpPreview{{Type: txn.OpUpdate, Path: "a.md"}},
	}

	// The caller gives up waiting, exactly as a platform does after its own
	// timeout; the prompt stays open for the user.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dec, err := svc.Approve(ctx, req)
	if err != nil || !dec.Pending {
		t.Fatalf("decision = %+v, err = %v, want pending", dec, err)
	}
	if len(svc.Pending()) != 1 {
		t.Fatalf("pending = %d, want the prompt to stay open", len(svc.Pending()))
	}

	if !svc.Resolve("chg_orphan", true, "") {
		t.Fatal("resolve reported no such approval")
	}
	if got := svc.Pending(); len(got) != 0 {
		t.Fatalf("pending = %d after the decision, want 0", len(got))
	}

	// The retry still collects the decision instead of prompting again.
	retry, err := svc.Approve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Approved || retry.Pending {
		t.Fatalf("retry decision = %+v, want approved", retry)
	}
	if got := svc.Pending(); len(got) != 0 {
		t.Fatalf("pending = %d after the retry consumed the decision, want 0", len(got))
	}
}

// An uncollected decision must not authorise a write once it is stale.
func TestStaleDecisionIsNotReplayed(t *testing.T) {
	svc, err := New(ModeSafe, DefaultBudgets(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &txn.ApprovalRequest{
		ChangeSetID: "chg_stale",
		Operations:  []txn.OpPreview{{Type: txn.OpUpdate, Path: "a.md"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Approve(ctx, req); err != nil {
		t.Fatal(err)
	}
	if !svc.Resolve("chg_stale", true, "") {
		t.Fatal("resolve reported no such approval")
	}

	// Age the decision past its window.
	svc.mu.Lock()
	svc.decided["chg_stale"].decidedAt = time.Now().Add(-decisionTTL - time.Second)
	svc.mu.Unlock()

	done := make(chan txn.Decision, 1)
	go func() {
		d, _ := svc.Approve(context.Background(), req)
		done <- d
	}()
	deadline := time.After(2 * time.Second)
	for {
		if len(svc.Pending()) == 1 {
			break
		}
		select {
		case d := <-done:
			t.Fatalf("stale decision was replayed: %+v", d)
		case <-deadline:
			t.Fatal("retry never produced a fresh prompt")
		case <-time.After(5 * time.Millisecond):
		}
	}
	svc.Resolve("chg_stale", false, "")
	if d := <-done; d.Approved {
		t.Fatal("the fresh prompt's decision was not used")
	}
}

// Regression: balanced mode auto-approves a change set of nothing but safe
// creates by walking its operations. A request with no operations walked an
// empty list, found nothing to object to, and was approved — which is the
// shape a command approval has, so wiring commands through this
// service would have auto-approved every one of them in balanced mode.
func TestEmptyOperationListIsNeverAutoApproved(t *testing.T) {
	s, err := New(ModeBalanced, DefaultBudgets(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.autoApproved(&txn.ApprovalRequest{ChangeSetID: "cs-1"}) {
		t.Fatal("a request with no operations was auto-approved")
	}
}

func TestACommandIsNeverAutoApproved(t *testing.T) {
	s, err := New(ModeBalanced, DefaultBudgets(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &txn.ApprovalRequest{
		ChangeSetID: "cmd:1",
		Command:     []string{"rm", "-rf", "node_modules"},
		Rule:        "recursive-delete",
		// Even carrying an operation the file policy would wave through, a
		// command is judged by the rule table, not by this one.
		Operations: []txn.OpPreview{{Type: txn.OpCreate, Path: "x.txt"}},
	}
	if s.autoApproved(req) {
		t.Fatal("a command was auto-approved by the file policy table")
	}
}
