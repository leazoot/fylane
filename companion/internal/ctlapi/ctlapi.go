// Package ctlapi is the local control API the desktop UI shell uses to talk
// to the resident Core process (the Core owns tunnel/MCP/
// approvals; the UI is a thin, restartable shell).
//
// Transport: HTTP on 127.0.0.1 with a random bearer token. The token and
// port are written to <data-dir>/control.json with 0600 permissions, so only
// the same OS user can connect — the Core accepts no unauthenticated local
// callers. Loopback-only listener; no remote exposure.
package ctlapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/pairclaim"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/update"
	"github.com/leazoot/fylane/companion/internal/workspace"
	"github.com/leazoot/fylane/shared/buildinfo"
)

// controlFileName is written into the data directory for the UI to find the
// API. It contains no secrets other than the session-scoped token.
const controlFileName = "control.json"

// controlFile is the on-disk rendezvous document.
type controlFile struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
	// MCPAddr is the loopback MCP listener. Another Companion reaching this
	// one over ssh reads it here instead of guessing a port.
	MCPAddr string `json:"mcp_addr,omitempty"`
}

// TunnelStatus reports whether the outbound relay connection is up.
// Implemented by *tunnel.Client; defined here so the server depends only on
// what it consumes.
type TunnelStatus interface {
	Connected() bool
}

// UpdateStatus exposes the last notice-only update check.
// Implemented by *update.Checker.
type UpdateStatus interface {
	Last() update.Status
}

// Server is the local control API. Manager, Store, and Approvals are
// required; Engine enables the rollback endpoint and Tunnel/RelayURL enrich
// the status document.
type Server struct {
	Manager   *workspace.Manager
	Store     *store.Store
	Approvals *approval.Service
	Engine    *txn.Engine
	Tunnel    TunnelStatus
	RelayURL  string
	// Updates, when set, contributes the notice-only update check outcome
	// to the status document.
	Updates UpdateStatus
	// Pairing, when set, mints a platform pairing code for this device (the
	// desktop UI's connect flow); nil means no relay is configured.
	Pairing func(ctx context.Context) (code string, expiresIn time.Duration, err error)
	// ConnectorURL returns the public MCP endpoint platforms connect to,
	// shown alongside the pairing code. It is a function because in direct
	// mode the tunnel can rename this machine while the daemon runs.
	ConnectorURL func() string
	// Connect, when set, backs the connect screen: which tunnel is running,
	// what it published, and switching to another one (direct mode only).
	Connect ConnectControl
	// PersistApprovalMode, when set, stores the approval mode chosen on the
	// Safety page so restarts keep it.
	PersistApprovalMode func(mode string) error
	// PairClaims, when set, exposes pending push-pairing prompts.
	PairClaims *pairclaim.Service
	// Rules reads and writes the route rules shown on the Route rules
	// screen; nil disables the endpoint.
	Rules RuleStore
	// BackupRoot is the local undo-copy area; clearing the trace removes
	// the copies the deleted change sets referenced.
	BackupRoot string
	// Commands is the command approval rung and its workspace grants; nil
	// disables the command endpoints (and the tools, upstream).
	Commands CommandGate
	// Proxies reports the locally configured MCP gateway providers so the
	// settings page can show them; nil simply means none are listed. The
	// desktop cannot change them from here — this Core has no endpoint that
	// would, because a provider is a program to run and naming one is not
	// something any remote surface should be able to do.
	Proxies ProxyLister

	// LanguageServers reports the language servers code_navigate may start,
	// for the same reason Proxies is here: a program this machine will start
	// on a caller's behalf is something the local user should be able to see
	// listed, not only find in a startup log.
	LanguageServers LanguageServerLister

	// ReadBox is the running boundary. The control API reads it to say what
	// one workspace's programs actually get; it never changes it.
	ReadBox *readbox.Box
	// Tasks backs the desktop task view.
	Tasks TaskLister
	// Prefs backs the settings page's execution preferences (task timeout,
	// stop button, start at login); nil disables those endpoints.
	Prefs PrefStore
	// Machines backs the remote machine list and the per-machine proxy;
	// nil disables those endpoints.
	Machines MachineControl
	// MCPAddr is the bound loopback MCP listener, recorded in the control
	// file for a Companion that drives this one over ssh.
	MCPAddr string

	token string
}

func (s *Server) connectorURL() string {
	if s.ConnectorURL == nil {
		return ""
	}
	return s.ConnectorURL()
}

