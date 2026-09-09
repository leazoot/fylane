package app

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/cmdrule"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// The authority matrix: for every combination of the settings a user can
// reach, is the user asked?
//
// This lives in app because app is the only package that builds both ladders
// (cmdgate in app.go, the approval service in audit.go) and is therefore the
// only place their independence can be stated. Each ladder already has point
// tests in its own package; what those cannot express is coverage. F29 was a
// crossing the point tests never enumerated — a rollback preview lost its
// Sensitive flag, and the balanced mode's auto-approval swallowed the prompt
// for it. Nothing was wrong with any single test; the combination simply had
// no owner.
//
// So the tests below enumerate the cross product rather than sampling it, and
// declare the expectation for every cell by hand. A formula derived from the
// implementation would restate the code and could be wrong in the same way.
//
// The invariant borrowed from the comparison is that the two ladders
// must not imply each other: neither one being lax may relax the other, and
// nothing is inferred from an adjacent setting. TestTheTwoLaddersDoNotImply
// states that directly.

// commandCell is one square of the command matrix. granted means the
// workspace holds a grant taken at this same rung.
type commandCell struct {
	rung    cmdgate.Rung
	verdict cmdrule.Verdict
	granted bool
}

func (c commandCell) String() string {
	return fmt.Sprintf("rung=%s verdict=%s granted=%v", c.rung, c.verdict, c.granted)
}

type commandWant struct {
	need cmdgate.Need
	// grant marks the one-time question that authorizes the workspace
	// rather than this one command.
	grant bool
}

// commandMatrix is the complete answer table. Every combination of the three
// axes appears exactly once; the test refuses to run if one is missing or if
// one is declared that the axes cannot produce.
var commandMatrix = map[commandCell]commandWant{
	// Refusals are the rung's hard floor: the open rung turns off asking,
	// not checking, and no grant buys past a blocked command.
	{cmdgate.Strict, cmdrule.Block, false}:    {cmdgate.Refused, false},
	{cmdgate.Strict, cmdrule.Block, true}:     {cmdgate.Refused, false},
	{cmdgate.Workspace, cmdrule.Block, false}: {cmdgate.Refused, false},
	{cmdgate.Workspace, cmdrule.Block, true}:  {cmdgate.Refused, false},
	{cmdgate.Open, cmdrule.Block, false}:      {cmdgate.Refused, false},
	{cmdgate.Open, cmdrule.Block, true}:       {cmdgate.Refused, false},

	// Disclosure is the one question that outlives the ladder: it
	// asks on every rung including the open one, and a workspace grant does
	// not cover it. The two open/granted rows are the whole of that claim.
	{cmdgate.Strict, cmdrule.Disclose, false}:    {cmdgate.Approval, false},
	{cmdgate.Strict, cmdrule.Disclose, true}:     {cmdgate.Approval, false},
	{cmdgate.Workspace, cmdrule.Disclose, false}: {cmdgate.Approval, false},
	{cmdgate.Workspace, cmdrule.Disclose, true}:  {cmdgate.Approval, false},
	{cmdgate.Open, cmdrule.Disclose, false}:      {cmdgate.Approval, false},
	{cmdgate.Open, cmdrule.Disclose, true}:       {cmdgate.Approval, false},

	// A gated command is gated at strict and workspace, and a grant does not
	// cover it — the grant is for ordinary work, not `git push --force`. The
	// open rung does run it unasked, and that is the rung's stated meaning
	//: it is why the open rung is never the default and cannot be set
	// without an explicit confirmation.
	{cmdgate.Strict, cmdrule.Confirm, false}:    {cmdgate.Approval, false},
	{cmdgate.Strict, cmdrule.Confirm, true}:     {cmdgate.Approval, false},
	{cmdgate.Workspace, cmdrule.Confirm, false}: {cmdgate.Approval, false},
	{cmdgate.Workspace, cmdrule.Confirm, true}:  {cmdgate.Approval, false},
	{cmdgate.Open, cmdrule.Confirm, false}:      {cmdgate.Allowed, false},
	{cmdgate.Open, cmdrule.Confirm, true}:       {cmdgate.Allowed, false},

	// An ordinary command is the only place the grant does any work, and the
	// only place the one-time question is asked. The strict rows say a grant
	// buys nothing at that rung: tightening the setting means being asked
	// again, so the grant is not consulted at all.
	{cmdgate.Strict, cmdrule.Allow, false}:    {cmdgate.Approval, false},
	{cmdgate.Strict, cmdrule.Allow, true}:     {cmdgate.Approval, false},
	{cmdgate.Workspace, cmdrule.Allow, false}: {cmdgate.Approval, true},
	{cmdgate.Workspace, cmdrule.Allow, true}:  {cmdgate.Allowed, false},
	{cmdgate.Open, cmdrule.Allow, false}:      {cmdgate.Allowed, false},
	{cmdgate.Open, cmdrule.Allow, true}:       {cmdgate.Allowed, false},
}

