package ctlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/tasks"
)

// CommandGate is the command approval rung and its workspace grants.
// Implemented by *cmdgate.Gate.
type CommandGate interface {
	Rung() cmdgate.Rung
	SetRung(rung cmdgate.Rung, confirm bool) error
	List(ctx context.Context) ([]*store.CommandGrant, error)
	Revoke(ctx context.Context, workspaceID string) error
}

// DelegationGates lists and withdraws the standing agent authorizations.
// Implemented by *cmdgate.Delegations.
type DelegationGates interface {
	List() []cmdgate.DelegationGrant
	Revoke(workspaceID, agent string) bool
}

// ProxyProvider is one locally configured MCP provider, as much of it as the
// desktop needs. Deliberately not the program behind it: the name and the
// trust are what a person can act on, and the command line is a local detail
// that has no reason to travel even this far.
type ProxyProvider struct {
	Name string `json:"name"`
	// Trust is "ask" or "workspace". The second one is an authorization in
	// force, which is exactly what this list exists to keep visible.
	Trust string `json:"trust"`
}

// ProxyLister reports the MCP providers this Core will forward calls to.
// Implemented by *app.App over the mcpgate registry.
type ProxyLister interface {
	Proxies() []ProxyProvider
}

// LanguageServer is one installed language server as the settings page sees
// it. Deliberately not the program behind it and not the workspace it is
// indexing: the same line ProxyProvider draws, for the same reason.
type LanguageServer struct {
	Name string `json:"name"`
	// Extensions are the file suffixes this server answers for. The name on
	// its own says nothing to a person who has not met gopls.
	Extensions []string `json:"extensions"`
	// Running is whether one is up right now, anywhere. A language server is
	// a program this Companion started and did not stop, and that is worth
	// being able to see.
	Running bool `json:"running"`
}

// LanguageServerLister reports the installed servers code_navigate may start.
// Implemented by *app.App over the lsp supervisor.
type LanguageServerLister interface {
	LanguageServers() []LanguageServer
}

// TaskLister exposes running and recent work to the desktop task view.
// Implemented by *tasks.Manager.
type TaskLister interface {
	List() []tasks.Snapshot
	Cancel(id string) error
	Status(id string, stdoutCursor, stderrCursor int) (tasks.Snapshot, error)
	Clear() int
}

// cancelSettle is how long the cancel endpoint waits for the task to actually
// stop before answering. Cancellation is asynchronous — the work goroutine has
// to notice its context — so replying immediately would hand the UI a list
// that still says RUNNING, and a user who pressed stop and saw nothing happen
// presses it again.
const cancelSettle = 2 * time.Second

type commandSettings struct {
	Rung   string                `json:"rung"`
	Grants []*store.CommandGrant `json:"grants"`
	// Providers is the locally configured MCP gateway list. Same reason as
	// Grants: an authorization is only safe while it is visible, and a
	// provider marked trust: workspace is one.
	Providers []ProxyProvider `json:"providers"`
	// LanguageServers is the code_navigate list. It is here rather than in
	// the preferences because it is the same question the two lists above
	// answer: which programs on this machine may this Companion start.
	LanguageServers []LanguageServer `json:"language_servers"`
	// Delegations are the agents a yes still covers, and for how long. The
	// broadest authorization this Core hands out, so the first to keep on
	// screen.
	Delegations []cmdgate.DelegationGrant `json:"delegations"`
	// DelegationHours is how long one yes lasts, for the prompt to say.
	DelegationHours int `json:"delegation_hours"`
}

