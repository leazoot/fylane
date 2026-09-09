package txn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type scriptedImpacter struct {
	seen  []ImpactRequest
	reply *OpImpact
	delay time.Duration
}

func (s *scriptedImpacter) Impact(ctx context.Context, req ImpactRequest) *OpImpact {
	s.seen = append(s.seen, req)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil
		}
	}
	return s.reply
}

func TestChangedLinesAreThePreImageLines(t *testing.T) {
	before := "one\ntwo\nthree\nfour\n"
	after := "one\nTWO\nthree\nfour\n"
	if got := changedLines(before, after); len(got) != 1 || got[0] != 2 {
		t.Fatalf("changedLines = %v, want [2]", got)
	}
	// A deletion has to point at the lines that were there, not at what took
	// their place: the symbols being asked about are in the old file.
	if got := changedLines("a\nb\nc\nd\n", "a\nd\n"); len(got) == 0 || got[0] != 2 {
		t.Fatalf("changedLines on a deletion = %v, want it to start at 2", got)
	}
	if got := changedLines("same\n", "same\n"); got != nil {
		t.Fatalf("changedLines on no change = %v, want nothing", got)
	}
}

func TestWhichOperationsHaveAReachAtAll(t *testing.T) {
	// A file being created has no callers yet; a move does not change what a
	// symbol is; a directory delete has no one file to ask about, and it
	// already carries the louder of the two warnings.
	cases := []struct {
		name string
		p    *plannedOp
		want bool
	}{
		{"update", &plannedOp{op: Operation{Type: OpUpdate, Content: "b\n"}, oldData: []byte("a\n")}, true},
		{"update that changes nothing", &plannedOp{op: Operation{Type: OpUpdate, Content: "a\n"}, oldData: []byte("a\n")}, false},
		{"file delete", &plannedOp{op: Operation{Type: OpDelete}, oldData: []byte("a\n")}, true},
		{"directory delete", &plannedOp{op: Operation{Type: OpDelete}, isDir: true}, false},
		{"create", &plannedOp{op: Operation{Type: OpCreate, Content: "a\n"}}, false},
		{"move", &plannedOp{op: Operation{Type: OpMove}, oldData: []byte("a\n")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := impactLines(tc.p); ok != tc.want {
				t.Fatalf("impactLines applies = %v, want %v", ok, tc.want)
			}
		})
	}
	// A whole-file delete asks about the whole file rather than a line range.
	lines, _ := impactLines(&plannedOp{op: Operation{Type: OpDelete}, oldData: []byte("a\nb\n")})
	if lines != nil {
		t.Fatalf("a delete asked about lines %v, want the whole file", lines)
	}
}

func TestTheApprovalFaceCarriesTheReach(t *testing.T) {
	f := newFixture(t)
	base := f.write(t, "widget.go", "package a\n\nfunc Size() int { return 1 }\n")
	imp := &scriptedImpacter{reply: &OpImpact{Symbols: []string{"Size"}, Callers: 3}}
	f.engine.Impact = imp

	res, err := f.engine.Execute(context.Background(), f.ws, Request{
		ChangeSetID: "chg_impact", Provider: "test", Summary: "edit",
		Operations: []Operation{{Type: OpUpdate, Path: "widget.go",
			ExpectedSHA256: base,
			Content:        "package a\n\nfunc Size() int { return 2 }\n"}},
	})
	if err != nil || res.Status != StatusApplied {
		t.Fatalf("Execute = %+v, %v", res, err)
	}
	if len(imp.seen) != 1 {
		t.Fatalf("%d impact questions, want 1", len(imp.seen))
	}
	if imp.seen[0].Path != "widget.go" || len(imp.seen[0].Lines) == 0 {
		t.Fatalf("request = %+v, want the changed lines of widget.go", imp.seen[0])
	}
	if f.approver.last == nil || len(f.approver.last.Operations) != 1 {
		t.Fatal("no preview reached the approver")
	}
	got := f.approver.last.Operations[0].Impact
	if got == nil || got.Callers != 3 {
		t.Fatalf("preview impact = %+v, want 3 callers", got)
	}
}

func TestNoImpacterMeansNoLineRatherThanNoWrite(t *testing.T) {
	// The reach is a nicety on a screen. A Companion with no language server
	// must write exactly as it did before.
	f := newFixture(t)
	base := f.write(t, "widget.go", "package a\n")
	res, err := f.engine.Execute(context.Background(), f.ws, Request{
		ChangeSetID: "chg_none", Provider: "test", Summary: "edit",
		Operations: []Operation{{Type: OpUpdate, Path: "widget.go",
			ExpectedSHA256: base, Content: "package b\n"}},
	})
	if err != nil || res.Status != StatusApplied {
		t.Fatalf("Execute = %+v, %v", res, err)
	}
	if f.approver.last.Operations[0].Impact != nil {
		t.Fatal("an unconfigured impacter produced an impact")
	}
}

func TestAnImpacterThatHangsDoesNotHangTheWrite(t *testing.T) {
	// Someone is waiting at the prompt, and the platform has its own timeout.
	f := newFixture(t)
	base := f.write(t, "widget.go", "package a\n")
	f.engine.Impact = &scriptedImpacter{delay: time.Hour, reply: &OpImpact{Callers: 9}}

	done := make(chan *Result, 1)
	go func() {
		res, _ := f.engine.Execute(context.Background(), f.ws, Request{
			ChangeSetID: "chg_slow", Provider: "test", Summary: "edit",
			Operations: []Operation{{Type: OpUpdate, Path: "widget.go",
				ExpectedSHA256: base, Content: "package b\n"}},
		})
		done <- res
	}()
	select {
	case res := <-done:
		if res.Status != StatusApplied {
			t.Fatalf("status = %s", res.Status)
		}
		if f.approver.last.Operations[0].Impact != nil {
			t.Fatal("a timed-out impacter still produced a number")
		}
	case <-time.After(impactBudget + 10*time.Second):
		t.Fatal("the write is still waiting on the impact query")
	}
}

func TestTheReachNeverLeavesTheMachine(t *testing.T) {
	// OpPreview is the local approver's view. Nothing in it is part of what a
	// remote caller is told, and the root path in the request is the reason
	// that has to stay true.
	blob, err := json.Marshal(Result{Status: StatusApplied, ChangeSetID: "chg_1",
		Operations: []OpResult{{Path: "widget.go", Status: "updated"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "impact") || strings.Contains(string(blob), "callers") {
		t.Fatalf("the tool result carries the reach: %s", blob)
	}
}
