package machines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRemote stands in for one machine and the ssh that reaches it. Its
// "control API" is a real HTTP server so the forward, the health check and
// the proxy are exercised for real; only ssh itself is replaced.
type fakeRemote struct {
	mu        sync.Mutex
	reachable bool
	stderr    string
	version   string // "" means not installed
	running   bool   // a live Companion wrote the control file
	stale     bool   // a control file from a dead Companion
	ctl       remoteControl
	installs  int
	starts    int
	links     []*fakeLink
	// home, when set, is a real directory that stands in for the remote
	// $HOME: the browse script runs in a local sh against it.
	home string

	srv   *httptest.Server
	mcp   *httptest.Server
	token string
	// mcpSeen records what the fake /mcp received: provider header + body.
	mcpSeen []string
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	f := &fakeRemote{reachable: true, token: "remote-secret"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/workspaces" {
			fmt.Fprint(w, `{"workspaces":[{"id":"ws_remote1","name":"api","mode":"read_write","status":"active","root_path":"/home/deploy/api"},{"id":"ws_gone","name":"old","mode":"read_only","status":"revoked"}],"current_workspace_id":"ws_remote1"}`)
			return
		}
		fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	t.Cleanup(f.srv.Close)
	f.mcp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.mcpSeen = append(f.mcpSeen, r.Header.Get("X-Fylane-Provider")+" "+r.Header.Get("Mcp-Method")+r.Header.Get("Authorization")+" "+string(body))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"task_id\":\"task_r1\"}}}\n\n")
	}))
	t.Cleanup(f.mcp.Close)
	f.ctl = remoteControl{Addr: "127.0.0.1:41000", Token: f.token, PID: 100, MCPAddr: "127.0.0.1:41001"}
	return f
}

func (f *fakeRemote) Run(_ context.Context, _ Machine, script string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.reachable {
		return "", f.stderr, errors.New("exit status 255")
	}
	switch {
	case strings.Contains(script, "version 2>/dev/null"):
		var b strings.Builder
		if f.version == "" {
			b.WriteString("version none\n")
		} else {
			fmt.Fprintf(&b, "version fylane-companion %s\n", f.version)
		}
		if f.running || f.stale {
			raw, _ := json.Marshal(f.ctl)
			fmt.Fprintf(&b, "control %s\n", raw)
		} else {
			b.WriteString("control none\n")
		}
		return b.String(), "", nil
	case strings.Contains(script, "install.sh"):
		f.installs++
		f.version = "0.0.4"
		return "installed", "", nil
	case strings.Contains(script, pathTerminator) && f.home != "":
		cmd := exec.Command("sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), "HOME="+f.home)
		cmd.Dir = f.home
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	case strings.Contains(script, "serve -data-dir"):
		f.starts++
		f.running, f.stale = true, false
		f.ctl.PID++
		f.ctl.Addr = fmt.Sprintf("127.0.0.1:%d", 41000+f.ctl.PID)
		return "", "", nil
	}
	return "", "", fmt.Errorf("unexpected script: %s", script)
}

func (f *fakeRemote) Forward(_ context.Context, _ Machine, forwards []Forward) (Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.reachable {
		return nil, explain(errors.New("exit status 255"), f.stderr)
	}
	l := &fakeLink{done: make(chan struct{})}
	for _, fw := range forwards {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", fw.LocalPort))
		if err != nil {
			return nil, err
		}
		l.listeners = append(l.listeners, ln)
		alive := f.running && (fw.RemoteAddr == f.ctl.Addr || fw.RemoteAddr == f.ctl.MCPAddr)
		target := f.srv.Listener.Addr().String()
		if fw.RemoteAddr == f.ctl.MCPAddr {
			target = f.mcp.Listener.Addr().String()
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				if !alive {
					// ssh accepts locally and closes when the remote port
					// refuses; this is what a stale control file looks like.
					c.Close()
					continue
				}
				go pipe(c, target)
			}
		}()
	}
	f.links = append(f.links, l)
	return l, nil
}

func pipe(c net.Conn, target string) {
	defer c.Close()
	up, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer up.Close()
	go io.Copy(up, c)
	io.Copy(c, up)
}

type fakeLink struct {
	once      sync.Once
	done      chan struct{}
	listeners []net.Listener
}

func (l *fakeLink) Done() <-chan struct{} { return l.done }
func (l *fakeLink) Close() {
	l.once.Do(func() {
		for _, ln := range l.listeners {
			ln.Close()
		}
		close(l.done)
	})
}

