// Package app wires the companion daemon together: configuration, storage,
// workspace manager, MCP server, tunnel client, and process lifecycle
// (single-instance lock, structured logging, graceful shutdown).
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/leazoot/fylane/companion/internal/approval"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/codeagent"
	"github.com/leazoot/fylane/companion/internal/crashlog"
	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/devicecred"
	"github.com/leazoot/fylane/companion/internal/directsrv"
	"github.com/leazoot/fylane/companion/internal/machines"
	"github.com/leazoot/fylane/companion/internal/mcpserver"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/tasks"
	"github.com/leazoot/fylane/companion/internal/tunnelget"
	"github.com/leazoot/fylane/companion/internal/tunnelproc"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/update"
	"github.com/leazoot/fylane/companion/internal/urlfetch"
	"github.com/leazoot/fylane/companion/internal/workspace"
	"github.com/leazoot/fylane/shared/buildinfo"
	"github.com/leazoot/fylane/shared/tunnel"
)

// App is one companion daemon instance.
type App struct {
	cfg *Config
	log *slog.Logger

	// listenAddr receives the bound local address once the listener is up.
	// Buffered so Run never blocks when nobody reads it (tests do).
	listenAddr chan string

	// Ready, when set, is called each time a tunnel Fylane manages announces a
	// public address: the connector URL platforms are given, and the function
	// that mints a pairing code for it. The desktop app learns both by polling
	// the control API; `share` is a foreground command with no window to poll
	// from, so it is told instead.
	Ready func(connectorURL string, code func(context.Context) (string, time.Duration, error))

	// Ask, when set, is handed every approval request as it arrives, with
	// the function that decides it. The desktop app finds requests by
	// polling the control API; a Companion in a terminal has no window and
	// is told instead, so a write or a command can be answered where it
	// was started. A request answered from either place is settled for both.
	Ask func(p *approval.Pending, resolve func(changeSetID string, approved bool, reason string) bool)

	// restart is closed when the connection mode changed. Switching between a
	// relay and this machine's own public surface swaps the listeners, the
	// authorization server and the pairing origin at once; a fresh process is
	// the only state in which none of that can be half-applied.
	restart    chan struct{}
	restarting atomic.Bool
}

// New returns an App for the given configuration, logging to logger.
// Logs must never contain absolute paths, file contents, or tokens.
func New(cfg *Config, logger *slog.Logger) *App {
	return &App{cfg: cfg, log: logger, listenAddr: make(chan string, 1), restart: make(chan struct{})}
}

// Restarting reports whether a mode switch already asked for a fresh process.
// It is true from the moment the switch is stored, not from the moment the
// process starts going down: the answer to the request that caused it has to
// carry the news, or the caller waits for a daemon that is already leaving.
func (a *App) Restarting() bool { return a.restarting.Load() }

// requestRestart ends this process shortly after the caller's control-API
// response has been written. The desktop shell starts the Core again.
func (a *App) requestRestart() {
	if a.restarting.Swap(true) {
		return
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		a.log.Info("connection mode changed; restarting")
		close(a.restart)
	}()
}

// ListenAddr returns the actual bound address, available after Run has
// started serving.
func (a *App) ListenAddr() <-chan string { return a.listenAddr }