func TestTheCommandAuthorityMatrixIsExhaustive(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	wsID := seedWorkspace(t, st)

	rungs := declaredRungs(t)
	verdicts := declaredVerdicts(t)

	seen := map[commandCell]bool{}
	for _, rung := range rungs {
		for _, verdict := range verdicts {
			for _, granted := range []bool{false, true} {
				cell := commandCell{rung, verdict, granted}
				want, ok := commandMatrix[cell]
				if !ok {
					t.Errorf("no expectation declared for %s\n"+
						"an axis gained a value; extend commandMatrix rather than "+
						"leaving the combination unowned", cell)
					continue
				}
				seen[cell] = true

				gate := cmdgate.New(st, string(rung), nil)
				if err := gate.SetRung(rung, true); err != nil {
					t.Fatalf("%s: SetRung: %v", cell, err)
				}
				if granted {
					if err := gate.Grant(ctx, wsID); err != nil {
						t.Fatalf("%s: Grant: %v", cell, err)
					}
				} else if err := gate.Revoke(ctx, wsID); err != nil {
					t.Fatalf("%s: Revoke: %v", cell, err)
				}

				got, err := gate.Decide(ctx, wsID, cmdrule.Decision{Verdict: verdict})
				if err != nil {
					t.Fatalf("%s: Decide: %v", cell, err)
				}
				if got.Need != want.need {
					t.Errorf("%s: need = %q, want %q", cell, got.Need, want.need)
				}
				if got.Grant != want.grant {
					t.Errorf("%s: grant = %v, want %v", cell, got.Grant, want.grant)
				}
			}
		}
	}

	for cell := range commandMatrix {
		if !seen[cell] {
			t.Errorf("commandMatrix declares %s, which the axes cannot produce", cell)
		}
	}

	// Every outcome the gate can return must be reached by some cell.
	// A matrix that never produces one of them is not describing the gate.
	for _, need := range declaredNeeds(t) {
		reached := false
		for _, want := range commandMatrix {
			if want.need == need {
				reached = true
				break
			}
		}
		if !reached {
			t.Errorf("no cell reaches need %q", need)
		}
	}
}

// writeCell is one square of the write matrix.
type writeCell struct {
	mode      string
	mustAsk   bool
	op        string
	sensitive bool
}

func (c writeCell) String() string {
	return fmt.Sprintf("mode=%s must_ask=%v op=%s sensitive=%v", c.mode, c.mustAsk, c.op, c.sensitive)
}

// autoApprovable is the complete set of shapes that reach disk without the
// user being asked. Declaring the exception rather than 32 rows is the point:
// the policy is "one shape passes, everything else stops", and widening it
// turns the other 31 cells red at once.
var autoApprovable = map[writeCell]bool{
	{approval.ModeBalanced, false, txn.OpCreate, false}: true,
}

func TestTheWriteApprovalMatrixIsExhaustive(t *testing.T) {
	modes := declaredModes(t)
	ops := []string{txn.OpCreate, txn.OpUpdate, txn.OpDelete, txn.OpMove}

	seen := map[writeCell]bool{}
	for _, mode := range modes {
		for _, mustAsk := range []bool{false, true} {
			for _, op := range ops {
				for _, sensitive := range []bool{false, true} {
					cell := writeCell{mode, mustAsk, op, sensitive}
					seen[cell] = true
					req := &txn.ApprovalRequest{
						ChangeSetID: "chg_" + cell.String(),
						WorkspaceID: "ws_1",
						Kind:        txn.KindWrite,
						MustAsk:     mustAsk,
						Operations: []txn.OpPreview{{
							Type: op, Path: "notes.md", Sensitive: sensitive,
						}},
					}
					asked := askedFor(t, mode, req)
					if want := !autoApprovable[cell]; asked != want {
						t.Errorf("%s: user asked = %v, want %v", cell, asked, want)
					}
				}
			}
		}
	}
	for cell := range autoApprovable {
		if !seen[cell] {
			t.Errorf("autoApprovable declares %s, which the axes cannot produce", cell)
		}
	}
}

func TestTheWritePolicyStopsShapesItWasNotWrittenFor(t *testing.T) {
	// The three shapes outside the matrix's axes. Each is auto-approvable on
	// every axis the matrix varies, so each one isolates a single guard: a
	// change set with nothing in it, a command (judged by the rule table, not
	// by this policy), and a command carried alongside a safe create.
	safeCreate := []txn.OpPreview{{Type: txn.OpCreate, Path: "new.md"}}
	for _, tc := range []struct {
		name string
		req  *txn.ApprovalRequest
	}{
		{"no operations at all", &txn.ApprovalRequest{
			ChangeSetID: "chg_empty", Kind: txn.KindWrite}},
		{"a command", &txn.ApprovalRequest{
			ChangeSetID: "chg_cmd", Kind: txn.KindCommand, Command: []string{"go", "test"}}},
		{"a command carrying a safe create", &txn.ApprovalRequest{
			ChangeSetID: "chg_both", Kind: txn.KindCommand,
			Command: []string{"go", "test"}, Operations: safeCreate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !askedFor(t, approval.ModeBalanced, tc.req) {
				t.Error("passed without asking; the file policy was never written for this shape")
			}
		})
	}
}