type memStore struct {
	mu      sync.Mutex
	list    []Machine
	current string
}

func (s *memStore) LoadCurrent() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}

func (s *memStore) SaveCurrent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = id
	return nil
}

func (s *memStore) Load() ([]Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Machine(nil), s.list...), nil
}

func (s *memStore) Save(list []Machine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append([]Machine(nil), list...)
	return nil
}

func harness(t *testing.T, remote *fakeRemote) (*Manager, *memStore) {
	t.Helper()
	minBackoff, maxBackoff = 20*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := &memStore{}
	m := New(Options{Store: st, Dialer: remote, Version: "0.0.4-dev"})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return m, st
}

func waitState(t *testing.T, m *Manager, id string, want State) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.List() {
			if s.ID == id && s.State == want {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got []State
	for _, s := range m.List() {
		got = append(got, s.State)
	}
	t.Fatalf("machine %s never reached %s; states %v", id, want, got)
	return Status{}
}

func through(t *testing.T, m *Manager, id, path string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := m.Proxy(id)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer local-token-must-not-leak")
	h.ServeHTTP(rec, req)
	return rec
}

func TestARunningMachineComesOnlineAndIsProxiedWithItsOwnToken(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, st := harness(t, remote)

	s, err := m.Add(Machine{Name: "vps", Host: "vps.example", User: "deploy", Port: 2222})
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateConnecting {
		t.Fatalf("state right after Add = %s", s.State)
	}
	got := waitState(t, m, s.ID, StateOnline)
	if got.Version != "0.0.4" {
		t.Errorf("version = %q", got.Version)
	}
	rec := through(t, m, s.ID, "/v1/tasks")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/v1/tasks"`) {
		t.Fatalf("proxy answered %d %s", rec.Code, rec.Body.String())
	}
	if remote.starts != 0 || remote.installs != 0 {
		t.Errorf("a running machine was started %d and installed %d times", remote.starts, remote.installs)
	}
	saved, _ := st.Load()
	if len(saved) != 1 || saved[0].Host != "vps.example" || saved[0].ID != s.ID {
		t.Errorf("store holds %+v", saved)
	}
	if eps := m.endpoints(); len(eps) != 1 || eps[0].mcpBase == "" || eps[0].name != "vps" || eps[0].token != remote.token {
		t.Errorf("endpoints = %+v", eps)
	}
}

func TestTheStatusNeverCarriesTheRemoteToken(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "vps", Host: "vps.example"})
	waitState(t, m, s.ID, StateOnline)
	raw, _ := json.Marshal(m.List())
	if strings.Contains(string(raw), remote.token) {
		t.Fatalf("status leaks the remote token: %s", raw)
	}
}

func TestAMachineWithoutFylaneWaitsForTheUserToInstall(t *testing.T) {
	remote := newFakeRemote(t)
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "fresh", Host: "fresh.example"})
	got := waitState(t, m, s.ID, StateMissing)
	if !strings.Contains(got.Detail, "not installed") || got.Reason != ReasonMissing {
		t.Errorf("detail = %q reason = %q", got.Detail, got.Reason)
	}
	time.Sleep(60 * time.Millisecond)
	if remote.installs != 0 {
		t.Fatal("installed without being asked")
	}
	if err := m.Install(s.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateOnline)
	if remote.installs != 1 || remote.starts != 1 {
		t.Errorf("installs=%d starts=%d", remote.installs, remote.starts)
	}
	if err := m.Install(s.ID); err == nil {
		t.Error("Install on an online machine should be refused")
	}
}

func TestAnOlderFylaneIsReportedAsMissingWithBothVersions(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.3", true
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "old", Host: "old.example"})
	got := waitState(t, m, s.ID, StateMissing)
	if !strings.Contains(got.Detail, "0.0.3") || !strings.Contains(got.Detail, "0.0.4-dev") {
		t.Errorf("detail = %q", got.Detail)
	}
	if got.Version != "0.0.3" || got.Reason != ReasonOutdated {
		t.Errorf("version = %q reason = %q", got.Version, got.Reason)
	}
}

func TestAStaleControlFileIsReplacedByAFreshStart(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.stale = "0.0.4", true
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "rebooted", Host: "r.example"})
	waitState(t, m, s.ID, StateOnline)
	if remote.starts != 1 {
		t.Errorf("starts = %d", remote.starts)
	}
	rec := through(t, m, s.ID, "/v1/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy after restart answered %d", rec.Code)
	}
}

