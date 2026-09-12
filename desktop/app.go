package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App bridges the frontend to the Core's local control API. It holds no
// state of its own: every call reads the control file and asks the Core, so
// a Core restart (new port/token) heals on the next call.
type App struct {
	ctx context.Context
	// dataDir is where the Core writes control.json; defaults to the same
	// directory the Core uses.
	dataDir string
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// FYLANE_DATA_DIR mirrors the Core's -data-dir flag for non-default
	// setups (and tests); the default matches the Core's default.
	if dir := os.Getenv("FYLANE_DATA_DIR"); dir != "" {
		a.dataDir = dir
	} else if base, err := os.UserConfigDir(); err == nil {
		a.dataDir = filepath.Join(base, "fylane")
	}
	// Before anything is shown: a Dock tile that appears and then vanishes
	// is the one thing a "hide the Dock icon" setting must not do.
	if dockSupported && a.readShellPrefs().DockHidden {
		applyDockHidden(true)
	}
	installDockReopen()
	// First-run: nobody has started the Core yet — the shell brings it up so
	// the user never faces a "core is not running" wall. The Core's
	// single-instance lock makes a concurrent start harmless.
	go a.StartCore()
}

// StartCore launches the Companion core when it is not already reachable.
// Bound to the frontend so the offline banner can offer a retry. Returns a
// short status string for display.
func (a *App) StartCore() (string, error) {
	if cf, err := a.client(); err == nil {
		req, err := http.NewRequest(http.MethodGet, "http://"+cf.Addr+"/v1/status", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+cf.Token)
			c := &http.Client{Timeout: 2 * time.Second}
			if resp, err := c.Do(req); err == nil {
				resp.Body.Close()
				return "already running", nil
			}
		}
	}
	bin, err := companionBinary()
	if err != nil {
		return "", fmt.Errorf("companion binary not found: install it next to this app or set FYLANE_COMPANION_BIN")
	}
	args := []string{"serve"}
	if os.Getenv("FYLANE_DATA_DIR") != "" {
		args = append(args, "-data-dir", a.dataDir)
	}
	// The Core says why it could not start, on its own stderr. Forwarding
	// that to ours puts it in a terminal a packaged app does not have, so a
	// copy is kept here — it is the difference between an offline banner that
	// explains itself and one that just sits there.
	tail := &tailBuffer{limit: 4 << 10}
	cmd := exec.Command(bin, args...)
	hideChildConsole(cmd)
	cmd.Stdout = os.Stderr
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting companion core: %w", err)
	}
	// Reap the child if it exits while the shell lives (e.g. it lost the
	// single-instance race); the Core otherwise outlives this window. The
	// channel is buffered so this goroutine finishes whether or not the
	// select below is still listening.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// A Core that is going to fail on a taken port fails at once. Watching
	// for that is not a readiness check — the shell polls for readiness — it
	// is how the immediate failures stop being silent.
	select {
	case err := <-done:
		return "", fmt.Errorf("the core stopped right after starting: %s", startFailure(err, tail.String()))
	case <-time.After(startWatch):
		return "starting", nil
	}
}

// startWatch is how long a freshly spawned Core is watched for an immediate
// exit. Long enough for a failed bind, short enough that a person holding
// down the retry button does not think the app hung.
const startWatch = 2 * time.Second

// startFailure turns what the Core left behind into one line. The last line
// of its stderr is the reason it wrote; the exit status is the fallback for a
// Core that died without saying anything.
func startFailure(exitErr error, stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	if exitErr != nil {
		return exitErr.Error()
	}
	return "it exited without an error"
}

// tailBuffer keeps the last limit bytes written to it. The tail rather than
// the head: a program explains itself on the way out.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// companionBinary locates the Core executable: explicit override, then next
// to the shell binary (packaged layout), then PATH.
func companionBinary() (string, error) {
	name := "fylane-companion"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	if p := os.Getenv("FYLANE_COMPANION_BIN"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return exec.LookPath(name)
}

type controlFile struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

func (a *App) client() (*controlFile, error) {
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "control.json"))
	if err != nil {
		return nil, fmt.Errorf("companion core is not running")
	}
	var cf controlFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return nil, fmt.Errorf("companion core control file is invalid")
	}
	return &cf, nil
}