// Run starts the daemon and blocks until ctx is cancelled or a fatal error
// occurs. On cancellation it shuts the HTTP server down gracefully and
// releases all resources.
func (a *App) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	lock, err := acquireInstanceLock(a.cfg.DataDir)
	if err != nil {
		return err
	}
	defer lock.release()

	// Arm local crash capture: fatal panics land in <dataDir>/crash/,
	// nothing ever leaves the machine automatically.
	if _, err := crashlog.Setup(a.cfg.DataDir, buildinfo.Version); err != nil {
		a.log.Warn("crash capture not armed", "error", err)
	}
	defer crashlog.Release()

	// Where to look for a tunnel binary the user consented to download
	//. Telling the resolver where to look fetches nothing and makes
	// nothing eligible to be fetched; without this a download from an earlier
	// run would simply never be found.
	tunnelproc.SetDownloadDir(tunnelget.Dir(a.cfg.DataDir))

	st, err := store.Open(ctx, filepath.Join(a.cfg.DataDir, "fylane.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	// Before anything else can write to the journal: the commands that were
	// running when this Core last stopped are still open rows, and a fresh
	// run of the same command must not be mistaken for one of them.
	reconcileRuns(ctx, st, a.log)

	manager, err := workspace.NewManager(st, a.cfg.DataDir)
	if err != nil {
		return err
	}

	ws, err := a.selectWorkspace(ctx, manager)
	if err != nil {
		return err
	}
	if ws == nil {
		// First run: no workspace yet. The daemon still serves — the control
		// API is how the desktop app registers the first folder (W13), and
		// MCP calls answer with a clear "no workspace selected" error.
		a.log.Info("no workspace configured; add one from the desktop app or restart with -workspace <dir>")
	} else {
		a.log.Info("workspace selected", "workspace_id", ws.ID(), "name", ws.Name())
	}

	notes := newNotifier(a.log, func() string { return Language(a.cfg.DataDir) })
	var approvals *approval.Service
	approvals, err = newApprovals(a.cfg.ApprovalMode, st, a.log, func(p *approval.Pending) {
		notes.Approval(p.Request.Provider)
		if a.Ask != nil {
			go a.Ask(p, approvals.Resolve)
		}
	})
	if err != nil {
		return err
	}
	// The kernel read boundary. It wraps every program this Companion
	// starts. What it cannot do is stated at startup rather than implied by
	// silence: an absence is not a failure, and a platform that offers no
	// boundary still runs every other check exactly as before.
	box := readbox.New(ReadBoundaryEnabled(a.cfg.DataDir))
	switch box.State() {
	case readbox.Enforced:
		a.log.Info("read boundary in force", "detail", box.Why())
	default:
		a.log.Warn("no read boundary on subprocess reads", "detail", box.Why())
	}

	// Language servers, for code_navigate and for the impact line on
	// the approval face. The supervisor owns every server it starts,
	// which is the whole reason it exists rather than each caller starting
	// processes of its own; closing it here is that ownership's other half.
	navigators, err := a.languageServers(box)
	if err != nil {
		return err
	}
	defer navigators.Close()

	engine := &txn.Engine{
		Store:      st,
		BackupRoot: filepath.Join(a.cfg.DataDir, "backups"),
		Approver:   approvals,
		Impact:     impactFromLSP{sup: navigators},
	}
	if n, err := engine.Recover(ctx); err != nil {
		return fmt.Errorf("crash recovery: %w", err)
	} else if n > 0 {
		a.log.Warn("recovered interrupted change sets", "count", n)
	}
	go a.backupCleanupLoop(ctx, engine)

	// Command execution. The task manager owns every running process;
	// closing it is what makes "the Core exited" mean "so did the builds it
	// started", so it is closed before the store it audits into.
	taskManager := tasks.New(tasks.Options{})
	defer taskManager.Close()
	runner := cmdexec.New(execAuditor{store: st, log: a.log}, box)

	// Delegation targets. Both are probed at call time, so a
	// Companion started before the user installed one still finds it later.
	// A delegated agent gets the same base environment a command does plus
	// the names the user listed, and nothing else.
	agentEnv, err := LoadAgentEnvPassthrough(a.cfg.DataDir)
	if err != nil {
		return err
	}
	agents := codeagent.NewRegistry(
		&codeagent.Codex{EnvPassthrough: agentEnv["codex"], Box: box},
		&codeagent.OpenCode{},
	)
	if installed := agents.Installed(); len(installed) > 0 {
		a.log.Info("coding agents available for delegation", "agents", strings.Join(installed, ","))
	}

	// The local MCP gateway. Providers come from the settings file and
	// from nowhere remote; a rejected entry is named in the log rather than
	// dropped in silence, because the most likely reason for a rejection is a
	// provider spelled as a downloader.
	providers, err := a.mcpProviders()
	if err != nil {
		return err
	}

	storedRung, err := LoadCommandRung(a.cfg.DataDir)
	if err != nil {
		return err
	}
	gate := cmdgate.New(st, storedRung, func(rung string) error {
		return SaveCommandRung(a.cfg.DataDir, rung)
	})
	if gate.Rung() == cmdgate.Open {
		// The open rung runs destructive commands with nobody watching. It
		// is the user's call, but it is never a quiet one.
		a.log.Warn("command approval is on the open rung: allowed and gated commands run without asking")
	}

	// Other machines reached over ssh. Started here because the MCP handler
	// below lists their workspaces and the router in front of it forwards
	// calls to them.
	remotes := machines.New(machines.Options{Store: MachineStore{DataDir: a.cfg.DataDir},
		Version: buildinfo.Version, Log: a.log})
	if err := remotes.Start(ctx); err != nil {
		return err
	}

	var handler http.Handler = mcpserver.Handler(mcpserver.Deps{
		Source:     manager,
		Engine:     engine,
		Reads:      approvals,
		Rules:      RuleStore{DataDir: a.cfg.DataDir},
		Exec:       runner,
		Tasks:      taskManager,
		Approve:    approvals,
		Gate:       gate,
		ExecAudit:  execAuditor{store: st, log: a.log},
		Runs:       st,
		Agents:     agents,
		Providers:  providers,
		Navigators: navigators,
		Box:        box,
		Seen:       seenRecorder(ctx, st, a.log),
		Remotes:    remoteWorkspaces(remotes),
	}, &mcpserver.Options{
		EnableWaitProbe: a.cfg.EnableProbes,
		MaxInlineBytes:  a.cfg.MaxInlineBytes,
		// Read per call, not captured here: a ceiling changed on the settings
		// page applies to the next command, not to the next restart.
		TaskCeiling: func() time.Duration { return TaskTimeout(a.cfg.DataDir) },
	})
	handler = remotes.MCPHandler(handler)
	handler = logMCPMethods(a.log, handler)
	if a.cfg.EnableProbes {
		a.log.Warn("diagnostic probe tools enabled")
	}

	// The tunnel client is built before the control API so the status
	// endpoint can report its connection state; it starts running below.
	var tun *tunnel.Client
	if a.cfg.RelayURL != "" {
		token, err := a.cfg.TunnelToken()
		if err != nil {
			return err
		}
		tun = &tunnel.Client{RelayURL: a.cfg.RelayURL, Token: token, Handler: handler, EagerSSE: true}
	}

	// Direct mode: this process is the authorization server platforms talk to,
	// and a tunnel publishes its loopback listener. No relay is in the path.
	var direct *directsrv.Server
	if a.cfg.DirectAddr != "" {
		name, _ := os.Hostname()
		direct, err = directsrv.Open(ctx, a.cfg.DataDir, name, handler, a.log)
		if err != nil {
			return err
		}
		defer direct.Close()
		direct.SetPublicURL(a.cfg.PublicURL)
		// The claim listener is this process's own loopback mux (below), so
		// the page is told where it actually is rather than guessing 8787.
		direct.SetClaimOrigin("http://" + a.cfg.Addr)
		go direct.PurgeLoop(ctx)
	}

	// The tunnel that publishes that listener, when the user picked one for
	// Fylane to run. Whatever address it hands out becomes the issuer, so a
	// quick tunnel renaming the machine on restart just works.
	var tunProc *tunnelproc.Manager
	if direct != nil && a.cfg.TunnelProvider != "" {
		tunProc, err = a.startTunnel(ctx, direct)
		if err != nil {
			// A tunnel that will not start is not a reason to stop serving:
			// the local surfaces and the desktop app still work, and the
			// connect screen shows what went wrong.
			a.log.Error("tunnel not started", "provider", a.cfg.TunnelProvider, "error", err)
		} else {
			defer tunProc.Stop()
		}
	}

	ctl := &ctlapi.Server{Manager: manager, Store: st, Approvals: approvals, Engine: engine,
		RelayURL: a.cfg.RelayURL, Commands: gate, Tasks: taskManager,
		Proxies: proxyList{providers}, LanguageServers: serverList{navigators}, ReadBox: box}
	ctl.PersistApprovalMode = func(mode string) error {
		return SaveApprovalMode(a.cfg.DataDir, mode)
	}
	ctl.Rules = RuleStore{DataDir: a.cfg.DataDir}
	ctl.Prefs = newPrefs(a.cfg.DataDir, box)
	ctl.BackupRoot = filepath.Join(a.cfg.DataDir, "backups")
	if tun != nil {
		ctl.Tunnel = tun
	}
	if direct != nil {
		ctl.Tunnel = direct
		ctl.ConnectorURL = direct.ConnectorURL
		ctl.Pairing = func(ctx context.Context) (string, time.Duration, error) {
			if direct.PublicURL() == "" {
				return "", 0, errors.New("no public address yet: start the tunnel first")
			}
			return direct.PairingCode(ctx)
		}
	}
	// The desktop connect sheet mints pairing codes over the control API,
	// registering the device first when this machine never paired.
	if a.cfg.RelayURL != "" {
		if base, err := BaseURLFromTunnel(a.cfg.RelayURL); err == nil {
			connector := base + "/mcp"
			ctl.ConnectorURL = func() string { return connector }
			ctl.Pairing = func(ctx context.Context) (string, time.Duration, error) {
				if _, err := devicecred.Load(base); err != nil {
					name, _ := os.Hostname()
					if _, err := devicecred.Register(ctx, base, name); err != nil {
						return "", 0, err
					}
				}
				return devicecred.PairingCode(ctx, base)
			}
		}
	}
	// Wired in both modes: the connect screen asks how platforms reach this
	// machine, which has an answer with or without a relay. Only starting a
	// tunnel is refused when a relay is doing the publishing.
	ctl.Connect = newConnectControl(a, direct, tunProc, ctx, ctl.ConnectorURL)
	// Beta-phase update check: notice only, nothing is downloaded.
	if a.cfg.UpdateManifestURL != "" {
		fetcher := &urlfetch.Fetcher{}
		checker := &update.Checker{
			ManifestURL: a.cfg.UpdateManifestURL,
			Current:     buildinfo.Version,
			Fetch: func(ctx context.Context, url string) ([]byte, error) {
				res, err := fetcher.Fetch(ctx, url)
				if err != nil {
					return nil, err
				}
				return res.Data, nil
			},
		}
		ctl.Updates = checker
		go a.updateCheckLoop(ctx, checker)
	}
	// The MCP listener is bound before the control API starts so the control
	// file can name it: a Companion driving this one over ssh forwards both.
	ln, err := net.Listen("tcp", a.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", a.cfg.Addr, err)
	}
	ctl.MCPAddr = ln.Addr().String()

	ctl.Machines = remotes

	ctlAddr, err := ctl.Start(ctx, a.cfg.DataDir)
	if err != nil {
		ln.Close()
		return err
	}
	a.log.Info("control api listening", "addr", ctlAddr)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	// Push pairing: the relay's pairing page — running in a browser
	// on this same machine — claims the flow over loopback; approval happens
	// in the desktop app and is executed against the relay with the device
	// credentials. Only same-machine pages on the relay's origin get through.
	paired := pairedRecorder(ctx, st, a.log)
	announce := func(clientName string) { notes.Pairing(clientName) }
	if a.cfg.RelayURL != "" {
		if base, err := BaseURLFromTunnel(a.cfg.RelayURL); err == nil {
			claims := newPairClaims(a.log, paired, announce)
			claims.AllowedOrigin = base
			claims.Info = func(ctx context.Context, requestID string) (string, string, error) {
				return devicecred.PairRequestInfo(ctx, base, requestID)
			}
			claims.Approve = func(ctx context.Context, requestID string) (string, error) {
				return devicecred.PairApprove(ctx, base, requestID)
			}
			mux.Handle("/pair/claim", claims.Handler())
			ctl.PairClaims = claims
		}
	}
	// Direct mode runs the same push-pairing flow, minus the network: the
	// authorization server the page is talking to is this process. The allowed
	// origin follows the tunnel, which can rename this machine at any time.
	if direct != nil {
		claims := newPairClaims(a.log, paired, announce)
		claims.AllowedOriginFn = direct.PublicURL
		claims.Info = direct.RequestInfo
		claims.Approve = direct.Approve
		mux.Handle("/pair/claim", claims.Handler())
		ctl.PairClaims = claims
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	a.listenAddr <- ln.Addr().String()
	a.log.Info("mcp server listening", "addr", ln.Addr().String())

	if tun != nil {
		go func() {
			if err := tun.Run(ctx); err != nil && ctx.Err() == nil {
				a.log.Error("tunnel stopped", "error", err)
			}
		}()
		a.log.Info("tunnel starting", "relay", a.cfg.RelayURL)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	// The public surface is a second server on its own listener, never the
	// local one: /mcp there is unauthenticated by design, and a tunnel points
	// at a port, not at a path.
	var publicSrv *http.Server
	if direct != nil {
		publicLn, err := net.Listen("tcp", a.cfg.DirectAddr)
		if err != nil {
			return fmt.Errorf("listening on %s: %w", a.cfg.DirectAddr, err)
		}
		publicSrv = &http.Server{Handler: direct.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() { errCh <- publicSrv.Serve(publicLn) }()
		a.log.Info("direct connect surface listening", "addr", publicLn.Addr().String(),
			"public_url", a.cfg.PublicURL)
		if a.cfg.PublicURL == "" {
			a.log.Warn("no public URL configured; start a tunnel and set -public-url before connecting a platform")
		}
	}

	shutdown := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if publicSrv != nil {
			publicSrv.Shutdown(shutdownCtx)
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down: %w", err)
		}
		a.log.Info("shut down")
		return nil
	}

	select {
	case err := <-errCh:
		return fmt.Errorf("mcp server: %w", err)
	case <-ctx.Done():
		return shutdown()
	case <-a.restart:
		return shutdown()
	}
}

// updateCheckLoop runs the notice-only update check at startup and then
// daily until shutdown.
func (a *App) updateCheckLoop(ctx context.Context, checker *update.Checker) {
	ticker := time.NewTicker(update.Interval)
	defer ticker.Stop()
	for {
		if s, err := checker.Check(ctx); err != nil {
			if ctx.Err() == nil {
				a.log.Warn("update check", "error", err)
			}
		} else if s.Available {
			a.log.Info("update available", "latest", s.Latest, "current", buildinfo.Version)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// backupCleanupLoop enforces backup retention (7 days / 1 GiB) at
// startup and then hourly until shutdown.
func (a *App) backupCleanupLoop(ctx context.Context, engine *txn.Engine) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if n, err := engine.CleanupBackups(ctx); err != nil {
			a.log.Error("backup cleanup", "error", err)
		} else if n > 0 {
			a.log.Info("backup cleanup", "removed", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// logMCPMethods records the JSON-RPC method and response status of each MCP
// request at debug level — method names and sizes only, never bodies or
// paths, so platform integration failures are diagnosable locally.
func logMCPMethods(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !log.Enabled(r.Context(), slog.LevelDebug) || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var probe struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		json.Unmarshal(body, &probe)
		rec := &statusWriter{ResponseWriter: w}
		// Non-standard methods (platform protocol drift) get their full
		// request and response captured: they carry platform protocol, never
		// user file content — standard MCP methods are never dumped.
		dump := !standardMCPMethod(probe.Method)
		if dump {
			rec.capture = &bytes.Buffer{}
		}
		next.ServeHTTP(rec, r)
		if dump {
			log.Debug("non-standard mcp request", "rpc_method", probe.Method,
				"request", string(body), "response", rec.capture.String())
		} else {
			log.Debug("mcp request", "rpc_method", probe.Method, "has_id", probe.ID != nil,
				"status", rec.status, "req_bytes", len(body))
		}
	})
}

func standardMCPMethod(m string) bool {
	switch {
	case m == "initialize", m == "ping", m == "tools/list", m == "tools/call",
		m == "resources/list", m == "resources/read", m == "resources/templates/list",
		m == "prompts/list", m == "prompts/get", m == "completion/complete",
		m == "logging/setLevel":
		return true
	case strings.HasPrefix(m, "notifications/"):
		return true
	}
	return false
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	capture *bytes.Buffer
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if s.capture != nil && s.capture.Len() < 1<<16 {
		s.capture.Write(p)
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// selectWorkspace resolves the workspace to serve: the -workspace flag
// registers (or finds) that root and makes it current; otherwise the stored
// current selection is used. (nil, nil) means no workspace is configured
// yet — first run proceeds and the desktop app registers one over the
// control API.
func (a *App) selectWorkspace(ctx context.Context, manager *workspace.Manager) (*workspace.Workspace, error) {
	if a.cfg.Workspace != "" {
		rec, err := manager.Add(ctx, a.cfg.Workspace)
		if errors.Is(err, workspace.ErrDuplicateRoot) {
			rec, err = a.findByRoot(ctx, manager, a.cfg.Workspace)
		}
		if err != nil {
			return nil, err
		}
		if err := manager.SetCurrent(ctx, rec.ID); err != nil {
			return nil, err
		}
		return manager.Open(ctx, rec.ID)
	}

	rec, err := manager.Current(ctx)
	if errors.Is(err, workspace.ErrNoCurrent) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return manager.Open(ctx, rec.ID)
}

func (a *App) findByRoot(ctx context.Context, manager *workspace.Manager, root string) (*store.Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	list, err := manager.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range list {
		if w.RootPath == real {
			return w, nil
		}
	}
	return nil, fmt.Errorf("workspace for %q not found", filepath.Base(root))
}

// remoteWorkspaces adapts the machine list to what workspace_info offers.
func remoteWorkspaces(remotes *machines.Manager) func(context.Context) []mcpserver.RemoteWorkspace {
	return func(ctx context.Context) []mcpserver.RemoteWorkspace {
		list := remotes.Workspaces(ctx)
		out := make([]mcpserver.RemoteWorkspace, 0, len(list))
		for _, w := range list {
			out = append(out, mcpserver.RemoteWorkspace{WorkspaceID: w.WorkspaceID, Name: w.Name,
				Mode: w.Mode, Status: w.Status, Machine: w.Machine, Current: w.Current})
		}
		return out
	}
}