// ConnectControl is the connect screen's view of how this machine is
// published: which tunnel provider is configured, what state it is in, and
// the switch to another one. Implemented by the app package, which owns the
// tunnel process.
//
// Reading it works in both modes — "how does a platform reach this machine"
// has an answer either way. Only starting and stopping a tunnel is direct
// mode's business.
type ConnectControl interface {
	ConnectSnapshot() ConnectDoc
	ApplyTunnel(ctx context.Context, req TunnelRequest) error
	StopTunnel() error
	// StartSetup runs a provider's own browser sign-in. It returns once the
	// command is running; the URL to open arrives on a later snapshot,
	// because the user is about to spend minutes in a browser.
	StartSetup(ctx context.Context, provider string) error
	// CancelSetup abandons a sign-in in progress.
	CancelSetup()
	// StartDownload fetches the pinned build of a provider's program onto
	// this machine. It carries the user's consent for this one
	// download and nothing else: the provider is the whole request, and what
	// gets fetched is decided by the pin table inside the Core.
	StartDownload(ctx context.Context, provider string) error
	// CancelDownload abandons a download in progress.
	CancelDownload()
	// SignOut forgets a provider's credential on this machine.
	SignOut(provider string) error
}

// ErrNotDirect is returned by ApplyTunnel and StopTunnel when the companion
// reaches its platforms through a relay: there is no local tunnel to run.
var ErrNotDirect = errors.New("this companion is not in direct mode")

// ConnectDoc describes the current public entry point.
type ConnectDoc struct {
	// Mode is "direct" (this machine is the server) or "relay".
	Mode         string `json:"mode"`
	Provider     string `json:"provider,omitempty"`
	State        string `json:"state"`
	Detail       string `json:"detail,omitempty"`
	PublicURL    string `json:"public_url,omitempty"`
	ConnectorURL string `json:"connector_url,omitempty"`
	// RelayURL is the relay this machine paired with, in either mode: it is
	// what the screen offers as the way back. Empty means never paired.
	RelayURL string `json:"relay_url,omitempty"`
	// Restarting reports that a mode switch was stored and this process is
	// about to exit, so the caller waits for the Core instead of the answer.
	Restarting bool              `json:"restarting,omitempty"`
	Providers  []ConnectProvider `json:"providers"`
	// Setup is the browser sign-in currently running, if any.
	Setup *ConnectSetup `json:"setup,omitempty"`
	// Download is the program fetch currently running or last finished.
	Download *ConnectDownload `json:"download,omitempty"`
	// Platform is this machine's target as Go names it ("darwin/arm64"). The
	// screen needs it to say which platform a build has no pin for; without
	// it "we have nothing for you" reads as a fault rather than a gap.
	Platform string `json:"platform,omitempty"`
}

// ConnectProvider is one tunnel option, with what the user must supply and
// whether its program is installed.
type ConnectProvider struct {
	Kind          string `json:"kind"`
	Binary        string `json:"binary"`
	Install       string `json:"install"`
	Installed     bool   `json:"installed"`
	NeedsToken    bool   `json:"needs_token"`
	NeedsHostname bool   `json:"needs_hostname"`
	Stable        bool   `json:"stable"`
	// Setup is "none", "browser" or "token": how this provider is authorized.
	Setup string `json:"setup"`
	// Download is the vendor's page, offered when Installed is false. It is
	// the whole answer on a platform this build has no pin for, and stays the
	// second answer everywhere else — a pinned fetch became possible, but it
	// did not make installing it yourself the lesser path.
	Download string `json:"download,omitempty"`
	// Offer is the pinned build Fylane could fetch instead, present only when
	// the program is missing and this build has a pin for this platform.
	// Absent means there is nothing to offer, which the screen must say
	// rather than showing an offer it cannot honour.
	Offer *ConnectOffer `json:"offer,omitempty"`
	// Credential is where a "token" provider issues its token.
	Credential string `json:"credential,omitempty"`
	// Authorized reports that the credential is already on this machine.
	Authorized bool `json:"authorized"`
	// Checkable is false when Authorized is the benefit of the doubt rather
	// than a read of local state — the screen must not claim it is signed in.
	Checkable bool `json:"checkable"`
	// CanSignOut reports whether Fylane can undo the sign-in. False where the
	// credential is not Fylane's to remove.
	CanSignOut bool `json:"can_sign_out"`
	// SuggestedHostname is the address to offer, worked out from the sign-in
	// rather than asked for again. Empty when it is not known.
	SuggestedHostname string `json:"suggested_hostname,omitempty"`
	// OpensBrowser reports that the sign-in command opens the browser itself,
	// so the window must not open it a second time.
	OpensBrowser bool `json:"opens_browser"`
}

