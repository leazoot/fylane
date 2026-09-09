package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	cfg, err := ParseArgs([]string{"-workspace", "/tmp/x", "-data-dir", "/tmp/d", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if cfg.Workspace != "/tmp/x" || cfg.DataDir != "/tmp/d" || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Addr != "127.0.0.1:8787" || cfg.EnableProbes {
		t.Fatalf("defaults wrong: %+v", cfg)
	}

	if _, err := ParseArgs([]string{"-relay", "https://not-ws.example"}); err == nil {
		t.Fatal("non-websocket relay URL accepted")
	}
	// The unauthenticated local MCP listener must never leave loopback.
	for _, addr := range []string{"0.0.0.0:8787", "192.168.1.5:8787", ":8787"} {
		if _, err := ParseArgs([]string{"-addr", addr}); err == nil {
			t.Fatalf("non-loopback listen address %q accepted", addr)
		}
	}
	if _, err := ParseArgs([]string{"-addr", "localhost:8787"}); err != nil {
		t.Fatalf("localhost listen address rejected: %v", err)
	}
	// Plaintext tunnels are a loopback-only development affordance.
	if _, err := ParseArgs([]string{"-relay", "ws://relay.example/tunnel"}); err == nil {
		t.Fatal("plaintext ws:// to a remote relay accepted")
	}
	if _, err := ParseArgs([]string{"-relay", "ws://127.0.0.1:9000/tunnel"}); err != nil {
		t.Fatalf("loopback ws:// rejected: %v", err)
	}
	if _, err := ParseArgs([]string{"-relay", "wss://relay.example/tunnel"}); err != nil {
		t.Fatalf("wss:// rejected: %v", err)
	}
	if _, err := ParseArgs([]string{"-log-level", "loud"}); err == nil {
		t.Fatal("invalid log level accepted")
	}
	if _, err := ParseArgs([]string{"stray"}); err == nil {
		t.Fatal("stray positional argument accepted")
	}
}

// Direct mode publishes a listener to the internet, so what it accepts is a
// security boundary, not a convenience.
func TestParseArgsDirectMode(t *testing.T) {
	dataDir := t.TempDir()
	base := []string{"-data-dir", dataDir}

	cfg, err := ParseArgs(append(base, "-direct-addr", "127.0.0.1:8788",
		"-public-url", "https://x.trycloudflare.com"))
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if cfg.DirectAddr != "127.0.0.1:8788" || cfg.PublicURL != "https://x.trycloudflare.com" {
		t.Fatalf("cfg = %+v", cfg)
	}
	// No public URL yet is normal: a quick tunnel names the machine only once
	// it is running.
	if _, err := ParseArgs(append(base, "-direct-addr", "127.0.0.1:8788")); err != nil {
		t.Fatalf("direct mode without a public URL rejected: %v", err)
	}

	// The tunnel connects locally, so a wider bind only widens exposure.
	if _, err := ParseArgs(append(base, "-direct-addr", "0.0.0.0:8788")); err == nil {
		t.Fatal("non-loopback direct address accepted")
	}
	// Publishing the local MCP port would publish an unauthenticated /mcp.
	if _, err := ParseArgs(append(base, "-direct-addr", "127.0.0.1:8787")); err == nil {
		t.Fatal("direct address equal to the local MCP listener accepted")
	}
	// One machine, one way in.
	if _, err := ParseArgs(append(base, "-direct-addr", "127.0.0.1:8788",
		"-relay", "wss://relay.example/tunnel")); err == nil {
		t.Fatal("-relay together with -direct-addr accepted")
	}
	// OAuth codes and file traffic never travel in the clear.
	if _, err := ParseArgs(append(base, "-direct-addr", "127.0.0.1:8788",
		"-public-url", "http://public.example")); err == nil {
		t.Fatal("plaintext public URL accepted")
	}
	if _, err := ParseArgs(append(base, "-direct-addr", "127.0.0.1:8788",
		"-public-url", "http://127.0.0.1:8788")); err != nil {
		t.Fatalf("loopback http public URL rejected: %v", err)
	}
	if _, err := ParseArgs(append(base, "-public-url", "https://x.example")); err == nil {
		t.Fatal("-public-url without -direct-addr accepted")
	}
}

// The direct-mode choice persists, so an auto-started Core keeps serving what
// the user set up; an explicit -relay overrides it rather than colliding.
func TestPersistedDirectMode(t *testing.T) {
	dataDir := t.TempDir()
	if err := SaveDirect(dataDir, "127.0.0.1:8788", "https://x.trycloudflare.com"); err != nil {
		t.Fatalf("SaveDirect: %v", err)
	}
	cfg, err := ParseArgs([]string{"-data-dir", dataDir})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if cfg.DirectAddr != "127.0.0.1:8788" || cfg.PublicURL != "https://x.trycloudflare.com" {
		t.Fatalf("stored direct mode not applied: %+v", cfg)
	}
	cfg, err = ParseArgs([]string{"-data-dir", dataDir, "-relay", "wss://relay.example/tunnel"})
	if err != nil {
		t.Fatalf("ParseArgs with -relay: %v", err)
	}
	if cfg.DirectAddr != "" {
		t.Fatalf("-relay did not supersede the stored direct mode: %+v", cfg)
	}
	if err := SaveDirect(dataDir, "0.0.0.0:8788", ""); err == nil {
		t.Fatal("SaveDirect accepted a non-loopback address")
	}
}

func TestInstanceLock(t *testing.T) {
	dir := t.TempDir()
	l1, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireInstanceLock(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire: err = %v, want ErrAlreadyRunning", err)
	}
	if err := l1.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	l2, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	l2.release()
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The relay endpoint persisted by `pair` applies when serve has no -relay
// flag; an explicit flag always wins; a tampered stored value fails loudly.
func TestPersistedRelayURL(t *testing.T) {
	dataDir := t.TempDir()
	if err := SaveRelayURL(dataDir, "wss://relay.example/tunnel"); err != nil {
		t.Fatalf("SaveRelayURL: %v", err)
	}

	cfg, err := ParseArgs([]string{"-data-dir", dataDir})
	if err != nil {
		t.Fatalf("ParseArgs with stored relay: %v", err)
	}
	if cfg.RelayURL != "wss://relay.example/tunnel" {
		t.Fatalf("RelayURL = %q, want stored value", cfg.RelayURL)
	}

	cfg, err = ParseArgs([]string{"-data-dir", dataDir, "-relay", "wss://other.example/tunnel"})
	if err != nil {
		t.Fatalf("ParseArgs with flag: %v", err)
	}
	if cfg.RelayURL != "wss://other.example/tunnel" {
		t.Fatalf("RelayURL = %q, want flag to win", cfg.RelayURL)
	}

	if err := SaveRelayURL(dataDir, "ws://relay.example/tunnel"); err == nil {
		t.Fatal("SaveRelayURL accepted plaintext ws:// for a remote relay")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"),
		[]byte(`{"relay_url":"ws://evil.example/tunnel"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseArgs([]string{"-data-dir", dataDir}); err == nil {
		t.Fatal("ParseArgs accepted a tampered plaintext relay setting")
	}
}

func TestTunnelURLFromBase(t *testing.T) {
	cases := []struct {
		base, want string
	}{
		{"https://relay.example", "wss://relay.example/tunnel"},
		{"https://relay.example/", "wss://relay.example/tunnel"},
		{"https://relay.example/fy", "wss://relay.example/fy/tunnel"},
		{"http://127.0.0.1:9000", "ws://127.0.0.1:9000/tunnel"},
		{"http://remote.example", ""},
		{"ftp://relay.example", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, err := TunnelURLFromBase(c.base)
		if c.want == "" {
			if err == nil {
				t.Errorf("TunnelURLFromBase(%q) accepted, got %q", c.base, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("TunnelURLFromBase(%q) = %q, %v; want %q", c.base, got, err, c.want)
		}
		// The inverse recovers the base URL (sans trailing slash).
		back, err := BaseURLFromTunnel(got)
		if wantBack := strings.TrimSuffix(c.base, "/"); err != nil || back != wantBack {
			t.Errorf("BaseURLFromTunnel(%q) = %q, %v; want %q", got, back, err, wantBack)
		}
	}
	if _, err := BaseURLFromTunnel("https://not-a-tunnel.example"); err == nil {
		t.Error("BaseURLFromTunnel accepted a non-ws URL")
	}
}

type testControl struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

func readControlFile(t *testing.T, dataDir string) *testControl {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "control.json"))
	if err != nil {
		t.Fatalf("reading control file: %v", err)
	}
	var cf testControl
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing control file: %v", err)
	}
	return &cf
}

func ctlDo(t *testing.T, cf *testControl, method, path, body string) string {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+cf.Addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cf.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s -> %d: %s", method, path, resp.StatusCode, out)
	}
	return string(out)
}

func ctlGet(t *testing.T, cf *testControl, path string) string {
	return ctlDo(t, cf, http.MethodGet, path, "")
}

func ctlPost(t *testing.T, cf *testControl, path, body string) string {
	return ctlDo(t, cf, http.MethodPost, path, body)
}

// End-to-end smoke test: the daemon starts on a database-backed workspace,
// answers HTTP on /mcp, and shuts down cleanly on context cancellation.
func TestAppRunAndShutdown(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	cfg := &Config{DataDir: dataDir, Addr: "127.0.0.1:0", Workspace: root}
	a := New(cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	var addr string
	select {
	case addr = <-a.ListenAddr():
	case err := <-done:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for listener")
	}

	// A GET probe on the MCP endpoint must answer (405 per Streamable HTTP
	// without SSE support, or 200 — anything but a connection error).
	resp, err := http.Get(fmt.Sprintf("http://%s/mcp", addr))
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for shutdown")
	}
}

// Direct mode brings up a second listener — the one a tunnel publishes. The
// two must not be confused: the local one answers /mcp to anybody on this
// machine, the published one demands a platform access token.
func TestAppRunsTheDirectSurface(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	directAddr := probe.Addr().String()
	probe.Close()

	cfg := &Config{
		DataDir: t.TempDir(), Addr: "127.0.0.1:0", Workspace: t.TempDir(),
		DirectAddr: directAddr, PublicURL: "https://example.trycloudflare.com",
	}
	a := New(cfg, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	var localAddr string
	select {
	case localAddr = <-a.ListenAddr():
	case err := <-done:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for listener")
	}

	deadline := time.Now().Add(10 * time.Second)
	var resp *http.Response
	for {
		resp, err = http.Get("http://" + directAddr + "/.well-known/oauth-authorization-server")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("discovery on the direct surface: %v", err)
	}
	var doc map[string]any
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	if doc["issuer"] != cfg.PublicURL {
		t.Fatalf("issuer = %v; want %s", doc["issuer"], cfg.PublicURL)
	}

	resp, err = http.Post("http://"+directAddr+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /mcp on the direct surface: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("published /mcp without a token = %d; want 401", resp.StatusCode)
	}
	// The local listener stays as it was: loopback-only and unauthenticated.
	resp, err = http.Get("http://" + localAddr + "/mcp")
	if err != nil {
		t.Fatalf("GET local /mcp: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("the local listener now demands a token; direct mode leaked into it")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for shutdown")
	}
}

// Two daemons on one data directory must be impossible.
func TestAppSingleInstance(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	first := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:0", Workspace: root}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- first.Run(ctx) }()
	select {
	case <-first.ListenAddr():
	case err := <-done:
		t.Fatalf("first Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for first instance")
	}

	second := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:0", Workspace: root}, testLogger())
	if err := second.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run: err = %v, want ErrAlreadyRunning", err)
	}
}

// Without -workspace and without a stored selection the daemon still starts
// (first run, W13): the control API is up so the desktop app can register
// the first folder, and workspaces/add works immediately.
func TestAppStartsWithNoWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:0"}, testLogger())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-a.ListenAddr():
	case err := <-done:
		t.Fatalf("Run without workspace exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for first-run instance")
	}

	cf := readControlFile(t, dataDir)
	list := ctlGet(t, cf, "/v1/workspaces")
	if !strings.Contains(list, `"workspaces":[]`) && !strings.Contains(list, `"workspaces":null`) {
		t.Fatalf("first-run workspaces = %s, want empty", list)
	}
	root := t.TempDir()
	added := ctlPost(t, cf, "/v1/workspaces/add", `{"path":`+strconv.Quote(root)+`}`)
	if !strings.Contains(added, `"id":"ws_`) {
		t.Fatalf("workspaces/add on first run = %s", added)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// The stored current workspace must be reused on restart without -workspace.
func TestAppReusesStoredCurrentWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()

	ctx1, cancel1 := context.WithCancel(context.Background())
	first := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:0", Workspace: root}, testLogger())
	done1 := make(chan error, 1)
	go func() { done1 <- first.Run(ctx1) }()
	select {
	case <-first.ListenAddr():
	case err := <-done1:
		t.Fatalf("first Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for first run")
	}
	cancel1()
	if err := <-done1; err != nil {
		t.Fatalf("first shutdown: %v", err)
	}

	// Restart without -workspace: stored selection carries over.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	second := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:0"}, testLogger())
	done2 := make(chan error, 1)
	go func() { done2 <- second.Run(ctx2) }()
	select {
	case <-second.ListenAddr():
	case err := <-done2:
		t.Fatalf("second Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for second run")
	}
	cancel2()
	if err := <-done2; err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}
