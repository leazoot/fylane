package cmdgate

import (
	"testing"
	"time"
)

func TestADelegationGrantIsPerAgentPerWorkspaceAndRunsOut(t *testing.T) {
	d := NewDelegations()
	clock := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }

	if d.Granted("ws_1", "codex") {
		t.Fatal("granted before any yes")
	}
	g := d.Grant("ws_1", "codex")
	if !g.ExpiresAt.Equal(clock.Add(DelegationTTL)) {
		t.Fatalf("expires %v, want %v", g.ExpiresAt, clock.Add(DelegationTTL))
	}
	if !d.Granted("ws_1", "codex") {
		t.Fatal("not granted right after the yes")
	}
	// The yes is to this agent in this workspace and nothing wider.
	if d.Granted("ws_1", "claude") || d.Granted("ws_2", "codex") {
		t.Fatal("the grant leaked to another agent or workspace")
	}
	if d.Granted("", "codex") || d.Granted("ws_1", "") {
		t.Fatal("an empty key is granted")
	}

	clock = clock.Add(DelegationTTL - time.Minute)
	if !d.Granted("ws_1", "codex") {
		t.Fatal("ran out early")
	}
	// A second yes restarts the clock rather than stacking on the first.
	d.Grant("ws_1", "codex")
	clock = clock.Add(DelegationTTL - time.Minute)
	if !d.Granted("ws_1", "codex") {
		t.Fatal("the second yes did not restart the clock")
	}
	clock = clock.Add(2 * time.Minute)
	if d.Granted("ws_1", "codex") {
		t.Fatal("still granted after the ttl")
	}
	if list := d.List(); len(list) != 0 {
		t.Fatalf("an expired grant is still listed: %+v", list)
	}
}

func TestDelegationGrantsAreListedAndWithdrawn(t *testing.T) {
	d := NewDelegations()
	clock := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }
	d.Grant("ws_2", "codex")
	clock = clock.Add(time.Hour)
	d.Grant("ws_1", "claude")

	list := d.List()
	if len(list) != 2 || list[0].WorkspaceID != "ws_2" || list[1].Agent != "claude" {
		t.Fatalf("list = %+v", list)
	}
	if !d.Revoke("ws_2", "codex") {
		t.Fatal("withdrawing a live grant reported nothing to withdraw")
	}
	if d.Revoke("ws_2", "codex") {
		t.Fatal("withdrawing twice reported a second grant")
	}
	if d.Granted("ws_2", "codex") || !d.Granted("ws_1", "claude") {
		t.Fatal("the wrong grant was withdrawn")
	}
	var nilD *Delegations
	if nilD.Granted("ws_1", "claude") {
		t.Fatal("a nil Delegations grants")
	}
}