// ConnectSetup is the browser authorization in progress, if any.
type ConnectSetup struct {
	Provider string `json:"provider,omitempty"`
	Phase    string `json:"phase"`
	// URL is the page to approve on. The desktop opens it and also shows it,
	// so a user whose browser did not open can still get there.
	URL    string `json:"url,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ConnectOffer is what would be downloaded, shown before the user is asked.
// These are the same three facts the download is bound by, which is the point
// of showing them: the consent is informed by what actually constrains it.
type ConnectOffer struct {
	Binary  string `json:"binary"`
	Version string `json:"version"`
	// Source is the publisher's release address, without the version or file
	// name — those are already on their own lines.
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
}

// ConnectDownload is the program fetch in progress or last finished.
type ConnectDownload struct {
	Provider string `json:"provider,omitempty"`
	// Phase is "running", "ready" or "failed".
	Phase  string `json:"phase"`
	Detail string `json:"detail,omitempty"`
}

// TunnelRequest switches to a provider. Token is a credential: it goes to the
// OS keychain and is never echoed back or written to the config file.
type TunnelRequest struct {
	// Mode "relay" hands publishing back to the paired relay and ignores the
	// fields below. Empty means direct mode, published by Provider.
	Mode     string `json:"mode,omitempty"`
	Provider string `json:"provider"`
	Hostname string `json:"hostname,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Token    string `json:"token,omitempty"`
}

func (s *Server) handleConnect(w http.ResponseWriter, _ *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, s.Connect.ConnectSnapshot())
}

func (s *Server) handleConnectApply(w http.ResponseWriter, r *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	var req TunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.Connect.ApplyTunnel(r.Context(), req); err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, s.Connect.ConnectSnapshot())
}

func (s *Server) handleConnectStop(w http.ResponseWriter, _ *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	if err := s.Connect.StopTunnel(); err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, s.Connect.ConnectSnapshot())
}

// handleConnectSetup starts a provider's browser sign-in. It answers with the
// snapshot immediately: the sign-in itself happens in the user's browser and
// is reported by the regular poll, not by holding this request open.
func (s *Server) handleConnectSetup(w http.ResponseWriter, r *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Not r.Context(): that is cancelled when this response is written, and
	// the sign-in has to outlive it by however long the browser takes.
	if err := s.Connect.StartSetup(context.WithoutCancel(r.Context()), req.Provider); err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, s.Connect.ConnectSnapshot())
}

// handleConnectDownload fetches a provider's program. Reaching this handler is
// the consent a download requires: the desktop shows what would arrive — program,
// source and digest, from the same pin the download is checked against — and
// only a press gets here. The request carries the provider and nothing else,
// so no caller can name what to fetch.
func (s *Server) handleConnectDownload(w http.ResponseWriter, r *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Not r.Context(): that is cancelled when this response is written, and a
	// tunnel binary over a slow link is a legitimate several-minute transfer.
	if err := s.Connect.StartDownload(context.WithoutCancel(r.Context()), req.Provider); err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, s.Connect.ConnectSnapshot())
}

func (s *Server) handleConnectDownloadCancel(w http.ResponseWriter, _ *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	s.Connect.CancelDownload()
	writeJSON(w, s.Connect.ConnectSnapshot())
}

// handleConnectSignOut forgets one provider's credential. There is a sign-in,
// so there is a sign-out: an authorization the user cannot undo from the same
// panel that granted it is not a setting, it is a one-way door.
func (s *Server) handleConnectSignOut(w http.ResponseWriter, r *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.Connect.SignOut(req.Provider); err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, s.Connect.ConnectSnapshot())
}

func (s *Server) handleConnectSetupCancel(w http.ResponseWriter, _ *http.Request) {
	if s.Connect == nil {
		http.Error(w, ErrNotDirect.Error(), http.StatusConflict)
		return
	}
	s.Connect.CancelSetup()
	writeJSON(w, s.Connect.ConnectSnapshot())
}

func writeConnectError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotDirect) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// RuleStore is the settings-backed route rule list. Defined here, at the
// consumer, so ctlapi does not depend on the concrete storage.
type RuleStore interface {
	Load() ([]routerule.Rule, error)
	Save([]routerule.Rule) error
}

