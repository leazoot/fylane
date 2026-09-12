package ctlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/machines"
)

// stubMachines is a MachineControl with one online machine whose control
// API is a recorder: the test checks what the proxy sent through.
type stubMachines struct {
	list    []machines.Status
	remote  *httptest.Server
	seen    []string
	actions []string
	current string
}

func (s *stubMachines) List() []machines.Status { return s.list }
func (s *stubMachines) Add(m machines.Machine) (machines.Status, error) {
	m.ID = "m_new"
	st := machines.Status{Machine: m, State: machines.StateConnecting}
	s.list = append(s.list, st)
	return st, nil
}
func (s *stubMachines) Remove(id string) error     { return s.act("remove", id) }
func (s *stubMachines) Connect(id string) error    { return s.act("connect", id) }
func (s *stubMachines) Disconnect(id string) error { return s.act("disconnect", id) }
func (s *stubMachines) Install(id string) error    { return s.act("install", id) }
func (s *stubMachines) act(what, id string) error {
	for _, m := range s.list {
		if m.ID == id {
			s.actions = append(s.actions, what+":"+id)
			return nil
		}
	}
	return machines.ErrUnknown
}
func (s *stubMachines) Update(m machines.Machine) (machines.Status, error) {
	for i := range s.list {
		if s.list[i].ID == m.ID {
			s.list[i].Machine = m
			s.actions = append(s.actions, "update:"+m.ID)
			return s.list[i], nil
		}
	}
	return machines.Status{}, machines.ErrUnknown
}
func (s *stubMachines) Probe(_ context.Context, m machines.Machine) (machines.ProbeResult, error) {
	if m.Host == "" {
		return machines.ProbeResult{}, machines.ErrUnknown
	}
	return machines.ProbeResult{Reachable: true, Version: "0.0.4", Running: true, Compatible: true}, nil
}
func (s *stubMachines) Select(id string) error {
	if id != "" && id != "m1" {
		return machines.ErrUnknown
	}
	s.current = id
	return nil
}
func (s *stubMachines) Selected() string { return s.current }
func (s *stubMachines) Browse(_ context.Context, id, path string) (machines.Listing, error) {
	if id != "m1" {
		return machines.Listing{}, machines.ErrUnknown
	}
	s.actions = append(s.actions, "browse "+path)
	return machines.Listing{Path: "/home/dev", Parent: "/home", Home: "/home/dev",
		Entries: []machines.Entry{{Name: "app", Repo: true}}}, nil
}
func (s *stubMachines) Proxy(id string) (http.Handler, error) {
	for _, m := range s.list {
		if m.ID == id && m.State == machines.StateOnline {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.seen = append(s.seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
				w.Write([]byte(`{"from":"remote"}`))
			}), nil
		}
	}
	return nil, machines.ErrUnknown
}