// call performs one authenticated control-API request and returns the raw
// JSON response for the frontend to render. Reads and quick commands answer
// immediately, so they use the short deadline.
func (a *App) call(method, path string, body any) (string, error) {
	return a.callWithin(method, path, body, 5*time.Second)
}

// callWithin is call with an explicit deadline, for the two endpoints that
// legitimately block: a save waits on the approval service, and a rollback
// is bounded by the Core at ten seconds. Cutting either off early would
// report "not reachable" for work that is actually still in progress.
func (a *App) callWithin(method, path string, body any, timeout time.Duration) (string, error) {
	cf, err := a.client()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return "", err
		}
	}
	req, err := http.NewRequest(method, "http://"+cf.Addr+path, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cf.Token)
	httpClient := &http.Client{Timeout: timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("companion core is not reachable")
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("core error (%d): %s", resp.StatusCode, out.String())
	}
	return out.String(), nil
}

// CoreStatus returns the Core /v1/status document as JSON.
func (a *App) CoreStatus() (string, error) {
	return a.call("GET", "/v1/status", nil)
}

// CopyText puts text on the system clipboard (WKWebView's JS clipboard API
// is unreliable inside Wails).
func (a *App) CopyText(text string) error {
	return runtime.ClipboardSetText(a.ctx, text)
}

// RaiseWindow brings the window to the front. The frontend calls it when a
// new approval arrives — the platform is blocked on the user's decision, so
// the window must not stay buried.
func (a *App) RaiseWindow() {
	runtime.WindowShow(a.ctx)
}

// OpenWorkspaceDir reveals a granted folder in the platform's file manager.
// The path is one the user picked themselves and the Core already handed to
// this window; it is opened, never listed or read, and nothing about it
// leaves the machine.
func (a *App) OpenWorkspaceDir(path string) error {
	if path == "" {
		return errors.New("no folder")
	}
	// BrowserOpenURL hands the string to the OS opener, which treats a
	// directory as "reveal it" — the same thing the user would do by hand.
	runtime.BrowserOpenURL(a.ctx, "file://"+path)
	return nil
}

// PairClaims lists pending push-pairing prompts.
func (a *App) PairClaims() (string, error) {
	return a.call("GET", "/v1/pairclaims", nil)
}

// ResolvePairClaim records the local decision on a push-pairing prompt.
func (a *App) ResolvePairClaim(requestID string, approved bool) (string, error) {
	return a.call("POST", "/v1/pairclaims/resolve", map[string]any{
		"request_id": requestID, "approved": approved,
	})
}

// Save writes files into a workspace through the Core's save endpoint. It is
// the same 14-step transaction, sandbox and approval service as every other
// write — the shell has no write path of its own. Used by first run to send
// one real test file down the lane.
func (a *App) Save(requestJSON string) (string, error) {
	var body any
	if err := json.Unmarshal([]byte(requestJSON), &body); err != nil {
		return "", fmt.Errorf("invalid save payload: %w", err)
	}
	// A save in safe mode blocks until the write is approved locally; the
	// Core's own approval budget is the real bound.
	return a.callWithin("POST", "/v1/save", body, 5*time.Minute)
}

// Connect returns how this machine is published: the tunnel Fylane runs (if
// any), what it published, and which providers are installed.
func (a *App) Connect() (string, error) {
	return a.call("GET", "/v1/connect", nil)
}