func TestAnUnreachableMachineSaysWhyAndKeepsTrying(t *testing.T) {
	remote := newFakeRemote(t)
	remote.reachable = false
	remote.stderr = "Host key verification failed.\n"
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "new", Host: "new.example"})
	got := waitState(t, m, s.ID, StateError)
	if !strings.Contains(got.Detail, "known_hosts") || got.Reason != ReasonHostKey {
		t.Errorf("detail = %q reason = %q", got.Detail, got.Reason)
	}
	if _, err := m.Proxy(s.ID); err == nil {
		t.Error("Proxy on an errored machine should fail")
	}
	remote.mu.Lock()
	remote.reachable, remote.version, remote.running = true, "0.0.4", true
	remote.mu.Unlock()
	waitState(t, m, s.ID, StateOnline)
}

func TestADroppedSessionReconnectsWithoutRestartingTheRemote(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "vps", Host: "vps.example"})
	waitState(t, m, s.ID, StateOnline)
	remote.mu.Lock()
	first := remote.links[0]
	remote.mu.Unlock()
	first.Close()
	if got := waitState(t, m, s.ID, StateError); got.Reason != ReasonLost {
		t.Errorf("reason = %q", got.Reason)
	}
	waitState(t, m, s.ID, StateOnline)
	remote.mu.Lock()
	n, starts := len(remote.links), remote.starts
	remote.mu.Unlock()
	if n != 2 || starts != 0 {
		t.Errorf("links=%d starts=%d", n, starts)
	}
}

func TestDisconnectStopsTheLinkAndConnectResumesIt(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, _ := harness(t, remote)
	s, _ := m.Add(Machine{Name: "vps", Host: "vps.example"})
	waitState(t, m, s.ID, StateOnline)
	if err := m.Disconnect(s.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateOff)
	remote.mu.Lock()
	select {
	case <-remote.links[0].Done():
	case <-time.After(time.Second):
		t.Error("the forward was not closed")
	}
	remote.mu.Unlock()
	if err := m.Connect(s.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, s.ID, StateOnline)
}

func TestRemoveForgetsTheMachine(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, st := harness(t, remote)
	s, _ := m.Add(Machine{Name: "vps", Host: "vps.example"})
	waitState(t, m, s.ID, StateOnline)
	if err := m.Remove(s.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Error("still listed")
	}
	if saved, _ := st.Load(); len(saved) != 0 {
		t.Error("still stored")
	}
	if err := m.Remove(s.ID); !errors.Is(err, ErrUnknown) {
		t.Errorf("second Remove = %v", err)
	}
}

