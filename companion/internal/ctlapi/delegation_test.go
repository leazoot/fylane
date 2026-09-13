package ctlapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/cmdgate"
)

func TestDelegationGrantsAreListedWithTheirClockAndWithdrawn(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = cmdgate.New(f.st, "workspace", nil)
	d := cmdgate.NewDelegations()
	f.srv.Delegations = d
	d.Grant("ws_1", "codex")

	resp, raw := f.call(t, "GET", "/v1/commands", f.token, nil)
	var doc struct {
		Delegations     []cmdgate.DelegationGrant `json:"delegations"`
		DelegationHours int                       `json:"delegation_hours"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("commands = %d %s", resp.StatusCode, raw)
	}
	if len(doc.Delegations) != 1 || doc.Delegations[0].Agent != "codex" || doc.DelegationHours != 8 {
		t.Fatalf("delegations = %+v, hours %d", doc.Delegations, doc.DelegationHours)
	}
	resp, raw = f.call(t, "POST", "/v1/commands/revoke_delegation", f.token, map[string]string{"workspace_id": "ws_1"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("withdrawal without an agent = %d %s", resp.StatusCode, raw)
	}
	resp, raw = f.call(t, "POST", "/v1/commands/revoke_delegation", f.token, map[string]string{"workspace_id": "ws_1", "agent": "codex"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"delegations":[]`) {
		t.Fatalf("withdrawal = %d %s", resp.StatusCode, raw)
	}
	if d.Granted("ws_1", "codex") {
		t.Fatal("still granted after withdrawal")
	}
}