// ApplyTunnel switches to another tunnel provider. The payload can carry a
// provider token; it goes straight to the Core, which stores it in the OS
// keychain — the shell keeps no copy.
func (a *App) ApplyTunnel(requestJSON string) (string, error) {
	var body any
	if err := json.Unmarshal([]byte(requestJSON), &body); err != nil {
		return "", fmt.Errorf("invalid tunnel payload: %w", err)
	}
	// Starting a tunnel means launching a program and waiting for it to name
	// an address; that is slower than a normal control call.
	return a.callWithin("POST", "/v1/connect", body, 60*time.Second)
}

// StartTunnelSetup runs a provider's own browser sign-in. It returns as soon
// as the command is up; the URL to open shows up on the next poll, because
// the user is about to spend minutes in a browser.
func (a *App) StartTunnelSetup(provider string) (string, error) {
	return a.call("POST", "/v1/connect/setup", map[string]string{"provider": provider})
}

// CancelTunnelSetup abandons a sign-in in progress.
func (a *App) CancelTunnelSetup() (string, error) {
	return a.call("POST", "/v1/connect/setup/cancel", nil)
}

// StartTunnelDownload fetches the pinned build of a provider's program. The
// window sends a provider name and nothing else: what gets downloaded, from
// where, and at which digest are the Core's to decide, and the panel
// that showed those three facts is where the user agreed to them.
//
// It returns as soon as the transfer is running; progress shows up on the next
// poll, because a binary over a slow link takes minutes.
func (a *App) StartTunnelDownload(provider string) (string, error) {
	return a.call("POST", "/v1/connect/download", map[string]string{"provider": provider})
}

// CancelTunnelDownload abandons a download in progress.
func (a *App) CancelTunnelDownload() (string, error) {
	return a.call("POST", "/v1/connect/download/cancel", nil)
}

// PairingCode mints a short code a platform's connect page will accept. It is
// the way in when the browser is not on this machine: the page's loopback
// claim cannot reach a Companion it is not sitting next to, and the typed code
// is the fallback it offers instead.
func (a *App) PairingCode() (string, error) {
	return a.call("POST", "/v1/pair", nil)
}

// ClearBackups removes the undo copies for a workspace. The write records
// stay; what goes is the ability to take those writes back, which is why the
// window asks twice before calling this.
func (a *App) ClearBackups(workspaceID string) (string, error) {
	return a.call("POST", "/v1/backups/clear?workspace_id="+url.QueryEscape(workspaceID), nil)
}

// SignOutTunnel forgets a provider's credential on this machine. A sign-in
// the user cannot undo from the same panel is a one-way door, not a setting.
func (a *App) SignOutTunnel(provider string) (string, error) {
	return a.call("POST", "/v1/connect/signout", map[string]string{"provider": provider})
}

// OpenURL hands a web address to the user's browser. Only https, and only
// from the shell: the page it opens is a vendor's sign-in, and a shell that
// would open anything asked of it is a way to launch arbitrary handlers.
func (a *App) OpenURL(url string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing to open a non-https address")
	}
	runtime.BrowserOpenURL(a.ctx, url)
	return nil
}

// Sources returns per-provider connection state for the Sources screen.
func (a *App) Sources() (string, error) {
	return a.call("GET", "/v1/sources", nil)
}

// Tasks returns running and recently finished command work.
func (a *App) Tasks() (string, error) {
	return a.call("GET", "/v1/tasks", nil)
}

// CancelTask stops a running task.
func (a *App) CancelTask(taskID string) (string, error) {
	return a.call("POST", "/v1/tasks/cancel", map[string]string{"task_id": taskID})
}

// CommandSettings returns the command approval rung and every workspace
// grant in force.
func (a *App) CommandSettings() (string, error) {
	return a.call("GET", "/v1/commands", nil)
}

// SetCommandRung changes the command approval rung. confirm is required for
// the open rung and the Core refuses without it, so the acknowledgement can
// never be skipped by calling this directly.
func (a *App) SetCommandRung(rung string, confirm bool) (string, error) {
	return a.call("POST", "/v1/commands/rung", map[string]any{"rung": rung, "confirm": confirm})
}

