// Package cmdgate decides whether a command needs a human before it runs.
//
// It sits between the rule table, which judges the command itself, and the
// approval service, which asks the user. What it adds is the user's chosen
// rung and the per-workspace grant — the two things that turn
// "this command is destructive" into "and therefore we do or do not stop".
//
// One invariant runs through all of it: the rung controls whether the user is
// *asked*, never whether the checks *run*. A blocked command stays blocked on
// every rung, including the open one, and every attempt is still audited.
package cmdgate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdrule"
	"github.com/leazoot/fylane/companion/internal/store"
)

// Rung is how much the user wants to be asked.
type Rung string

const (
	// Strict asks before every command, including ones the rule table has
	// no objection to.
	Strict Rung = "strict"
	// Workspace asks once per workspace, then only for commands the rule
	// table gates. This is the default.
	Workspace Rung = "workspace"
	// Open never asks. It is never the default and cannot be set without an
	// explicit confirmation.
	Open Rung = "open"
)

// DefaultRung is what a Companion runs at until the user changes it.
const DefaultRung = Workspace

// ErrConfirmationRequired is returned when the open rung is set without the
// caller confirming it. The confirmation is the point of the rung: it is the
// moment the user takes on what it means.
var ErrConfirmationRequired = errors.New("the open rung runs every allowed command without asking; set confirm to acknowledge that")

// Need is what has to happen before the command may run.
type Need string

const (
	// Allowed means run it.
	Allowed Need = "allowed"
	// Approval means ask the user first.
	Approval Need = "approval"
	// Refused means never, on any rung.
	Refused Need = "refused"
)

// Verdict is the gate's answer.
type Verdict struct {
	Need Need
	// Grant reports that approving this prompt should also authorize the
	// workspace, i.e. the user is being asked the one-time question rather
	// than one about this particular command.
	Grant bool
	Rung  Rung
	Rule  cmdrule.Decision
}

// Grants is the persistence the gate needs. Defined here so the gate depends
// only on what it uses.
type Grants interface {
	CommandGrantFor(ctx context.Context, workspaceID string) (*store.CommandGrant, error)
	GrantCommands(ctx context.Context, workspaceID, rung string, at time.Time) error
	RevokeCommands(ctx context.Context, workspaceID string) error
	ListCommandGrants(ctx context.Context) ([]*store.CommandGrant, error)
}

// Gate answers "does this need a human". Safe for concurrent use.
type Gate struct {
	grants Grants

	mu   sync.RWMutex
	rung Rung

	// persist stores the rung across restarts; nil keeps it in memory only.
	persist func(rung string) error
}

// New builds a Gate. An unknown or empty rung falls back to the default
// rather than failing: a config file that predates this setting, or one a
// user hand-edited, must not stop the Companion from starting — and the
// fallback is the stricter of the usable options, never Open.
func New(grants Grants, rung string, persist func(string) error) *Gate {
	g := &Gate{grants: grants, rung: DefaultRung, persist: persist}
	switch Rung(rung) {
	case Strict, Workspace, Open:
		g.rung = Rung(rung)
	}
	return g
}

// Rung reports the active rung.
func (g *Gate) Rung() Rung {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.rung
}

// SetRung changes the rung. Moving to Open requires confirm, and that is not
// a formality: it is the one rung where a destructive command runs with
// nobody watching, and the user has to have said so in those terms.
func (g *Gate) SetRung(rung Rung, confirm bool) error {
	switch rung {
	case Strict, Workspace:
	case Open:
		if !confirm {
			return ErrConfirmationRequired
		}
	default:
		return fmt.Errorf("unknown approval rung %q", rung)
	}
	g.mu.Lock()
	g.rung = rung
	g.mu.Unlock()
	if g.persist != nil {
		return g.persist(string(rung))
	}
	return nil
}

// Decide answers whether the command may run, needs approval, or is refused.
func (g *Gate) Decide(ctx context.Context, workspaceID string, d cmdrule.Decision) (Verdict, error) {
	rung := g.Rung()
	v := Verdict{Rung: rung, Rule: d}

	// Refusals come first and answer the same on every rung. The open rung
	// turns off asking, not checking.
	if d.Verdict == cmdrule.Block {
		v.Need = Refused
		return v, nil
	}
	// Disclosure is asked about before the rung is consulted, so the open
	// rung cannot waive it. The rung is a statement about how much
	// the user wants to be interrupted while work happens *in a workspace*;
	// handing over the machine's own state is not that work, and a grant
	// covering the workspace does not cover it either.
	if d.Verdict == cmdrule.Disclose {
		v.Need = Approval
		return v, nil
	}
	if rung == Open {
		v.Need = Allowed
		return v, nil
	}
	// A gated command is gated at both remaining rungs: the workspace grant
	// covers ordinary work, not `git push --force`.
	if d.Verdict == cmdrule.Confirm {
		v.Need = Approval
		return v, nil
	}
	if rung == Strict {
		v.Need = Approval
		return v, nil
	}

	granted, err := g.granted(ctx, workspaceID, rung)
	if err != nil {
		return Verdict{}, err
	}
	if granted {
		v.Need = Allowed
		return v, nil
	}
	// The first ordinary command in an unauthorized workspace is the
	// one-time question, so approving it grants the workspace rather than
	// only this command.
	v.Need = Approval
	v.Grant = true
	return v, nil
}

func (g *Gate) granted(ctx context.Context, workspaceID string, rung Rung) (bool, error) {
	if workspaceID == "" || g.grants == nil {
		return false, nil
	}
	grant, err := g.grants.CommandGrantFor(ctx, workspaceID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A grant taken at a laxer rung does not carry over to a stricter one:
	// the user tightening the setting means they want to be asked again.
	return Rung(grant.Rung) == rung, nil
}

// Grant authorizes a workspace at the current rung.
func (g *Gate) Grant(ctx context.Context, workspaceID string) error {
	if g.grants == nil {
		return fmt.Errorf("no grant storage configured")
	}
	return g.grants.GrantCommands(ctx, workspaceID, string(g.Rung()), time.Now().UTC())
}

// Revoke withdraws a workspace's authorization. It takes effect on the next
// command: nothing caches the answer.
func (g *Gate) Revoke(ctx context.Context, workspaceID string) error {
	if g.grants == nil {
		return fmt.Errorf("no grant storage configured")
	}
	return g.grants.RevokeCommands(ctx, workspaceID)
}

// List returns the grants in force, for the desktop to show. A grant that is
// active but invisible would be the whole failure mode of this design.
func (g *Gate) List(ctx context.Context) ([]*store.CommandGrant, error) {
	if g.grants == nil {
		return nil, nil
	}
	return g.grants.ListCommandGrants(ctx)
}
