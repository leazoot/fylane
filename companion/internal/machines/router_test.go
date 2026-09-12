package machines

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type localMCP struct{ seen []string }

func (l *localMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	json.NewEncoder(&b).Encode(map[string]any{"method": r.Method})
	body := make([]byte, 4096)
	n, _ := r.Body.Read(body)
	l.seen = append(l.seen, r.Method+" "+strings.TrimSpace(string(body[:n])))
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"local":true}}`))
}

func onlineRouter(t *testing.T) (*Manager, *fakeRemote, *localMCP, http.Handler, string) {
	t.Helper()
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "vps", Host: "vps.example"})
	waitState(t, m, s.ID, StateOnline)
	local := &localMCP{}
	return m, remote, local, m.MCPHandler(local), s.ID
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Fylane-Provider", "claude")
	// The sessionless protocol names the method in a header; a router that
	// dropped it would break every forwarded call (found by the two-binary
	// test in cmd/companion).
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Authorization", "Bearer must-not-cross")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouterLeavesLocalCallsUntouched(t *testing.T) {
	_, remote, local, h, _ := onlineRouter(t)
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"workspace_id":"ws_local","path":"a.txt"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"workspace_info","arguments":{}}}`,
		`not json at all`,
	} {
		rec := post(h, body)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"local":true`) {
			t.Errorf("%s: answered %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(local.seen) != 5 || !strings.Contains(local.seen[2], "ws_local") {
		t.Errorf("local saw %v", local.seen)
	}
	if len(remote.mcpSeen) != 0 {
		t.Errorf("remote saw %v", remote.mcpSeen)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if len(local.seen) != 6 {
		t.Error("a GET did not reach the local handler")
	}
}

func TestRouterForwardsCallsForARemoteWorkspace(t *testing.T) {
	_, remote, local, h, _ := onlineRouter(t)
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read_file","arguments":{"workspace_id":"ws_remote1","path":"main.go"}}}`
	rec := post(h, body)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("answered %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"task_id":"task_r1"`) {
		t.Errorf("remote answer not passed through: %s", rec.Body.String())
	}
	if len(local.seen) != 0 {
		t.Errorf("local saw %v", local.seen)
	}
	if len(remote.mcpSeen) != 1 || remote.mcpSeen[0] != "claude tools/call "+body {
		t.Errorf("remote saw %v", remote.mcpSeen)
	}
	// A resource on that workspace goes the same way.
	post(h, `{"jsonrpc":"2.0","id":8,"method":"resources/read","params":{"uri":"fylane://ws_remote1/README.md"}}`)
	if len(remote.mcpSeen) != 2 || len(local.seen) != 0 {
		t.Errorf("resource read: remote %d local %d", len(remote.mcpSeen), len(local.seen))
	}
	// A revoked remote workspace is not offered and not routed.
	post(h, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"read_file","arguments":{"workspace_id":"ws_gone"}}}`)
	if len(local.seen) != 1 {
		t.Errorf("revoked workspace was routed remotely")
	}
}

func TestRouterListsRemoteWorkspacesWithTheirMachine(t *testing.T) {
	m, _, _, _, _ := onlineRouter(t)
	list := m.Workspaces(context.Background())
	if len(list) != 1 || list[0].WorkspaceID != "ws_remote1" || list[0].Machine != "vps" || list[0].Name != "api" || list[0].Mode != "read_write" {
		t.Fatalf("workspaces = %+v", list)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "/home/deploy") {
		t.Fatalf("a remote path leaked: %s", raw)
	}
}

func TestRouterMarksTheStandingMachinesCurrentFolder(t *testing.T) {
	m, _, _, _, id := onlineRouter(t)
	if list := m.Workspaces(context.Background()); len(list) != 1 || list[0].Current {
		t.Fatalf("standing on this computer, nothing remote is current: %+v", list)
	}
	if err := m.Select(id); err != nil {
		t.Fatal(err)
	}
	if list := m.Workspaces(context.Background()); len(list) != 1 || !list[0].Current {
		t.Fatalf("standing on the machine, its current folder is current: %+v", list)
	}
	if err := m.Select(""); err != nil {
		t.Fatal(err)
	}
	if list := m.Workspaces(context.Background()); len(list) != 1 || list[0].Current {
		t.Fatalf("back on this computer: %+v", list)
	}
}

func TestRouterRoutesTaskStatusToWhereTheTaskStarted(t *testing.T) {
	_, remote, local, h, _ := onlineRouter(t)
	post(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_command","arguments":{"workspace_id":"ws_remote1","command":["go","test"]}}}`)
	post(h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"task_status","arguments":{"task_id":"task_r1"}}}`)
	if len(remote.mcpSeen) != 2 || !strings.Contains(remote.mcpSeen[1], "task_status") {
		t.Errorf("remote saw %v", remote.mcpSeen)
	}
	post(h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"task_status","arguments":{"task_id":"task_local"}}}`)
	if len(local.seen) != 1 || !strings.Contains(local.seen[0], "task_local") {
		t.Errorf("an unknown task id must stay local: %v", local.seen)
	}
}

