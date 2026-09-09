package cmdgate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdrule"
	"github.com/leazoot/fylane/companion/internal/store"
)

// memGrants is an in-memory Grants.
type memGrants struct {
	byWorkspace map[string]*store.CommandGrant
}

func newMemGrants() *memGrants {
	return &memGrants{byWorkspace: map[string]*store.CommandGrant{}}
}

func (m *memGrants) CommandGrantFor(_ context.Context, id string) (*store.CommandGrant, error) {
	if g, ok := m.byWorkspace[id]; ok {
		return g, nil
	}
	return nil, store.ErrNotFound
}

func (m *memGrants) GrantCommands(_ context.Context, id, rung string, at time.Time) error {
	m.byWorkspace[id] = &store.CommandGrant{WorkspaceID: id, Rung: rung, GrantedAt: at}
	return nil
}

func (m *memGrants) RevokeCommands(_ context.Context, id string) error {
	delete(m.byWorkspace, id)
	return nil
}

func (m *memGrants) ListCommandGrants(context.Context) ([]*store.CommandGrant, error) {
	out := make([]*store.CommandGrant, 0, len(m.byWorkspace))
	for _, g := range m.byWorkspace {
		out = append(out, g)
	}
	return out, nil
}

var (
	allow    = cmdrule.Decision{Verdict: cmdrule.Allow}
	confirm  = cmdrule.Decision{Verdict: cmdrule.Confirm, Rule: "recursive-delete"}
	block    = cmdrule.Decision{Verdict: cmdrule.Block, Rule: "privilege-escalation"}
	disclose = cmdrule.Decision{Verdict: cmdrule.Disclose, Rule: "reads-machine-state"}
)