// RevokeCommandGrant withdraws one workspace's standing authorization. It
// takes effect on the next command and returns the settings the page redraws
// from, so the list can never disagree with what the Core now holds.
func (a *App) RevokeCommandGrant(workspaceID string) (string, error) {
	return a.call("POST", "/v1/commands/revoke", map[string]string{"workspace_id": workspaceID})
}

// SetWriteMode switches the file-write approval policy: "safe" asks before
// every change set, "balanced" lets a set through when every operation in it
// creates a new, non-sensitive file. It is a separate axis from the command
// rung because a write is transactional and reversible and a
// command is neither, so loosening one must not loosen the other.
func (a *App) SetWriteMode(mode string) (string, error) {
	return a.call("POST", "/v1/safety", map[string]string{"mode": mode})
}

// Settings reads the execution preferences shown on the settings page.
func (a *App) Settings() (string, error) {
	return a.call("GET", "/v1/settings", nil)
}

// SaveSettings applies the fields the page changed and returns the result.
// The body is a partial document: absent fields are left alone.
func (a *App) SaveSettings(patch string) (string, error) {
	var body any
	if err := json.Unmarshal([]byte(patch), &body); err != nil {
		return "", fmt.Errorf("invalid settings payload: %w", err)
	}
	return a.call("POST", "/v1/settings", body)
}

// ClearTasks forgets the finished task records and returns what is left. It
// touches neither the workspace nor the undo copies a write left behind.
func (a *App) ClearTasks() (string, error) {
	return a.call("POST", "/v1/tasks/clear", nil)
}

// PendingApprovals returns the pending approval list as JSON.
func (a *App) PendingApprovals() (string, error) {
	return a.call("GET", "/v1/approvals", nil)
}

// ResolveApproval records the local decision for a pending approval.
func (a *App) ResolveApproval(changeSetID string, approved bool) (string, error) {
	return a.call("POST", "/v1/approvals/resolve", map[string]any{
		"change_set_id": changeSetID, "approved": approved,
	})
}

// Workspaces returns the workspace list as JSON.
func (a *App) Workspaces() (string, error) {
	return a.call("GET", "/v1/workspaces", nil)
}

// AddWorkspace opens the native folder picker and registers the chosen
// directory as a workspace. An empty result means the user cancelled.
func (a *App) AddWorkspace() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose a workspace folder",
	})
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", nil
	}
	return a.call("POST", "/v1/workspaces/add", map[string]any{"path": dir})
}

// SelectWorkspace makes the workspace the current one.
func (a *App) SelectWorkspace(id string) (string, error) {
	return a.call("POST", "/v1/workspaces/select", map[string]any{"id": id})
}

// PauseWorkspace suspends all external access to the workspace.
func (a *App) PauseWorkspace(id string) (string, error) {
	return a.call("POST", "/v1/workspaces/pause", map[string]any{"id": id})
}

// SetWorkspaceNetwork records whether the programs started for one workspace
// may reach the network. It answers with the whole workspace list, because the
// effective answer is not the setting: on a machine that cannot deny anything
// the row comes back saying so.
func (a *App) SetWorkspaceNetwork(id string, allow bool) (string, error) {
	return a.call("POST", "/v1/workspaces/network", map[string]any{"id": id, "allow": allow})
}

// ResumeWorkspace restores external access to a paused workspace.
func (a *App) ResumeWorkspace(id string) (string, error) {
	return a.call("POST", "/v1/workspaces/resume", map[string]any{"id": id})
}

// ChangeSets returns recent change sets for a workspace as JSON.
func (a *App) ChangeSets(workspaceID string) (string, error) {
	path := "/v1/changesets"
	if workspaceID != "" {
		path += "?workspace_id=" + workspaceID
	}
	return a.call("GET", path, nil)
}

// AcceptChangeSet records the user's verdict on an applied change set. It
// authorizes nothing and blocks nothing; it is the difference between a
// change a human read and approved of and one whose undo window simply ran
// out.
func (a *App) AcceptChangeSet(workspaceID, changeSetID string) (string, error) {
	return a.call("POST", "/v1/changesets/accept", map[string]any{
		"workspace_id": workspaceID, "change_set_id": changeSetID,
	})
}

