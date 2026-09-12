package machines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The router sits in front of this Companion's /mcp. A call that names a
// workspace on another machine is carried, unchanged, to that machine's
// own loopback /mcp through the ssh forward; everything else goes to the
// local handler exactly as before. The platform sees one endpoint.
//
// Only the request envelope is read, and only for two fields: the
// workspace_id in a tool call (or the workspace in a resource URI), and the
// task_id of task_status, which is routed to wherever that task started.
// Bodies are never logged.

const (
	// maxBody matches the tunnel's frame bound: a request larger than what
	// the relay would carry is not one this router needs to understand.
	maxBody = 64 << 20
	// listTTL is how long a machine's workspace list is trusted before it is
	// asked again. Short enough that a folder added on the desktop is
	// addressable on the next call; long enough that a burst of reads does
	// not become a burst of control-API calls.
	listTTL = 3 * time.Second
	// taskMemory is how long a forwarded task_id stays routable. Tasks
	// themselves are forgotten by the remote after an hour.
	taskMemory = 2 * time.Hour
	// connectWait bounds how long a call waits for a machine that is on
	// its way. A Core that just restarted has every link connecting for a
	// few seconds, and the platform's retry lands inside them; "not
	// connected" then is wrong by a moment. Platforms allow a tool call
	// tens of seconds, so ten is spent here at most.
	connectWait = 10 * time.Second
)

// Workspace is a remote workspace as workspace_info will list it.
type Workspace struct {
	WorkspaceID string
	Name        string
	Mode        string
	Status      string
	Machine     string
	// Current is the current folder of the machine the window stands on:
	// the one workspace_info calls current instead of a local folder.
	Current   bool
	machineID string
}

// MCPHandler wraps the local MCP handler with the router.
func (m *Manager) MCPHandler(local http.Handler) http.Handler {
	m.rt.local = local
	return m.rt
}

// Workspaces lists every workspace on every online machine, for
// workspace_info. Names and ids only.
func (m *Manager) Workspaces(ctx context.Context) []Workspace {
	return m.rt.Workspaces(ctx)
}

// forget drops the cached workspace list so the next ask lists again.
func (r *router) forget() {
	r.mu.Lock()
	r.listed = time.Time{}
	r.mu.Unlock()
}

func newRouter(m *Manager) *router {
	return &router{mgr: m, client: &http.Client{}, tasks: map[string]taskOwner{}}
}

type router struct {
	mgr    *Manager
	local  http.Handler
	client *http.Client

	mu        sync.Mutex
	listed    time.Time
	byID      map[string]Workspace // workspace id → owner
	tasks     map[string]taskOwner // task id → owner
	lastPurge time.Time
}

type taskOwner struct {
	machineID string
	at        time.Time
}

// envelope is the little of a JSON-RPC request the router reads.
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name      string `json:"name"`
		URI       string `json:"uri"`
		Arguments struct {
			WorkspaceID string `json:"workspace_id"`
			TaskID      string `json:"task_id"`
		} `json:"arguments"`
	} `json:"params"`
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || req.Body == nil {
		r.local.ServeHTTP(w, req)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	var env envelope
	if json.Unmarshal(body, &env) != nil {
		r.local.ServeHTTP(w, req)
		return
	}
	owner, name := r.route(req.Context(), env)
	if owner == "" {
		r.local.ServeHTTP(w, req)
		return
	}
	r.forward(w, req, body, env, owner, name)
}

// route names the machine a request belongs to, or "" for this one.
func (r *router) route(ctx context.Context, env envelope) (machineID, name string) {
	switch env.Method {
	case "tools/call":
		if env.Params.Name == "task_status" && env.Params.Arguments.TaskID != "" {
			if o, ok := r.taskOwner(env.Params.Arguments.TaskID); ok {
				return r.online(o)
			}
			return "", ""
		}
		if env.Params.Arguments.WorkspaceID != "" {
			if ws, ok := r.workspace(ctx, env.Params.Arguments.WorkspaceID); ok {
				return r.online(ws.machineID)
			}
		}
	case "resources/read":
		if u, err := url.Parse(env.Params.URI); err == nil && u.Scheme == "fylane" && u.Host != "" {
			if ws, ok := r.workspace(ctx, u.Host); ok {
				return r.online(ws.machineID)
			}
		}
	}
	return "", ""
}

// online confirms the machine is reachable right now and returns its name.
// A machine that owns the workspace but is offline still routes to itself:
// the caller gets "not connected", not a silent fall-through to a local
// workspace of the same id (there is none, but the shape matters).
func (r *router) online(machineID string) (string, string) {
	for _, e := range r.mgr.endpoints() {
		if e.id == machineID {
			return e.id, e.name
		}
	}
	r.mgr.mu.Lock()
	l, ok := r.mgr.links[machineID]
	r.mgr.mu.Unlock()
	if !ok {
		return "", ""
	}
	return machineID, l.m.Name
}

func (r *router) taskOwner(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.tasks[id]
	return o.machineID, ok
}

// workspace finds which machine a workspace id belongs to, refreshing the
// lists once on a miss so a folder added a moment ago is found. A miss
// for an id that is not local, while a machine is still on its way, waits
// for that machine and looks again: the folder may be on it.
func (r *router) workspace(ctx context.Context, id string) (Workspace, bool) {
	r.mu.Lock()
	ws, ok := r.byID[id]
	fresh := time.Since(r.listed) < listTTL
	r.mu.Unlock()
	if ok {
		return ws, true
	}
	if !fresh {
		if ws, ok = r.lookup(ctx, id); ok {
			return ws, true
		}
	}
	if r.mgr.isLocal(id) {
		return Workspace{}, false
	}
	if r.mgr.settle(ctx, r.mgr.pending(""), connectWait) {
		return r.lookup(ctx, id)
	}
	return Workspace{}, false
}

