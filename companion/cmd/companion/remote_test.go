package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two real Companions, one real machine.
//
// The remote-machine feature is a control plane over ssh: the local Core
// probes a machine, starts the Companion there, forwards its loopback
// listeners here, proxies its control API to the desktop and its /mcp to
// the platform. Every piece has its own test against a fake; what none of
// them shows is the two binaries actually meeting. So this one builds the
// binary, stands in for ssh with a program that runs scripts and forwards
// ports on this same host, and then does what a user would do: add a
// folder on the "remote" machine, read a file in it through the local
// endpoint, and approve a write to it from the local control API.
//
// Only ssh is replaced. The remote Companion is the real one, started
// detached by the real start script under a HOME of its own.

func TestARemoteMachineIsReachedThroughTheLocalEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a POSIX shell script")
	}
	bin := buildCompanion(t)

	// The "remote" machine: a HOME with the Companion installed where the
	// probe looks for it.
	remoteHome := t.TempDir()
	remoteBin := filepath.Join(remoteHome, ".fylane", "bin")
	if err := os.MkdirAll(remoteBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(bin, filepath.Join(remoteBin, "fylane-companion")); err != nil {
		t.Fatal(err)
	}
	remoteProject := filepath.Join(remoteHome, "project")
	if err := os.MkdirAll(remoteProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteProject, "README.md"), []byte("hello from the vps\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killRemote(remoteHome) })

	// The stand-in ssh: the test binary itself, in a mode that runs the
	// script under the remote HOME or holds the forwards open.
	fakeBin := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nFYLANE_FAKE_SSH=1 FYLANE_FAKE_HOME=" + remoteHome + " exec \"" + self + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// The local Core, with the machine already configured.
	localDir := t.TempDir()
	cfg := `{"machines":[{"id":"m_vps","name":"vps","host":"vps.example","user":"deploy"}]}`
	if err := os.WriteFile(filepath.Join(localDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	local := exec.Command(bin, "serve", "-addr", "127.0.0.1:0", "-data-dir", localDir, "-log-level", "warn")
	local.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	local.Stdout, local.Stderr = os.Stderr, os.Stderr
	if err := local.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		local.Process.Signal(syscall.SIGTERM)
		local.Wait()
	})
	ctl := waitControl(t, localDir)

	// The link comes up on its own: probe, start, forward, verify.
	deadline := time.Now().Add(30 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		var doc struct {
			Machines []struct {
				State  string `json:"state"`
				Detail string `json:"detail"`
			} `json:"machines"`
		}
		ctlCall(t, ctl, "GET", "/v1/machines", nil, &doc)
		if len(doc.Machines) == 1 {
			state = doc.Machines[0].State + " " + doc.Machines[0].Detail
			if doc.Machines[0].State == "online" {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.HasPrefix(state, "online") {
		t.Fatalf("the machine never came online: %s", state)
	}

	// Grant a folder on it, through the proxy.
	var added struct {
		ID string `json:"id"`
	}
	ctlCall(t, ctl, "POST", "/v1/machines/m_vps/v1/workspaces/add", map[string]string{"path": remoteProject}, &added)
	if added.ID == "" {
		t.Fatal("the remote workspace was not added")
	}
	ctlCall(t, ctl, "POST", "/v1/machines/m_vps/v1/workspaces/select", map[string]string{"id": added.ID}, nil)

	// The platform sees one endpoint. workspace_info lists the remote folder
	// with the machine's name, and a read with its id goes there.
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://" + ctl.MCPAddr + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var info struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspace_id"`
			Name        string `json:"name"`
			Machine     string `json:"machine"`
		} `json:"workspaces"`
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "workspace_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	structuredInto(t, res, &info)
	found := false
	for _, w := range info.Workspaces {
		if w.WorkspaceID == added.ID {
			found = true
			if w.Machine != "vps" || w.Name != "project" {
				t.Errorf("remote entry = %+v", w)
			}
		}
	}
	if !found {
		t.Fatalf("workspace_info did not list the remote folder: %+v", info.Workspaces)
	}

	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "read_file",
		Arguments: map[string]any{"workspace_id": added.ID, "path": "README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Content string `json:"content"`
	}
	structuredInto(t, res, &read)
	if read.Content != "hello from the vps\n" {
		t.Fatalf("read through the local endpoint = %q (isError=%v)", read.Content, res.IsError)
	}

	// A write asks on the remote machine; the answer comes from the local
	// control API through the proxy, and the file lands on the remote disk.
	var pendingID string
	approved := make(chan struct{})
	go func() {
		defer close(approved)
		for time.Now().Before(time.Now().Add(20 * time.Second)) {
			var doc struct {
				Approvals []struct {
					ChangeSetID string `json:"change_set_id"`
				} `json:"approvals"`
			}
			ctlCall(t, ctl, "GET", "/v1/machines/m_vps/v1/approvals", nil, &doc)
			if len(doc.Approvals) == 1 {
				pendingID = doc.Approvals[0].ChangeSetID
				ctlCall(t, ctl, "POST", "/v1/machines/m_vps/v1/approvals/resolve",
					map[string]any{"change_set_id": pendingID, "approved": true}, nil)
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "write_file",
		Arguments: map[string]any{"workspace_id": added.ID, "path": "notes.txt", "content": "written from afar\n"}})
	if err != nil {
		t.Fatal(err)
	}
	<-approved
	var wrote struct {
		Status string `json:"status"`
	}
	structuredInto(t, res, &wrote)
	if wrote.Status != "applied" {
		t.Fatalf("write status = %q (pending was %q)", wrote.Status, pendingID)
	}
	got, err := os.ReadFile(filepath.Join(remoteProject, "notes.txt"))
	if err != nil || string(got) != "written from afar\n" {
		t.Fatalf("remote disk = %q, %v", got, err)
	}
	// And the local machine's own disk saw nothing of it.
	if _, err := os.Stat(filepath.Join(localDir, "notes.txt")); err == nil {
		t.Error("the write landed locally")
	}
}

type controlInfo struct {
	Addr    string `json:"addr"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	MCPAddr string `json:"mcp_addr"`
}

func waitControl(t *testing.T, dataDir string) controlInfo {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(dataDir, "control.json"))
		if err == nil {
			var c controlInfo
			if json.Unmarshal(raw, &c) == nil && c.Addr != "" && c.MCPAddr != "" {
				return c
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the local Core never wrote its control file")
	return controlInfo{}
}

func ctlCall(t *testing.T, c controlInfo, method, path string, body any, out any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, "http://"+c.Addr+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, raw)
		}
	}
}

func structuredInto(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}

// killRemote stops the detached remote Companion by the pid in its control
// file. It was started with nohup and outlives everything else on purpose.
func killRemote(home string) {
	raw, err := os.ReadFile(filepath.Join(home, ".fylane", "data", "control.json"))
	if err != nil {
		return
	}
	var c controlInfo
	if json.Unmarshal(raw, &c) != nil || c.PID == 0 {
		return
	}
	if p, err := os.FindProcess(c.PID); err == nil {
		p.Signal(syscall.SIGTERM)
	}
}

// fakeSSH is the stand-in. `ssh … sh -s` runs the script on stdin under the
// remote HOME; `ssh -N -L …` listens on each local port and copies bytes to
// the remote address until killed. Everything else ssh does — keys, host
// keys, the network — is exactly what this test is not about.
func fakeSSH(args []string) int {
	home := os.Getenv("FYLANE_FAKE_HOME")
	if len(args) > 0 && args[0] == "-N" {
		var wg sync.WaitGroup
		for i := 0; i < len(args); i++ {
			if args[i] != "-L" || i+1 >= len(args) {
				continue
			}
			spec := args[i+1] // 127.0.0.1:LP:127.0.0.1:RP
			parts := strings.SplitN(spec, ":", 3)
			if len(parts) != 3 {
				fmt.Fprintln(os.Stderr, "bad forward", spec)
				return 255
			}
			ln, err := net.Listen("tcp", parts[0]+":"+parts[1])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 255
			}
			target := parts[2]
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						up, err := net.Dial("tcp", target)
						if err != nil {
							return
						}
						defer up.Close()
						go io.Copy(up, c)
						io.Copy(c, up)
					}()
				}
			}()
		}
		wg.Wait()
		return 0
	}
	if len(args) >= 2 && args[len(args)-2] == "sh" && args[len(args)-1] == "-s" {
		cmd := exec.Command("sh", "-s")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "HOME="+home)
		if err := cmd.Run(); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "fake ssh: unexpected arguments", args)
	return 255
}

func TestMain(m *testing.M) {
	if os.Getenv("FYLANE_FAKE_SSH") == "1" {
		os.Exit(fakeSSH(os.Args[1:]))
	}
	os.Exit(m.Run())
}
