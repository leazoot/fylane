package ctlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/store"
)

type stubGate struct{ rung cmdgate.Rung }

func (s *stubGate) Rung() cmdgate.Rung                   { return s.rung }
func (s *stubGate) SetRung(r cmdgate.Rung, _ bool) error { s.rung = r; return nil }
func (s *stubGate) Revoke(context.Context, string) error { return nil }
func (s *stubGate) List(context.Context) ([]*store.CommandGrant, error) {
	return nil, nil
}

type stubProxies []ProxyProvider

func (s stubProxies) Proxies() []ProxyProvider { return s }

// A provider marked trust: workspace is an authorization in force, and the
// same rule that put the command grants in this document puts these here: an
// authorization nobody can see is the failure mode.
func TestCommandSettingsCarriesTheForwardedProviders(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = &stubGate{rung: cmdgate.Workspace}
	f.srv.Proxies = stubProxies{
		{Name: "sqlite", Trust: "workspace"},
		{Name: "docs", Trust: "ask"},
	}

	resp, body := f.call(t, http.MethodGet, "/v1/commands", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/commands = %d %s", resp.StatusCode, body)
	}
	var doc commandSettings
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Providers) != 2 {
		t.Fatalf("providers = %+v, want both", doc.Providers)
	}
	if doc.Providers[0].Name != "sqlite" || doc.Providers[0].Trust != "workspace" {
		t.Errorf("first provider = %+v", doc.Providers[0])
	}
}

// A Core with no gateway configured answers with an empty list, not null: a
// page that has to special-case the absence of a field ends up special-casing
// it wrong.
func TestCommandSettingsAnswersAnEmptyProviderList(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = &stubGate{rung: cmdgate.Strict}

	_, body := f.call(t, http.MethodGet, "/v1/commands", f.token, nil)
	var doc struct {
		Providers []ProxyProvider `json:"providers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Providers == nil {
		t.Error("providers came back null; want an empty list")
	}
	if len(doc.Providers) != 0 {
		t.Errorf("providers = %+v, want none", doc.Providers)
	}
}

// There is no endpoint that changes this list, and there must not be: a
// provider is a program to run, and naming one has to stay something that
// happens in a file on this machine.
func TestNothingOnTheControlAPICanAddAProvider(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = &stubGate{rung: cmdgate.Strict}

	for _, path := range []string{"/v1/providers", "/v1/commands/providers", "/v1/proxies"} {
		resp, _ := f.call(t, http.MethodPost, path, f.token, map[string]any{
			"name": "anything", "command": []string{"sh"},
		})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d; the control API must have no way to name a program to run", path, resp.StatusCode)
		}
	}
}

type stubServers []LanguageServer

func (s stubServers) LanguageServers() []LanguageServer { return s }

// The language servers ride in the same document for the same reason the
// providers do: they are programs this Companion starts, and a start-up log
// line is not a place anyone looks.
func TestCommandSettingsCarriesTheInstalledLanguageServers(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = &stubGate{rung: cmdgate.Workspace}
	f.srv.LanguageServers = stubServers{
		{Name: "gopls", Extensions: []string{".go"}, Running: true},
		{Name: "pyright-langserver", Extensions: []string{".py"}},
	}

	resp, body := f.call(t, http.MethodGet, "/v1/commands", f.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/commands = %d %s", resp.StatusCode, body)
	}
	var doc commandSettings
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.LanguageServers) != 2 {
		t.Fatalf("language servers = %+v, want both", doc.LanguageServers)
	}
	got := doc.LanguageServers[0]
	if got.Name != "gopls" || !got.Running || len(got.Extensions) != 1 {
		t.Errorf("first server = %+v", got)
	}
	// One that is installed but not started is still listed. The list answers
	// "what may this Companion start", and a server nobody has used yet is
	// exactly the entry a person would be surprised to find missing.
	if doc.LanguageServers[1].Running {
		t.Errorf("second server = %+v, want it listed and not running", doc.LanguageServers[1])
	}
}

func TestCommandSettingsAnswersAnEmptyLanguageServerList(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = &stubGate{rung: cmdgate.Strict}

	_, body := f.call(t, http.MethodGet, "/v1/commands", f.token, nil)
	var doc struct {
		Servers []LanguageServer `json:"language_servers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Servers == nil {
		t.Error("language servers came back null; want an empty list")
	}
}