// handleCommands reports the rung and every workspace grant in force. The
// desktop renders both permanently: a grant nobody can see is the failure
// mode this design has to avoid.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		http.Error(w, "command execution is not configured", http.StatusNotFound)
		return
	}
	grants, err := s.Commands.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []*store.CommandGrant{}
	}
	providers := []ProxyProvider{}
	if s.Proxies != nil {
		if p := s.Proxies.Proxies(); p != nil {
			providers = p
		}
	}
	servers := []LanguageServer{}
	if s.LanguageServers != nil {
		if l := s.LanguageServers.LanguageServers(); l != nil {
			servers = l
		}
	}
	delegations := []cmdgate.DelegationGrant{}
	if s.Delegations != nil {
		if d := s.Delegations.List(); d != nil {
			delegations = d
		}
	}
	writeJSON(w, commandSettings{
		Rung:            string(s.Commands.Rung()),
		Grants:          grants,
		Providers:       providers,
		LanguageServers: servers,
		Delegations:     delegations,
		DelegationHours: int(cmdgate.DelegationTTL / time.Hour),
	})
}

// handleDelegationRevoke withdraws one agent's standing authorization in one
// workspace. Like a command grant, it takes effect on the next request and
// answers with the settings the page redraws from.
func (s *Server) handleDelegationRevoke(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil || s.Delegations == nil {
		http.Error(w, "delegation is not configured", http.StatusNotFound)
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		Agent       string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.WorkspaceID == "" || req.Agent == "" {
		http.Error(w, "workspace_id and agent are required", http.StatusBadRequest)
		return
	}
	s.Delegations.Revoke(req.WorkspaceID, req.Agent)
	s.handleCommands(w, r)
}

type rungRequest struct {
	Rung string `json:"rung"`
	// Confirm is required to reach the open rung. The UI sends it only after
	// the user has read what the rung means; the daemon refuses without it
	// so no other caller can slip past that screen.
	Confirm bool `json:"confirm"`
}

func (s *Server) handleCommandRung(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		http.Error(w, "command execution is not configured", http.StatusNotFound)
		return
	}
	var req rungRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.Commands.SetRung(cmdgate.Rung(req.Rung), req.Confirm); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, cmdgate.ErrConfirmationRequired) {
			// Not a malformed request: the caller asked for something that
			// needs the user to have acknowledged it.
			status = http.StatusPreconditionRequired
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.handleCommands(w, r)
}

func (s *Server) handleCommandRevoke(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		http.Error(w, "command execution is not configured", http.StatusNotFound)
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.WorkspaceID == "" {
		http.Error(w, "workspace_id is required", http.StatusBadRequest)
		return
	}
	if err := s.Commands.Revoke(r.Context(), req.WorkspaceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.handleCommands(w, r)
}

// handleTasks lists what has run and what was refused, for the task view.
// The in-memory manager holds running work and its output; the audit table
// holds everything else, including the runs this process never saw and the
// commands the user refused. Neither is the whole history on its own.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		http.Error(w, "command execution is not configured", http.StatusNotFound)
		return
	}
	live := s.Tasks.List()
	if live == nil {
		live = []tasks.Snapshot{}
	}
	var past []*store.ExecEvent
	if s.Store != nil {
		var err error
		if past, err = s.Store.ListExecEvents(r.Context(), historyLimit); err != nil {
			// The live list is still worth answering with: losing history is
			// worse than losing it silently, so it is said out loud rather
			// than turned into a 500 that empties the screen.
			http.Error(w, "reading command history: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"tasks": mergeHistory(live, past)})
}

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		http.Error(w, "command execution is not configured", http.StatusNotFound)
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TaskID == "" {
		http.Error(w, "task_id is required", http.StatusBadRequest)
		return
	}
	if err := s.Tasks.Cancel(req.TaskID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	deadline := time.Now().Add(cancelSettle)
	for time.Now().Before(deadline) {
		snap, err := s.Tasks.Status(req.TaskID, 0, 0)
		if err != nil || snap.State.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.handleTasks(w, r)
}

// handleTaskClear drops the finished task records. Nothing on disk changes and
// no backup is touched — what disappears is the list of what ran. Rolling a
// write back goes through the trace, which has its own confirmation.
func (s *Server) handleTaskClear(w http.ResponseWriter, r *http.Request) {
	if s.Tasks == nil {
		http.Error(w, "command execution is not configured", http.StatusNotFound)
		return
	}
	s.Tasks.Clear()
	s.handleTasks(w, r)
}