func TestMachineEndpointsListAddAndAct(t *testing.T) {
	f := newFixture(t)
	stub := &stubMachines{list: []machines.Status{{Machine: machines.Machine{ID: "m_1", Name: "vps", Host: "vps.example"}, State: machines.StateOnline}}}
	f.srv.Machines = stub

	resp, body := f.call(t, "GET", "/v1/machines", f.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"vps"`) {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	resp, body = f.call(t, "POST", "/v1/machines/add", f.token, map[string]any{"name": "box", "host": "box.example", "user": "me", "port": 22})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add = %d %s", resp.StatusCode, body)
	}
	var st machines.Status
	json.Unmarshal(body, &st)
	if st.ID != "m_new" || st.State != machines.StateConnecting || st.User != "me" {
		t.Errorf("add returned %+v", st)
	}
	resp, body = f.call(t, "POST", "/v1/machines/update", f.token, map[string]any{"id": "m_1", "name": "vps", "host": "new.example", "port": 2222})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"new.example"`) {
		t.Errorf("update = %d %s", resp.StatusCode, body)
	}
	for _, action := range []string{"connect", "disconnect", "install", "remove"} {
		resp, body = f.call(t, "POST", "/v1/machines/"+action, f.token, map[string]string{"id": "m_1"})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d %s", action, resp.StatusCode, body)
		}
	}
	if got := strings.Join(stub.actions, ","); got != "update:m_1,connect:m_1,disconnect:m_1,install:m_1,remove:m_1" {
		t.Errorf("actions = %s", got)
	}
	resp, body = f.call(t, "POST", "/v1/machines/probe", f.token, map[string]any{"name": "x", "host": "box.example"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"reachable":true`) {
		t.Errorf("probe = %d %s", resp.StatusCode, body)
	}
	resp, body = f.call(t, "POST", "/v1/machines/select", f.token, map[string]string{"id": "m1"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"current":"m1"`) {
		t.Errorf("select = %d %s", resp.StatusCode, body)
	}
	resp, body = f.call(t, "GET", "/v1/machines", f.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"current":"m1"`) {
		t.Errorf("list after select = %d %s", resp.StatusCode, body)
	}
	resp, _ = f.call(t, "POST", "/v1/machines/select", f.token, map[string]string{"id": "m_nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("select unknown = %d", resp.StatusCode)
	}
	resp, body = f.call(t, "POST", "/v1/machines/select", f.token, map[string]string{"id": ""})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"current":""`) {
		t.Errorf("select this computer = %d %s", resp.StatusCode, body)
	}
	resp, body = f.call(t, "POST", "/v1/machines/browse", f.token, map[string]string{"id": "m1", "path": "/home/dev"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"repo":true`) {
		t.Errorf("browse = %d %s", resp.StatusCode, body)
	}
	resp, _ = f.call(t, "POST", "/v1/machines/browse", f.token, map[string]string{"id": "m_nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("browse unknown machine = %d", resp.StatusCode)
	}
	resp, _ = f.call(t, "POST", "/v1/machines/connect", f.token, map[string]string{"id": "m_nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown machine = %d", resp.StatusCode)
	}
	resp, _ = f.call(t, "POST", "/v1/machines/connect", f.token, map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing id = %d", resp.StatusCode)
	}
}

func TestMachineProxyStripsThePrefixAndKeepsTheQuery(t *testing.T) {
	f := newFixture(t)
	stub := &stubMachines{list: []machines.Status{{Machine: machines.Machine{ID: "m_1", Name: "vps"}, State: machines.StateOnline},
		{Machine: machines.Machine{ID: "m_2", Name: "down"}, State: machines.StateError}}}
	f.srv.Machines = stub

	resp, body := f.call(t, "GET", "/v1/machines/m_1/v1/changesets?workspace_id=ws_1", f.token, nil)
	if resp.StatusCode != http.StatusOK || string(body) != `{"from":"remote"}` {
		t.Fatalf("proxy = %d %s", resp.StatusCode, body)
	}
	if len(stub.seen) != 1 || stub.seen[0] != "GET /v1/changesets?workspace_id=ws_1" {
		t.Errorf("remote saw %v", stub.seen)
	}
	resp, _ = f.call(t, "POST", "/v1/machines/m_1/v1/approvals/resolve", f.token, map[string]any{"change_set_id": "chg_1", "approved": true})
	if resp.StatusCode != http.StatusOK || stub.seen[1] != "POST /v1/approvals/resolve?" {
		t.Errorf("post through proxy: %d %v", resp.StatusCode, stub.seen)
	}
	resp, _ = f.call(t, "GET", "/v1/machines/m_2/v1/status", f.token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("offline machine = %d", resp.StatusCode)
	}
	// The local token is still the gate: no token, no proxy.
	resp, _ = f.call(t, "GET", "/v1/machines/m_1/v1/status", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token = %d", resp.StatusCode)
	}
	// And the exact routes are not swallowed by the proxy pattern.
	resp, _ = f.call(t, "GET", "/v1/machines", f.token, nil)
	if resp.StatusCode != http.StatusOK || len(stub.seen) != 2 {
		t.Errorf("list went through the proxy: %d %v", resp.StatusCode, stub.seen)
	}
}

func TestMachineEndpointsAreAbsentWhenNotConfigured(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.call(t, "GET", "/v1/machines", f.token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("list = %d", resp.StatusCode)
	}
	resp, _ = f.call(t, "GET", "/v1/machines/m_1/v1/status", f.token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("proxy = %d", resp.StatusCode)
	}
}

func TestControlFileNamesTheMCPListener(t *testing.T) {
	f := newFixture(t)
	srv := &Server{Manager: f.manager, Store: f.st, Approvals: f.svc, MCPAddr: "127.0.0.1:8787"}
	dir := t.TempDir()
	ctx, cancel := contextWithCancel()
	defer cancel()
	if _, err := srv.Start(ctx, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := readControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mcp_addr":"127.0.0.1:8787"`) {
		t.Errorf("control file = %s", raw)
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func readControl(dir string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, controlFileName))
}
