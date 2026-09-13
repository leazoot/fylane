package cmdgate

import (
	"sort"
	"sync"
	"time"
)

// A delegation hands a workspace to a coding agent Fylane does not review,
// so the prompt for it is asked at every rung and no workspace grant covers
// it. What one yes buys is bounded here instead: this agent, in this
// workspace, for DelegationTTL, after which the question is asked again.
// The grants live in memory on purpose — a restart asks again, which for an
// authorization this broad is the right side to err on — and every one in
// force is listed for the desktop, because an authorization nobody can see
// is the failure mode this whole design avoids.

// DelegationTTL is how long one yes to an agent lasts in a workspace: a
// working day, not a setting.
const DelegationTTL = 8 * time.Hour

// DelegationGrant is one agent authorized in one workspace until ExpiresAt.
type DelegationGrant struct {
	WorkspaceID string    `json:"workspace_id"`
	Agent       string    `json:"agent"`
	GrantedAt   time.Time `json:"granted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type delegationKey struct{ workspace, agent string }

// Delegations holds the standing agent authorizations. Safe for concurrent use.
type Delegations struct {
	mu     sync.Mutex
	grants map[delegationKey]DelegationGrant
	now    func() time.Time
}

func NewDelegations() *Delegations {
	return &Delegations{grants: map[delegationKey]DelegationGrant{}, now: time.Now}
}

// Granted reports whether this agent may work in this workspace right now.
func (d *Delegations) Granted(workspaceID, agent string) bool {
	if d == nil || workspaceID == "" || agent == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	g, ok := d.grants[delegationKey{workspaceID, agent}]
	return ok && d.now().Before(g.ExpiresAt)
}

// Grant records one yes. A second yes before the first runs out starts the
// clock again rather than stacking.
func (d *Delegations) Grant(workspaceID, agent string) DelegationGrant {
	now := d.now().UTC()
	g := DelegationGrant{WorkspaceID: workspaceID, Agent: agent, GrantedAt: now, ExpiresAt: now.Add(DelegationTTL)}
	d.mu.Lock()
	d.grants[delegationKey{workspaceID, agent}] = g
	d.mu.Unlock()
	return g
}

// Revoke withdraws one authorization; it takes effect on the next
// code_task. False means there was nothing to withdraw.
func (d *Delegations) Revoke(workspaceID, agent string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := delegationKey{workspaceID, agent}
	_, ok := d.grants[k]
	delete(d.grants, k)
	return ok
}

// List returns the grants still in force, soonest to expire last. Expired
// ones are dropped here rather than by a timer: nothing reads them anyway.
func (d *Delegations) List() []DelegationGrant {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	out := make([]DelegationGrant, 0, len(d.grants))
	for k, g := range d.grants {
		if !now.Before(g.ExpiresAt) {
			delete(d.grants, k)
			continue
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out
}