func TestTheTwoLaddersDoNotImply(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	wsID := seedWorkspace(t, st)

	// The open rung says "do not interrupt me while I work in this folder".
	// It is a statement about commands. Reading it as a statement about
	// writes would silently retire the whole change-set approval, so: an
	// ordinary update still asks, in either approval mode, with the rung as
	// open and as granted as it can be.
	gate := cmdgate.New(st, string(cmdgate.Open), nil)
	if err := gate.SetRung(cmdgate.Open, true); err != nil {
		t.Fatal(err)
	}
	if err := gate.Grant(ctx, wsID); err != nil {
		t.Fatal(err)
	}
	for _, mode := range declaredModes(t) {
		req := &txn.ApprovalRequest{
			ChangeSetID: "chg_open_" + mode, WorkspaceID: wsID, Kind: txn.KindWrite,
			Operations: []txn.OpPreview{{Type: txn.OpUpdate, Path: "main.go"}},
		}
		if !askedFor(t, mode, req) {
			t.Errorf("mode %s: an update passed without asking because the command rung is open", mode)
		}
	}

	// The mirror. Balanced mode is a statement about files that cannot
	// exist yet; it says nothing about what a command may do, and a command
	// the rule table gates must stay gated whichever mode is set.
	got, err := gate.Decide(ctx, wsID, cmdrule.Decision{Verdict: cmdrule.Disclose})
	if err != nil {
		t.Fatal(err)
	}
	if got.Need != cmdgate.Approval {
		t.Errorf("disclosure need = %q on the open rung with a workspace grant, want %q",
			got.Need, cmdgate.Approval)
	}
}

// askedFor runs one approval request through a real service in the given mode
// and reports whether the local prompt was raised. The prompt answers yes
// immediately, so the caller never blocks and the result under test is only
// whether the question was put at all.
func askedFor(t *testing.T, mode string, req *txn.ApprovalRequest) bool {
	t.Helper()
	asked := false
	var svc *approval.Service
	svc, err := approval.New(mode, approval.DefaultBudgets(), func(p *approval.Pending) {
		asked = true
		svc.Resolve(p.Request.ChangeSetID, true, "authority_matrix")
	})
	if err != nil {
		t.Fatalf("approval.New(%q): %v", mode, err)
	}
	if _, err := svc.Approve(context.Background(), req); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return asked
}

func declaredRungs(t *testing.T) []cmdgate.Rung {
	t.Helper()
	var out []cmdgate.Rung
	for _, v := range declaredConsts(t, "../cmdgate/cmdgate.go", "Strict") {
		out = append(out, cmdgate.Rung(v))
	}
	return out
}

func declaredVerdicts(t *testing.T) []cmdrule.Verdict {
	t.Helper()
	var out []cmdrule.Verdict
	for _, v := range declaredConsts(t, "../cmdrule/cmdrule.go", "Allow") {
		out = append(out, cmdrule.Verdict(v))
	}
	return out
}

func declaredNeeds(t *testing.T) []cmdgate.Need {
	t.Helper()
	var out []cmdgate.Need
	for _, v := range declaredConsts(t, "../cmdgate/cmdgate.go", "Allowed") {
		out = append(out, cmdgate.Need(v))
	}
	return out
}

func declaredModes(t *testing.T) []string {
	t.Helper()
	return declaredConsts(t, "../approval/approval.go", "ModeSafe")
}

// declaredConsts reads the axis values out of the source that declares them,
// returning every string constant in the block that also declares anchor.
//
// The axes could have been hand-written lists, and then adding a fourth rung
// would leave the matrix quietly covering three — which is the failure this
// whole file exists to stop, reproduced one level up. Reading the block means
// a new constant lands in the cross product on its next run, with no
// expectation declared for it, and the test says so.
func declaredConsts(t *testing.T, path, anchor string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var values []string
		anchored := false
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == anchor {
					anchored = true
				}
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unquoting %s: %v", path, lit.Value, err)
				}
				values = append(values, unquoted)
			}
		}
		if anchored {
			if len(values) == 0 {
				t.Fatalf("%s: the block declaring %s has no string constants", path, anchor)
			}
			return values
		}
	}
	t.Fatalf("%s: no const block declares %s", path, anchor)
	return nil
}