func TestStartReconnectsEveryStoredMachine(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	minBackoff, maxBackoff = 20*time.Millisecond, 50*time.Millisecond
	st := &memStore{list: []Machine{{ID: "m_a", Name: "a", Host: "a.example"}, {ID: "m_b", Name: "b", Host: "b.example"}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(Options{Store: st, Dialer: remote, Version: "0.0.4"})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "m_a", StateOnline)
	waitState(t, m, "m_b", StateOnline)
	if len(m.Online()) != 2 {
		t.Errorf("online = %d", len(m.Online()))
	}
}

func TestAddRejectsAnythingSSHCouldReadAsAnOption(t *testing.T) {
	m, _ := harness(t, newFakeRemote(t))
	for _, bad := range []Machine{
		{Name: "x", Host: "-oProxyCommand=evil"},
		{Name: "x", Host: "host name"},
		{Name: "x", Host: "h", User: "-l"},
		{Name: "x", Host: "h", Port: 70000},
		{Name: " ", Host: "h"},
	} {
		if _, err := m.Add(bad); err == nil {
			t.Errorf("Add(%+v) accepted", bad)
		}
	}
	if len(m.List()) != 0 {
		t.Error("a rejected machine was listed")
	}
}

func TestSSHArgumentsCarryUserPortAndBatchMode(t *testing.T) {
	args := baseArgs(Machine{Host: "vps.example", User: "deploy", Port: 2222})
	joined := strings.Join(args, " ")
	for _, want := range []string{"BatchMode=yes", "-p 2222", "deploy@vps.example"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}
	if args[len(args)-1] != "deploy@vps.example" {
		t.Errorf("target must be the last argument: %q", joined)
	}
	if got := baseArgs(Machine{Host: "alias"}); got[len(got)-1] != "alias" || strings.Contains(strings.Join(got, " "), "-p") {
		t.Errorf("bare alias args = %q", got)
	}
}

func TestExplainTurnsSSHErrorsIntoOneSentence(t *testing.T) {
	cases := map[string]string{
		"Warning: x\nHost key verification failed.":          "known_hosts",
		"deploy@h: Permission denied (publickey).":           "key-based login",
		"ssh: Could not resolve hostname h: nodename":        "resolve",
		"ssh: connect to host h port 22: Connection refused": "reach",
		"something else went wrong":                          "something else went wrong",
	}
	for stderr, want := range cases {
		if got := explain(errors.New("exit status 255"), stderr).Error(); !strings.Contains(got, want) {
			t.Errorf("explain(%q) = %q, want %q", stderr, got, want)
		}
	}
	if got := explain(errors.New("boom"), "").Error(); got != "boom" {
		t.Errorf("empty stderr = %q", got)
	}
}

func TestCompatibleIgnoresTheDevSuffixOnly(t *testing.T) {
	if !compatible("0.0.4", "0.0.4-dev") || !compatible("0.0.4", "0.0.4") {
		t.Error("same base should be compatible")
	}
	if compatible("0.0.3", "0.0.4") || compatible("", "0.0.4") {
		t.Error("different or missing versions must not be")
	}
	if installURL("v0.0.4") != "https://raw.githubusercontent.com/leazoot/fylane/v0.0.4/scripts/install.sh" {
		t.Error(installURL("v0.0.4"))
	}
}

func TestProbeAnswersWithoutSavingOrForwarding(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, st := harness(t, remote)
	res, err := m.Probe(context.Background(), Machine{Name: "x", Host: "vps.example"})
	if err != nil || !res.Reachable || res.Version != "0.0.4" || !res.Running || !res.Compatible {
		t.Fatalf("probe = %+v, %v", res, err)
	}
	if saved, _ := st.Load(); len(saved) != 0 || len(m.List()) != 0 || len(remote.links) != 0 {
		t.Error("a probe saved or connected something")
	}
	remote.mu.Lock()
	remote.version, remote.running = "", false
	remote.mu.Unlock()
	if res, _ = m.Probe(context.Background(), Machine{Name: "x", Host: "vps.example"}); !res.Reachable || res.Version != "" || res.Running || res.Compatible {
		t.Errorf("bare machine = %+v", res)
	}
	remote.mu.Lock()
	remote.reachable, remote.stderr = false, "Permission denied (publickey)."
	remote.mu.Unlock()
	if res, _ = m.Probe(context.Background(), Machine{Name: "x", Host: "vps.example"}); res.Reachable || !strings.Contains(res.Detail, "key-based") {
		t.Errorf("refused machine = %+v", res)
	}
	if _, err := m.Probe(context.Background(), Machine{Name: "x", Host: "-oBad"}); err == nil {
		t.Error("an option-shaped host must be refused before ssh sees it")
	}
}

func TestUpdateReconnectsWithTheCorrectedDetails(t *testing.T) {
	remote := newFakeRemote(t)
	remote.reachable, remote.stderr = false, "ssh: Could not resolve hostname vsp.example: nodename nor servname provided"
	m, st := harness(t, remote)
	s, _ := m.Add(Machine{Name: "HK", Host: "vsp.example", Port: 2222})
	got := waitState(t, m, s.ID, StateError)
	if got.Reason != ReasonResolve {
		t.Fatalf("reason = %q", got.Reason)
	}
	remote.mu.Lock()
	remote.reachable, remote.version, remote.running = true, "0.0.4", true
	remote.mu.Unlock()
	fixed, err := m.Update(Machine{ID: s.ID, Name: "HK", Host: "vps.example", User: "deploy", Port: 2222})
	if err != nil || fixed.State != StateConnecting || fixed.Host != "vps.example" {
		t.Fatalf("update = %+v, %v", fixed, err)
	}
	waitState(t, m, s.ID, StateOnline)
	if saved, _ := st.Load(); len(saved) != 1 || saved[0].Host != "vps.example" || saved[0].User != "deploy" {
		t.Errorf("store = %+v", saved)
	}
	if _, err := m.Update(Machine{ID: "m_nope", Name: "x", Host: "h"}); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown = %v", err)
	}
}