func decide(t *testing.T, g *Gate, d cmdrule.Decision) Verdict {
	t.Helper()
	v, err := g.Decide(context.Background(), "ws-1", d)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBlockedCommandsAreRefusedOnEveryRung(t *testing.T) {
	// The open rung turns off asking, not checking. If this ever passes on
	// Open, the rung has become the bypass flag the rules forbid.
	for _, rung := range []Rung{Strict, Workspace, Open} {
		g := New(newMemGrants(), string(rung), nil)
		if got := decide(t, g, block); got.Need != Refused {
			t.Fatalf("rung %s: got %s, want refused", rung, got.Need)
		}
	}
}

func TestStrictRungAsksAboutEverything(t *testing.T) {
	g := New(newMemGrants(), string(Strict), nil)
	if got := decide(t, g, allow); got.Need != Approval {
		t.Fatalf("ordinary command: got %s", got.Need)
	}
	if got := decide(t, g, confirm); got.Need != Approval {
		t.Fatalf("gated command: got %s", got.Need)
	}
	// Strict never hands out a workspace grant: that is the middle rung's
	// mechanism, and creating one here would silently downgrade the setting.
	if got := decide(t, g, allow); got.Grant {
		t.Fatal("the strict rung offered a workspace grant")
	}
}

func TestWorkspaceRungAsksOnceThenOnlyForGatedCommands(t *testing.T) {
	grants := newMemGrants()
	g := New(grants, string(Workspace), nil)

	first := decide(t, g, allow)
	if first.Need != Approval || !first.Grant {
		t.Fatalf("the first command should be the one-time question: %+v", first)
	}
	if err := g.Grant(context.Background(), "ws-1"); err != nil {
		t.Fatal(err)
	}

	if got := decide(t, g, allow); got.Need != Allowed {
		t.Fatalf("after granting, ordinary work should not ask: %s", got.Need)
	}
	// The grant covers ordinary work, not `git push --force`.
	if got := decide(t, g, confirm); got.Need != Approval {
		t.Fatalf("a gated command ran under the workspace grant: %s", got.Need)
	}
}

func TestOpenRungRunsAllowedAndGatedCommandsWithoutAsking(t *testing.T) {
	g := New(newMemGrants(), string(Open), nil)
	for _, d := range []cmdrule.Decision{allow, confirm} {
		if got := decide(t, g, d); got.Need != Allowed {
			t.Fatalf("%s: got %s, want allowed", d.Verdict, got.Need)
		}
	}
}

// The open rung was sold as "run my daily commands without nagging
// me"; disclosing the machine is not that work, so this is the one question
// it cannot switch off. If this ever passes as Allowed on Open, a `ps aux`
// goes to a platform unannounced again.
func TestDisclosureIsAskedAboutOnEveryRungIncludingOpen(t *testing.T) {
	for _, rung := range []Rung{Strict, Workspace, Open} {
		g := New(newMemGrants(), string(rung), nil)
		if got := decide(t, g, disclose); got.Need != Approval {
			t.Fatalf("rung %s: got %s, want approval", rung, got.Need)
		}
	}
}

// A workspace grant is permission to work in a folder. It must not quietly
// become permission to read the machine around it.
func TestAWorkspaceGrantDoesNotCoverDisclosure(t *testing.T) {
	grants := newMemGrants()
	g := New(grants, string(Workspace), nil)
	if err := g.Grant(context.Background(), "ws-1"); err != nil {
		t.Fatal(err)
	}
	if got := decide(t, g, allow); got.Need != Allowed {
		t.Fatalf("the grant did not take effect: %s", got.Need)
	}
	got := decide(t, g, disclose)
	if got.Need != Approval {
		t.Fatalf("a machine-state read ran under the workspace grant: %s", got.Need)
	}
	// It is not the one-time question either: answering it must not hand out
	// a standing grant for every later disclosure.
	if got.Grant {
		t.Fatal("approving a disclosure would have granted the workspace")
	}
}

func TestSettingTheOpenRungNeedsAConfirmation(t *testing.T) {
	g := New(newMemGrants(), "", nil)
	if err := g.SetRung(Open, false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("got %v, want ErrConfirmationRequired", err)
	}
	if g.Rung() != DefaultRung {
		t.Fatalf("a refused change took effect: %s", g.Rung())
	}
	if err := g.SetRung(Open, true); err != nil {
		t.Fatal(err)
	}
	if g.Rung() != Open {
		t.Fatalf("got %s", g.Rung())
	}
	// The stricter rungs need no ceremony — friction belongs on the way out,
	// not on the way back.
	for _, rung := range []Rung{Strict, Workspace} {
		if err := g.SetRung(rung, false); err != nil {
			t.Fatalf("setting %s: %v", rung, err)
		}
	}
	if err := g.SetRung("whatever", true); err == nil {
		t.Fatal("an unknown rung was accepted")
	}
}

func TestTheDefaultRungIsTheMiddleOne(t *testing.T) {
	for _, stored := range []string{"", "nonsense", "OPEN"} {
		if got := New(newMemGrants(), stored, nil).Rung(); got != Workspace {
			t.Fatalf("stored %q produced rung %s; an unreadable setting must never open the gate", stored, got)
		}
	}
}

func TestAGrantDoesNotSurviveTighteningTheRung(t *testing.T) {
	grants := newMemGrants()
	g := New(grants, string(Workspace), nil)
	if err := g.Grant(context.Background(), "ws-1"); err != nil {
		t.Fatal(err)
	}
	if got := decide(t, g, allow); got.Need != Allowed {
		t.Fatalf("got %s", got.Need)
	}

	// Tightening the setting is the user asking to be involved again; a
	// grant taken under the laxer rung must not answer for them.
	if err := g.SetRung(Strict, false); err != nil {
		t.Fatal(err)
	}
	if got := decide(t, g, allow); got.Need != Approval {
		t.Fatalf("a workspace grant survived a tightening: %s", got.Need)
	}
}

func TestRevokeTakesEffectImmediately(t *testing.T) {
	ctx := context.Background()
	grants := newMemGrants()
	g := New(grants, string(Workspace), nil)
	if err := g.Grant(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Revoke(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	if got := decide(t, g, allow); got.Need != Approval {
		t.Fatalf("a revoked workspace still ran commands: %s", got.Need)
	}
	// Revoking what is not there is what the caller wanted anyway.
	if err := g.Revoke(ctx, "ws-1"); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestGrantsAreListable(t *testing.T) {
	// A grant in force but invisible is the whole failure mode of this
	// design, so the gate has to be able to show them.
	ctx := context.Background()
	g := New(newMemGrants(), string(Workspace), nil)
	if err := g.Grant(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	got, err := g.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].WorkspaceID != "ws-1" || got[0].Rung != string(Workspace) {
		t.Fatalf("got %+v", got)
	}
}

func TestTheRungIsPersisted(t *testing.T) {
	var stored string
	g := New(newMemGrants(), "", func(rung string) error {
		stored = rung
		return nil
	})
	if err := g.SetRung(Strict, false); err != nil {
		t.Fatal(err)
	}
	if stored != string(Strict) {
		t.Fatalf("stored %q", stored)
	}
	// A restart must come back at the same rung.
	if got := New(newMemGrants(), stored, nil).Rung(); got != Strict {
		t.Fatalf("got %s", got)
	}
}