// RollbackChangeSet rolls one change set back. The confirm sheet in the UI
// is the local approval; the Core claims the engine's approval accordingly.
func (a *App) RollbackChangeSet(workspaceID, changeSetID string) (string, error) {
	return a.callWithin("POST", "/v1/changesets/rollback", map[string]any{
		"workspace_id": workspaceID, "change_set_id": changeSetID,
		"confirmed_in": "desktop",
	}, 30*time.Second)
}

// Machines lists the remote machines this Core reaches over ssh, with the
// state of each link.
func (a *App) Machines() (string, error) {
	return a.call("GET", "/v1/machines", nil)
}

// AddMachine records a remote machine and starts connecting to it. The
// payload is a name, a host, and optionally a login and port — never a key
// or a password; ssh's own configuration supplies those.
func (a *App) AddMachine(requestJSON string) (string, error) {
	var body any
	if err := json.Unmarshal([]byte(requestJSON), &body); err != nil {
		return "", fmt.Errorf("invalid machine payload: %w", err)
	}
	return a.call("POST", "/v1/machines/add", body)
}

// RemoveMachine forgets a machine. Nothing on that machine is touched.
func (a *App) RemoveMachine(id string) (string, error) {
	return a.call("POST", "/v1/machines/remove", map[string]string{"id": id})
}

// ConnectMachine (re)starts the link to a machine.
func (a *App) ConnectMachine(id string) (string, error) {
	return a.call("POST", "/v1/machines/connect", map[string]string{"id": id})
}

// DisconnectMachine closes the link and leaves the machine off.
func (a *App) DisconnectMachine(id string) (string, error) {
	return a.call("POST", "/v1/machines/disconnect", map[string]string{"id": id})
}

// InstallMachine asks the Core to install its own version of Fylane on a
// machine that was found without one. The window sends the machine id and
// nothing else: which version, from where, and checked how are the Core's.
func (a *App) InstallMachine(id string) (string, error) {
	return a.call("POST", "/v1/machines/install", map[string]string{"id": id})
}

// MachineCall performs one control-API request against a remote machine,
// through the local Core's per-machine proxy. The path is the remote's own
// control path (e.g. /v1/tasks); the local token gets the call into the
// Core, the Core's link to that machine carries it the rest of the way.
// A rollback through it is bounded the same way a local one is.
func (a *App) MachineCall(id, method, path, bodyJSON string) (string, error) {
	if id == "" || !strings.HasPrefix(path, "/v1/") {
		return "", errors.New("invalid machine call")
	}
	var body any
	if bodyJSON != "" {
		if err := json.Unmarshal([]byte(bodyJSON), &body); err != nil {
			return "", fmt.Errorf("invalid machine payload: %w", err)
		}
	}
	return a.callWithin(method, "/v1/machines/"+id+path, body, 30*time.Second)
}

// ProbeMachine asks what is at an address without saving it: the add sheet
// calls it as the user types, so the answer is on screen before the click.
func (a *App) ProbeMachine(requestJSON string) (string, error) {
	var body any
	if err := json.Unmarshal([]byte(requestJSON), &body); err != nil {
		return "", fmt.Errorf("invalid machine payload: %w", err)
	}
	// ssh's own connect timeout is ten seconds; the probe script is quick
	// once it is in.
	return a.callWithin("POST", "/v1/machines/probe", body, 30*time.Second)
}

// UpdateMachine replaces how a machine is reached and reconnects it.
func (a *App) UpdateMachine(requestJSON string) (string, error) {
	var body any
	if err := json.Unmarshal([]byte(requestJSON), &body); err != nil {
		return "", fmt.Errorf("invalid machine payload: %w", err)
	}
	return a.call("POST", "/v1/machines/update", body)
}