func TestBrowseWalksDirectoriesOverSSH(t *testing.T) {
	remote := newFakeRemote(t)
	remote.home = t.TempDir()
	for _, d := range []string{"proj/.git", "Notes", ".config", "it's here/sub", "$HOME"} {
		if err := os.MkdirAll(filepath.Join(remote.home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(remote.home, "a-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := harness(t, remote)
	st, err := m.Add(Machine{Name: "vps", Host: "vps.example"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	ls, err := m.Browse(ctx, st.ID, "")
	if err != nil || ls.Reason != "" {
		t.Fatalf("browse home = %+v, %v", ls, err)
	}
	if ls.Path != remote.home || ls.Home != remote.home || ls.Parent != filepath.Dir(remote.home) {
		t.Errorf("home listing = %+v", ls)
	}
	var names []string
	for _, e := range ls.Entries {
		names = append(names, fmt.Sprintf("%s repo=%v hidden=%v", e.Name, e.Repo, e.Hidden))
	}
	want := []string{
		"$HOME repo=false hidden=false",
		".config repo=false hidden=true",
		"it's here repo=false hidden=false",
		"Notes repo=false hidden=false",
		"proj repo=true hidden=false",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("entries:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}

	// Awkward names travel intact, and ~ means the machine's home.
	for _, dir := range []string{remote.home + "/it's here", "~/it's here", remote.home + "/$HOME"} {
		ls, err = m.Browse(ctx, st.ID, dir)
		if err != nil || ls.Reason != "" || !strings.HasPrefix(ls.Path, remote.home+"/") {
			t.Errorf("browse %q = %+v, %v", dir, ls, err)
		}
	}
	if ls, _ = m.Browse(ctx, st.ID, remote.home+"/it's here"); len(ls.Entries) != 1 || ls.Entries[0].Name != "sub" {
		t.Errorf("subdir listing = %+v", ls)
	}
	if ls, _ = m.Browse(ctx, st.ID, "~"); ls.Path != remote.home {
		t.Errorf("~ = %+v", ls)
	}

	// What is not a directory is a sentence, not an error.
	for _, dir := range []string{remote.home + "/nope", remote.home + "/a-file"} {
		if ls, err = m.Browse(ctx, st.ID, dir); err != nil || ls.Reason != ReasonNoDir || ls.Path != "" {
			t.Errorf("browse %q = %+v, %v", dir, ls, err)
		}
	}
	remote.mu.Lock()
	remote.reachable, remote.stderr = false, "Permission denied (publickey)."
	remote.mu.Unlock()
	if ls, err = m.Browse(ctx, st.ID, ""); err != nil || ls.Reason != ReasonAuth {
		t.Errorf("refused = %+v, %v", ls, err)
	}
	if _, err = m.Browse(ctx, "m_nope", ""); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown machine = %v", err)
	}
	if _, err = m.Browse(ctx, st.ID, "/tmp\nrm -rf /"); err == nil {
		t.Error("a path with a newline must be refused before the shell sees it")
	}
}

func TestSelectIsRememberedAndClearedWithTheMachine(t *testing.T) {
	remote := newFakeRemote(t)
	remote.version, remote.running = "0.0.4", true
	m, st := harness(t, remote)
	if m.Selected() != "" {
		t.Fatalf("a fresh manager stands on this computer, got %q", m.Selected())
	}
	saved, err := m.Add(Machine{Name: "vps", Host: "vps.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Select("m_nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown machine = %v", err)
	}
	if err := m.Select(saved.ID); err != nil || m.Selected() != saved.ID {
		t.Fatalf("select = %v, selected %q", err, m.Selected())
	}
	if cur, _ := st.LoadCurrent(); cur != saved.ID {
		t.Errorf("the choice is not persisted: %q", cur)
	}
	// A restart stands where the window stood.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	again := New(Options{Store: st, Dialer: remote, Version: "0.0.4-dev"})
	if err := again.Start(ctx); err != nil || again.Selected() != saved.ID {
		t.Fatalf("after restart: %v, selected %q", err, again.Selected())
	}
	if err := m.Remove(saved.ID); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.LoadCurrent(); m.Selected() != "" || cur != "" {
		t.Errorf("removing the machine must put the window back on this computer: %q / %q", m.Selected(), cur)
	}
	if err := m.Select(""); err != nil {
		t.Errorf("this computer is always selectable: %v", err)
	}
}