// Same rule, one rung stronger: a language server is a program this machine
// runs, so nothing reachable over the control API may name one either.
func TestNothingOnTheControlAPICanAddALanguageServer(t *testing.T) {
	f := newFixture(t)
	f.srv.Commands = &stubGate{rung: cmdgate.Strict}

	for _, path := range []string{"/v1/servers", "/v1/language-servers", "/v1/commands/servers"} {
		resp, _ := f.call(t, http.MethodPost, path, f.token, map[string]any{
			"name": "anything", "command": []string{"sh"},
		})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d; the control API must have no way to name a program to run", path, resp.StatusCode)
		}
	}
}

// The workspace list says what each folder's programs actually get, not what
// the folder asked for. The two differ on Linux and differ again on a machine
// with no boundary at all, and the page must not be the place that works that
// out — one rule, one implementation.
func TestTheWorkspaceListSaysWhatTheMachineWillActuallyDo(t *testing.T) {
	f := newFixture(t)
	f.srv.ReadBox = readbox.New(true)

	id := f.workspaceID(t)
	resp, body := f.call(t, http.MethodPost, "/v1/workspaces/network", f.token,
		map[string]any{"id": id, "allow": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/workspaces/network = %d %s", resp.StatusCode, body)
	}
	got := firstWorkspace(t, body)
	if got.Network != store.NetworkDeny {
		t.Errorf("network = %q, want the answer that was just given", got.Network)
	}
	// Whatever this machine can do, the word must not be the permissive one:
	// the workspace asked for no network, so the answer is denied, partial or
	// unbounded — never "allowed".
	if got.NetworkReach == string(readbox.ReachAllowed) || got.NetworkReach == "" {
		t.Errorf("reach = %q for a workspace that denies the network", got.NetworkReach)
	}

	_, body = f.call(t, http.MethodPost, "/v1/workspaces/network", f.token,
		map[string]any{"id": id, "allow": true})
	got = firstWorkspace(t, body)
	if got.Network != store.NetworkAllow || got.NetworkReach != string(readbox.ReachAllowed) {
		t.Errorf("network = %q reach = %q after allowing it", got.Network, got.NetworkReach)
	}
}

// A Companion with no boundary configured says so rather than reporting the
// wish as granted.
func TestWithNoBoundaryTheAnswerIsUnbounded(t *testing.T) {
	f := newFixture(t)
	id := f.workspaceID(t)
	_, body := f.call(t, http.MethodPost, "/v1/workspaces/network", f.token,
		map[string]any{"id": id, "allow": false})
	if got := firstWorkspace(t, body); got.NetworkReach != string(readbox.ReachUnbounded) {
		t.Errorf("reach = %q with no boundary, want unbounded", got.NetworkReach)
	}
}

func TestTheNetworkEndpointRefusesAnUnknownWorkspace(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.call(t, http.MethodPost, "/v1/workspaces/network", f.token,
		map[string]any{"id": "ws_nope", "allow": false})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST for an unknown workspace = %d, want 400", resp.StatusCode)
	}
	resp, _ = f.call(t, http.MethodPost, "/v1/workspaces/network", f.token,
		map[string]any{"allow": false})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST with no id = %d, want 400", resp.StatusCode)
	}
}

func firstWorkspace(t *testing.T, body []byte) workspaceView {
	t.Helper()
	var doc struct {
		Workspaces []workspaceView `json:"workspaces"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Workspaces) == 0 {
		t.Fatalf("no workspaces in %s", body)
	}
	return doc.Workspaces[0]
}

// workspaceID is the fixture's own workspace, which newFixture already added
// and selected.
func (f *fixture) workspaceID(t *testing.T) string {
	t.Helper()
	rec, err := f.manager.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rec.ID
}