// Start listens on a loopback port, writes the control file into dataDir,
// and serves until ctx is cancelled. It returns the bound address.
func (s *Server) Start(ctx context.Context, dataDir string) (string, error) {
	if s.Manager == nil || s.Store == nil || s.Approvals == nil {
		return "", fmt.Errorf("control api is not fully configured")
	}
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating control token: %w", err)
	}
	s.token = hex.EncodeToString(buf[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listening for control api: %w", err)
	}

	doc, err := json.Marshal(controlFile{Addr: ln.Addr().String(), Token: s.token, PID: os.Getpid(), MCPAddr: s.MCPAddr})
	if err != nil {
		ln.Close()
		return "", err
	}
	path := filepath.Join(dataDir, controlFileName)
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		ln.Close()
		return "", fmt.Errorf("writing control file: %w", err)
	}
	// WriteFile keeps the mode of a pre-existing file; the token must be
	// owner-readable only even if something planted a lax file first.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return "", fmt.Errorf("securing control file: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.HandleFunc("POST /v1/safety", s.handleSafety)
	mux.HandleFunc("GET /v1/pairclaims", s.handlePairClaims)
	mux.HandleFunc("POST /v1/pairclaims/resolve", s.handlePairClaimResolve)
	mux.HandleFunc("GET /v1/approvals", s.handleApprovals)
	mux.HandleFunc("POST /v1/approvals/resolve", s.handleResolve)
	mux.HandleFunc("GET /v1/workspaces", s.handleWorkspaces)
	mux.HandleFunc("POST /v1/workspaces/add", s.handleWorkspaceAdd)
	mux.HandleFunc("POST /v1/workspaces/select", s.workspaceAction(s.Manager.SetCurrent))
	mux.HandleFunc("POST /v1/workspaces/pause", s.workspaceAction(s.Manager.Pause))
	mux.HandleFunc("POST /v1/workspaces/resume", s.workspaceAction(s.Manager.Resume))
	mux.HandleFunc("POST /v1/workspaces/revoke", s.workspaceAction(s.Manager.Revoke))
	mux.HandleFunc("POST /v1/workspaces/network", s.handleWorkspaceNetwork)
	mux.HandleFunc("GET /v1/changesets", s.handleChangeSets)
	mux.HandleFunc("POST /v1/changesets/rollback", s.handleRollback)
	mux.HandleFunc("POST /v1/changesets/accept", s.handleAccept)
	mux.HandleFunc("GET /v1/commands", s.handleCommands)
	mux.HandleFunc("POST /v1/commands/rung", s.handleCommandRung)
	mux.HandleFunc("POST /v1/commands/revoke", s.handleCommandRevoke)
	mux.HandleFunc("GET /v1/tasks", s.handleTasks)
	mux.HandleFunc("POST /v1/tasks/cancel", s.handleTaskCancel)
	mux.HandleFunc("POST /v1/tasks/clear", s.handleTaskClear)
	mux.HandleFunc("GET /v1/settings", s.handlePrefs)
	mux.HandleFunc("POST /v1/settings", s.handlePrefsSave)
	mux.HandleFunc("GET /v1/rules", s.handleRules)
	mux.HandleFunc("POST /v1/rules", s.handleRulesSave)
	mux.HandleFunc("GET /v1/sources", s.handleSources)
	mux.HandleFunc("GET /v1/connect", s.handleConnect)
	mux.HandleFunc("POST /v1/connect", s.handleConnectApply)
	mux.HandleFunc("POST /v1/connect/stop", s.handleConnectStop)
	mux.HandleFunc("POST /v1/connect/setup", s.handleConnectSetup)
	mux.HandleFunc("POST /v1/connect/setup/cancel", s.handleConnectSetupCancel)
	mux.HandleFunc("POST /v1/connect/download", s.handleConnectDownload)
	mux.HandleFunc("POST /v1/connect/download/cancel", s.handleConnectDownloadCancel)
	mux.HandleFunc("POST /v1/connect/signout", s.handleConnectSignOut)
	mux.HandleFunc("POST /v1/trace/clear", s.handleTraceClear)
	mux.HandleFunc("POST /v1/backups/clear", s.handleBackupsClear)
	mux.HandleFunc("POST /v1/save", s.handleSave)
	mux.HandleFunc("GET /v1/machines", s.handleMachines)
	mux.HandleFunc("POST /v1/machines/add", s.handleMachineAdd)
	mux.HandleFunc("POST /v1/machines/remove", s.machineAction(func(c MachineControl, id string) error { return c.Remove(id) }))
	mux.HandleFunc("POST /v1/machines/connect", s.machineAction(func(c MachineControl, id string) error { return c.Connect(id) }))
	mux.HandleFunc("POST /v1/machines/disconnect", s.machineAction(func(c MachineControl, id string) error { return c.Disconnect(id) }))
	mux.HandleFunc("POST /v1/machines/install", s.machineAction(func(c MachineControl, id string) error { return c.Install(id) }))
	mux.HandleFunc("/v1/machines/{id}/{rest...}", s.handleMachineProxy)

	srv := &http.Server{Handler: s.auth(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		os.Remove(path)
	}()
	go srv.Serve(ln)
	return ln.Addr().String(), nil
}