func TestRouterAnswersForAMachineThatWentAway(t *testing.T) {
	m, remote, local, h, id := onlineRouter(t)
	// Learn the workspace while online, then lose the machine.
	m.Workspaces(context.Background())
	if err := m.Disconnect(id); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, id, StateOff)
	rec := post(h, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_file","arguments":{"workspace_id":"ws_remote1","path":"x"}}}`)
	var doc struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("answer %s: %v", rec.Body.String(), err)
	}
	if doc.ID != 5 || !doc.Result.IsError || !strings.Contains(doc.Result.Content[0].Text, "vps") {
		t.Errorf("answer = %s", rec.Body.String())
	}
	if len(local.seen) != 0 || len(remote.mcpSeen) != 0 {
		t.Errorf("local %v remote %v", local.seen, remote.mcpSeen)
	}
	if len(m.Workspaces(context.Background())) != 0 {
		t.Error("an offline machine's workspaces are still offered")
	}
}

func TestARemoteThatWritesNoMCPAddressIsReadFromItsLog(t *testing.T) {
	// The released 0.0.4 Companion has an MCP listener but writes only the
	// control address into its control file; its log is where the MCP port
	// is said. A machine running it must route, not read as online-but-mute.
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	remote.logMCP = remote.ctl.MCPAddr
	remote.ctl.MCPAddr = ""
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "vps", Host: "vps.example"})
	waitState(t, m, s.ID, StateOnline)
	local := &localMCP{}
	h := m.MCPHandler(local)
	rec := post(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"workspace_id":"ws_remote1","path":"a.txt"}}}`)
	if rec.Code != http.StatusOK || len(remote.mcpSeen) != 1 || strings.Contains(rec.Body.String(), "not connected") {
		t.Fatalf("forwarded call = %d %s; remote saw %d", rec.Code, rec.Body.String(), len(remote.mcpSeen))
	}

	// A log from an earlier start does not get to name the port of a later
	// one: the control address in the log has to match the control file.
	canned := dialerFunc(func(script string) string {
		return `version fylane-companion 0.0.4
control {"addr":"127.0.0.1:44435","token":"t","pid":7}
logctl time=x level=INFO msg="control api listening" addr=127.0.0.1:40000
logmcp time=x level=INFO msg="mcp server listening" addr=127.0.0.1:40499
`
	})
	p, err := (&link{mgr: New(Options{Store: &memStore{}, Dialer: canned, Version: "0.0.4-dev"}), m: Machine{Host: "x"}}).probe(context.Background())
	if err != nil || p.control == nil || p.control.MCPAddr != "" {
		t.Errorf("a log from another start must not name the port: %+v, %v", p.control, err)
	}
}

// dialerFunc is a Dialer whose Run answers from a function; Forward is never
// reached.
type dialerFunc func(script string) string

func (d dialerFunc) Run(_ context.Context, _ Machine, script string) (string, string, error) {
	return d(script), "", nil
}

func (d dialerFunc) Forward(context.Context, Machine, []Forward) (Link, error) {
	return nil, errors.New("not in this test")
}