// lookup lists again and looks the id up.
func (r *router) lookup(ctx context.Context, id string) (Workspace, bool) {
	r.refresh(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	ws, ok := r.byID[id]
	return ws, ok
}

// Workspaces waits for the machine the window stands on if it is still
// connecting: a list that leaves out the current machine names the wrong
// folder as current.
func (r *router) Workspaces(ctx context.Context) []Workspace {
	if selected := r.mgr.Selected(); selected != "" {
		if r.mgr.settle(ctx, r.mgr.pending(selected), connectWait) {
			r.forget()
		}
	}
	r.mu.Lock()
	fresh := time.Since(r.listed) < listTTL
	r.mu.Unlock()
	if !fresh {
		r.refresh(ctx)
	}
	// A list can be a few seconds old; a machine that dropped in between
	// must not be offered from it.
	online := map[string]bool{}
	for _, e := range r.mgr.endpoints() {
		online[e.id] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Workspace, 0, len(r.byID))
	for _, ws := range r.byID {
		if online[ws.machineID] {
			out = append(out, ws)
		}
	}
	// Stable order: by machine, then name — a map has none.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Machine != out[j].Machine {
			return out[i].Machine < out[j].Machine
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// refresh asks every online machine for its workspace list through its
// control API. A machine that does not answer keeps nothing: its
// workspaces are simply not offered until it does.
func (r *router) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	found := map[string]Workspace{}
	selected := r.mgr.Selected()
	for _, e := range r.mgr.endpoints() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ctlBase+"/v1/workspaces", nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+e.token)
		resp, err := r.client.Do(req)
		if err != nil {
			continue
		}
		var doc struct {
			Workspaces []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Mode   string `json:"mode"`
				Status string `json:"status"`
			} `json:"workspaces"`
			Current string `json:"current_workspace_id"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		for _, w := range doc.Workspaces {
			if w.Status == "revoked" {
				continue
			}
			found[w.ID] = Workspace{WorkspaceID: w.ID, Name: w.Name, Mode: w.Mode, Status: w.Status, Machine: e.name,
				Current: e.id == selected && w.ID == doc.Current, machineID: e.id}
		}
	}
	r.mu.Lock()
	r.byID = found
	r.listed = time.Now()
	r.mu.Unlock()
}

// skipHeader lists what belongs to one hop and not to the message:
// connection management, the length of a body that is re-framed, and the
// credentials of the surface the request arrived on.
var skipHeader = map[string]bool{
	"Authorization":     true,
	"Cookie":            true,
	"Connection":        true,
	"Content-Length":    true,
	"Host":              true,
	"Keep-Alive":        true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
}

var taskIDPattern = regexp.MustCompile(`"task_id"\s*:\s*"([^"]+)"`)

// forward carries the request to the machine's /mcp and the answer back.
// The response is read whole rather than streamed: a stateless MCP
// response is one message, and reading it lets a new task_id be remembered
// for task_status.
func (r *router) forward(w http.ResponseWriter, req *http.Request, body []byte, env envelope, machineID, name string) {
	base := r.mcpBase(machineID)
	if base == "" && r.mgr.settle(req.Context(), r.mgr.pending(machineID), connectWait) {
		base = r.mcpBase(machineID)
	}
	if base == "" {
		r.unavailable(w, env, fmt.Sprintf("%s is not connected right now", name))
		return
	}
	out, err := http.NewRequestWithContext(req.Context(), http.MethodPost, base+"/mcp", bytes.NewReader(body))
	if err != nil {
		r.unavailable(w, env, err.Error())
		return
	}
	// The whole envelope travels: the protocol names its method and version
	// in headers, and a header this router did not know about must not be
	// the reason a call fails. Only what names this hop stays behind.
	for h, vs := range req.Header {
		if skipHeader[http.CanonicalHeaderKey(h)] {
			continue
		}
		out.Header[h] = append([]string(nil), vs...)
	}
	resp, err := r.client.Do(out)
	if err != nil {
		r.unavailable(w, env, fmt.Sprintf("%s did not answer", name))
		return
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		r.unavailable(w, env, fmt.Sprintf("%s did not answer", name))
		return
	}
	if env.Method == "tools/call" && (env.Params.Name == "run_command" || env.Params.Name == "code_task") {
		r.remember(answer, machineID)
	}
	for h, vs := range resp.Header {
		if skipHeader[http.CanonicalHeaderKey(h)] {
			continue
		}
		w.Header()[h] = append([]string(nil), vs...)
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(answer)
}

func (r *router) mcpBase(machineID string) string {
	for _, e := range r.mgr.endpoints() {
		if e.id == machineID {
			return e.mcpBase
		}
	}
	return ""
}

func (r *router) remember(answer []byte, machineID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Sub(r.lastPurge) > time.Minute {
		for id, o := range r.tasks {
			if now.Sub(o.at) > taskMemory {
				delete(r.tasks, id)
			}
		}
		r.lastPurge = now
	}
	for _, m := range taskIDPattern.FindAllSubmatch(answer, -1) {
		r.tasks[string(m[1])] = taskOwner{machineID: machineID, at: now}
	}
}

// unavailable answers a tool call whose machine cannot be reached with a
// tool error, in the JSON framing every client accepts, so the model reads
// a sentence instead of a transport failure.
func (r *router) unavailable(w http.ResponseWriter, env envelope, msg string) {
	id := env.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	doc := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"isError": true,
			"content": []map[string]string{{"type": "text", "text": msg}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc)
}