// auth enforces the bearer token on every request, in constant time.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.token
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// handleSafety switches the approval policy (the desktop Safety page). Only
// the two defined modes exist; there is no always-allow.
func (s *Server) handleSafety(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.Approvals.SetMode(req.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.PersistApprovalMode != nil {
		if err := s.PersistApprovalMode(req.Mode); err != nil {
			// The switch is live; losing persistence only affects restarts.
			http.Error(w, "mode active but not persisted: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]string{"approval_mode": req.Mode})
}

// handlePairClaims lists push-pairing prompts awaiting a decision.
func (s *Server) handlePairClaims(w http.ResponseWriter, _ *http.Request) {
	claims := []pairclaim.Claim{}
	if s.PairClaims != nil {
		claims = s.PairClaims.Pending()
	}
	writeJSON(w, map[string]any{"claims": claims})
}

// handlePairClaimResolve records the local decision on a push-pairing
// prompt. Approval binds this device on the relay.
func (s *Server) handlePairClaimResolve(w http.ResponseWriter, r *http.Request) {
	if s.PairClaims == nil {
		http.Error(w, "push pairing is not available", http.StatusConflict)
		return
	}
	var req struct {
		RequestID string `json:"request_id"`
		Approved  bool   `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !s.PairClaims.Resolve(r.Context(), req.RequestID, req.Approved) {
		http.Error(w, "unknown or already decided", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePair mints a pairing code for the platform OAuth flow (the desktop
// connect sheet). Codes are short-lived; the UI refreshes on expiry.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if s.Pairing == nil {
		http.Error(w, "no relay configured; connect requires a relay", http.StatusConflict)
		return
	}
	code, expiresIn, err := s.Pairing(r.Context())
	if err != nil {
		http.Error(w, "pairing code: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"code":               code,
		"expires_in_seconds": int(expiresIn.Seconds()),
		"connector_url":      s.connectorURL(),
	})
}

type statusResponse struct {
	Version          string `json:"version"`
	PendingApprovals int    `json:"pending_approvals"`
	// Tunnel is "connected", "offline", or "disabled" (no relay configured).
	Tunnel          string `json:"tunnel"`
	RelayURL        string `json:"relay_url,omitempty"`
	ConnectorURL    string `json:"connector_url,omitempty"`
	ApprovalMode    string `json:"approval_mode"`
	CommandRung     string `json:"command_rung,omitempty"`
	RunningTasks    int    `json:"running_tasks,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available,omitempty"`
	// DownloadPage is the release page a notice links to. Absent until the
	// public domain exists, and a consumer hides the link while it is —
	// which is the contract update.DownloadPage was written to.
	DownloadPage string `json:"download_page,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	tun := "disabled"
	if s.Tunnel != nil {
		if s.Tunnel.Connected() {
			tun = "connected"
		} else {
			tun = "offline"
		}
	}
	resp := statusResponse{
		Version:          buildinfo.Version,
		PendingApprovals: len(s.Approvals.Pending()),
		Tunnel:           tun,
		RelayURL:         s.RelayURL,
		ConnectorURL:     s.connectorURL(),
		ApprovalMode:     s.Approvals.Mode(),
	}
	if s.Commands != nil {
		resp.CommandRung = string(s.Commands.Rung())
	}
	if s.Tasks != nil {
		for _, t := range s.Tasks.List() {
			if !t.State.Terminal() {
				resp.RunningTasks++
			}
		}
	}
	if s.Updates != nil {
		last := s.Updates.Last()
		resp.LatestVersion = last.Latest
		resp.UpdateAvailable = last.Available
		if last.Available {
			resp.DownloadPage = update.DownloadPage
		}
	}
	writeJSON(w, resp)
}

// pendingApproval is one prompt as the desktop renders it. It carries more
// than the change set did because a command prompt has no diff to show: the
// argv, the rule that objected and that rule's reason are the whole basis for
// the decision, and for a while none of the three reached the screen. Like
// root_path above, they stay on this loopback, token-authenticated surface —
// the argv is never echoed back over MCP or Relay.
type pendingApproval struct {
	ChangeSetID   string    `json:"change_set_id"`
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	Provider      string    `json:"provider"`
	Summary       string    `json:"summary"`
	CreatedAt     time.Time `json:"created_at"`
	Operations    any       `json:"operations"`
	// Kind selects which question the prompt asks; see txn.Kind* .
	Kind    string   `json:"kind"`
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Rule    string   `json:"rule,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	// Grant marks the one-time question that authorizes the whole workspace
	// rather than this one command.
	Grant bool `json:"grant,omitempty"`
	// MustAsk marks a stop the user's own route rule asked for.
	MustAsk bool `json:"must_ask,omitempty"`
	// Network states what this run gets from the outbound boundary
	// (readbox.Reach). A statement on the prompt, never a second question.
	Network string `json:"network,omitempty"`
}

func (s *Server) handleApprovals(w http.ResponseWriter, _ *http.Request) {
	pending := s.Approvals.Pending()
	out := make([]pendingApproval, 0, len(pending))
	for _, p := range pending {
		out = append(out, pendingApproval{
			ChangeSetID:   p.Request.ChangeSetID,
			WorkspaceID:   p.Request.WorkspaceID,
			WorkspaceName: p.Request.WorkspaceName,
			Provider:      p.Request.Provider,
			Summary:       p.Request.Summary,
			CreatedAt:     p.CreatedAt,
			Operations:    p.Request.Operations,
			Kind:          approvalKind(p.Request),
			Command:       p.Request.Command,
			Dir:           p.Request.Dir,
			Rule:          p.Request.Rule,
			Reason:        p.Request.Reason,
			Grant:         p.Request.Grant,
			MustAsk:       p.Request.MustAsk,
			Network:       p.Request.Network,
		})
	}
	writeJSON(w, map[string]any{"approvals": out})
}

// approvalKind never returns empty. A prompt whose kind did not reach the
// desktop would render as no kind at all, and the screen would be back to
// guessing from which fields are populated — which is the failure this field
// replaced. Write is the safe default: it is the one that shows the most.
func approvalKind(req *txn.ApprovalRequest) string {
	if req.Kind != "" {
		return req.Kind
	}
	return txn.KindWrite
}

type resolveRequest struct {
	ChangeSetID string `json:"change_set_id"`
	Approved    bool   `json:"approved"`
	Reason      string `json:"reason,omitempty"`
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChangeSetID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !s.Approvals.Resolve(req.ChangeSetID, req.Approved, req.Reason) {
		http.Error(w, "unknown or already decided approval", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]bool{"resolved": true})
}

// workspaceView adds the workspace root for local display. The control API
// is loopback-only and token-authenticated: this is the desktop UI showing
// the user their own folder, not a remote surface — root_path
// still never appears in MCP responses, Relay traffic, or logs.
type workspaceView struct {
	*store.Workspace
	RootPath string `json:"root_path"`
	// Availability is "available", "unavailable" (volume not mounted) or
	// "missing" (directory moved or deleted). A desktop grant never expires
	// on its own, so these are the only two ways to lose a folder.
	Availability string `json:"availability"`
	// NetworkReach is what this workspace's programs actually get from the
	// outbound boundary: allowed, denied, partial, or unbounded. It is
	// computed here rather than on the page because it is one rule — the
	// workspace's answer against what this kernel can deny — and a second
	// implementation in another language is a second chance to get it wrong.
	NetworkReach string `json:"network_reach"`
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.Manager.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]workspaceView, 0, len(list))
	for _, ws := range list {
		views = append(views, workspaceView{Workspace: ws, RootPath: ws.RootPath,
			Availability: availabilityOf(ws.RootPath), NetworkReach: s.reachOf(ws)})
	}
	current := ""
	if rec, err := s.Manager.Current(r.Context()); err == nil {
		current = rec.ID
	}
	writeJSON(w, map[string]any{"workspaces": views, "current_workspace_id": current})
}

// reachOf reports what this machine will actually do about one workspace's
// outbound traffic. With no Box configured the honest answer is that nothing
// is bounded, not that the workspace's wish came true.
func (s *Server) reachOf(ws *store.Workspace) string {
	return string(s.ReadBox.Reach(readbox.Policy{Network: ws.Network != store.NetworkDeny}))
}

type workspaceNetworkRequest struct {
	ID string `json:"id"`
	// Allow is the workspace's answer, not the machine's. A false here on a
	// machine that cannot deny anything is still recorded: the wish outlives
	// the laptop it was made on, and the face says it went unmet.
	Allow bool `json:"allow"`
}

func (s *Server) handleWorkspaceNetwork(w http.ResponseWriter, r *http.Request) {
	var req workspaceNetworkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.Manager.SetNetwork(r.Context(), req.ID, req.Allow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleWorkspaces(w, r)
}

type workspaceAddRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleWorkspaceAdd(w http.ResponseWriter, r *http.Request) {
	var req workspaceAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rec, err := s.Manager.Add(r.Context(), req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, workspaceView{Workspace: rec, RootPath: rec.RootPath, Availability: availabilityOf(rec.RootPath)})
}

// workspaceAction adapts one Manager method taking a workspace ID into a
// POST handler with an {"id": ...} body.
func (s *Server) workspaceAction(fn func(context.Context, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := fn(r.Context(), req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

type rollbackRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ChangeSetID string `json:"change_set_id"`
	// ConfirmedIn names the local surface that showed the user what would be
	// restored — both list the files first. It becomes the reason on the
	// approval decision, which is why it is an allowlist and not free text.
	ConfirmedIn string `json:"confirmed_in"`
}

// confirmedInReasons maps a caller's surface to the decision reason handed
// to the approval service. Unknown values are refused rather than passed
// through, so no caller can put free text into a decision.
var confirmedInReasons = map[string]string{
	"":        "confirmed in desktop UI",
	"desktop": "confirmed in desktop UI",
	"panel":   "confirmed in the browser side panel",
}

// handleRollback runs a UI-initiated rollback. The engine gates every
// rollback behind the local Approver; here the user's click in the local
// confirm surface IS the local approval, so the handler claims the resulting
// approval request as approved the moment it registers. This resolves
// through the exact same approval service as every other decision — it is a
// local decision made up front, not a bypass.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		http.Error(w, "rollback is not available", http.StatusServiceUnavailable)
		return
	}
	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChangeSetID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	reason, ok := confirmedInReasons[req.ConfirmedIn]
	if !ok {
		http.Error(w, "unknown confirmation surface", http.StatusBadRequest)
		return
	}
	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		rec, err := s.Manager.Current(r.Context())
		if err != nil {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		workspaceID = rec.ID
	}
	ws, err := s.Manager.Open(r.Context(), workspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	type outcome struct {
		res *txn.Result
		err error
	}
	resCh := make(chan outcome, 1)
	go func() {
		res, err := s.Engine.Rollback(r.Context(), ws, req.ChangeSetID)
		resCh <- outcome{res, err}
	}()

	claimKey := "rollback:" + req.ChangeSetID
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Second)
	claimed := false
	for {
		select {
		case out := <-resCh:
			if out.err != nil {
				http.Error(w, out.err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, out.res)
			return
		case <-ticker.C:
			// Conflict and window-expired results return without ever
			// requesting approval, so keep watching both channels.
			if !claimed && s.Approvals.Resolve(claimKey, true, reason) {
				claimed = true
			}
		case <-timeout:
			http.Error(w, "rollback timed out", http.StatusGatewayTimeout)
			return
		}
	}
}

type acceptRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ChangeSetID string `json:"change_set_id"`
}

// handleAccept records that the user reviewed an applied change set and said
// it was right.
//
// It is a record, not an authorization. Nothing is gated on it and no later
// write waits for it : making acceptance blocking would turn it into
// a second approval, and the ladder already settled who gets asked what.
// What it buys is that "a human read this diff and said it was right" stops
// being indistinguishable from "the rollback window closed on a change nobody
// ever saw".
//
// The evidence the user judges is all engine-written — operations, hashes,
// timings, the audit trail. The model that proposed the change contributes
// nothing to this decision, not even the account of what it did.
func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	var req acceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChangeSetID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		rec, err := s.Manager.Current(r.Context())
		if err != nil {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		workspaceID = rec.ID
	}
	rec, err := s.Store.GetChangeSet(r.Context(), req.ChangeSetID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown change set", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rec.WorkspaceID != workspaceID {
		http.Error(w, "change set belongs to another workspace", http.StatusBadRequest)
		return
	}
	if rec.Status != store.ChangeSetApplied {
		http.Error(w, "only an applied change set can be accepted", http.StatusConflict)
		return
	}
	first := rec.AcceptedAt.IsZero()

	updated, err := s.Store.AcceptChangeSet(r.Context(), req.ChangeSetID, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only the call that actually recorded the acceptance writes the audit
	// row, so a double click leaves one entry rather than two identical ones
	// that would read as two separate reviews.
	if first && !updated.AcceptedAt.IsZero() {
		if err := s.Store.AppendAuditEvent(r.Context(), &store.AuditEvent{
			ChangeSetID: updated.ID,
			EventType:   "change_set_accepted",
			Result:      "ok",
			CreatedAt:   updated.AcceptedAt,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, updated)
}

// saveBlockBudget bounds how long a save request blocks on local approval
// before degrading to pending_approval. The extension polls with the
// returned change_set_id; a Native Messaging round-trip must not hang for
// the platform-sized approval budgets.
const saveBlockBudget = 5 * time.Second

// routeSave runs the user's route rules over one save. Rules answer the
// question the browser cannot: an answer arrives with a name Fylane guessed,
// not a location the user chose. A matched "route" rule moves the file into
// its destination directory; a matched "ask" rule forces local confirmation
// even where the policy would have let it through.
//
// Rules are user preference and grant nothing: the resulting path goes
// through the same sandbox validation as any other write, so a misconfigured
// rule can misfile a save inside the workspace but can never leave it. When
// the table cannot be read the save proceeds unrouted — a broken preference
// file must not stop a write the user asked for.
func (s *Server) routeSave(provider string, ops []txn.Operation) (bool, []string) {
	if s.Rules == nil {
		return false, nil
	}
	rules, err := s.Rules.Load()
	if err != nil || len(rules) == 0 {
		return false, nil
	}
	mustAsk := false
	var credited []string
	for i := range ops {
		out := routerule.Apply(rules, provider, ops[i].Path)
		ops[i].Path = out.Path
		if out.Ask {
			mustAsk = true
		}
		if out.RuleID != "" {
			credited = append(credited, out.RuleID)
		}
	}
	return mustAsk, credited
}

// creditRules records that these rules routed a save that actually landed.
// Replayed results are skipped: retrying a pending change set must not count
// the same save twice.
func (s *Server) creditRules(res *txn.Result, ruleIDs []string) {
	if s.Rules == nil || len(ruleIDs) == 0 || res == nil ||
		res.Status != txn.StatusApplied || res.Replayed {
		return
	}
	rules, err := s.Rules.Load()
	if err != nil {
		return
	}
	changed := false
	for _, id := range ruleIDs {
		if routerule.CountUse(rules, id) {
			changed = true
		}
	}
	if changed {
		// A failed write here loses a counter, nothing else; the save has
		// already landed and must not be reported as failed because of it.
		_ = s.Rules.Save(rules)
	}
}

// handleSave writes browser-extension saves through the same transaction
// engine and approval service as every MCP write. Existing
// files are a conflict unless the request carries their expected hash —
// never a silent overwrite.
//
// The shapes below used to live in shared/nmproto because the browser
// extension sent them. It no longer does, and the desktop first-run
// write is the only caller left, so they belong to this API.
// SaveFile is one file of a save request. Content travels base64-encoded so
// binary payloads survive JSON.
type SaveFile struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
	// ExpectedSHA256 turns the write into an update of an existing file;
	// empty means create — an existing file at the path is a conflict,
	// never a silent overwrite.
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

// SaveRequest asks the Companion to write files into a workspace.
type SaveRequest struct {
	WorkspaceID string     `json:"workspace_id,omitempty"`
	Provider    string     `json:"provider"`
	Summary     string     `json:"summary"`
	Files       []SaveFile `json:"files"`
	// ChangeSetID retries a pending_approval result.
	ChangeSetID string `json:"change_set_id,omitempty"`
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		http.Error(w, "saving is not available", http.StatusServiceUnavailable)
		return
	}
	var req SaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Files) == 0 {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		rec, err := s.Manager.Current(r.Context())
		if err != nil {
			http.Error(w, "no current workspace", http.StatusBadRequest)
			return
		}
		workspaceID = rec.ID
	}
	ws, err := s.Manager.Open(r.Context(), workspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ops := make([]txn.Operation, 0, len(req.Files))
	for _, f := range req.Files {
		if f.Path == "" {
			http.Error(w, "file path is required", http.StatusBadRequest)
			return
		}
		content, err := base64.StdEncoding.DecodeString(f.ContentBase64)
		if err != nil {
			http.Error(w, "content_base64 is not valid base64", http.StatusBadRequest)
			return
		}
		op := txn.Operation{Path: f.Path, Content: string(content), ExpectedSHA256: f.ExpectedSHA256}
		if f.ExpectedSHA256 == "" {
			op.Type = txn.OpCreate
		} else {
			op.Type = txn.OpUpdate
		}
		ops = append(ops, op)
	}
	summary := req.Summary
	if summary == "" {
		summary = fmt.Sprintf("Save %d file(s) from the browser", len(ops))
	}
	mustAsk, credited := s.routeSave(req.Provider, ops)

	ctx, cancel := context.WithTimeout(r.Context(), saveBlockBudget)
	defer cancel()
	res, err := s.Engine.Execute(ctx, ws, txn.Request{
		ChangeSetID: req.ChangeSetID,
		Provider:    req.Provider,
		Summary:     summary,
		Operations:  ops,
		MustAsk:     mustAsk,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.creditRules(res, credited)
	writeJSON(w, res)
}

func (s *Server) handleChangeSets(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		if rec, err := s.Manager.Current(r.Context()); err == nil {
			workspaceID = rec.ID
		}
	}
	if workspaceID == "" {
		http.Error(w, "workspace_id is required", http.StatusBadRequest)
		return
	}
	list, err := s.Store.ListChangeSets(r.Context(), workspaceID, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"change_sets": list})
}
