package ctlapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/leazoot/fylane/companion/internal/machines"
)

// MachineControl is the remote machine list and the way through to each
// machine's own control API. Implemented by *machines.Manager.
type MachineControl interface {
	List() []machines.Status
	Add(machines.Machine) (machines.Status, error)
	Remove(id string) error
	Connect(id string) error
	Disconnect(id string) error
	Install(id string) error
	Proxy(id string) (http.Handler, error)
}

func (s *Server) handleMachines(w http.ResponseWriter, _ *http.Request) {
	if s.Machines == nil {
		http.Error(w, "remote machines are not configured", http.StatusNotFound)
		return
	}
	list := s.Machines.List()
	if list == nil {
		list = []machines.Status{}
	}
	writeJSON(w, map[string]any{"machines": list})
}

func (s *Server) handleMachineAdd(w http.ResponseWriter, r *http.Request) {
	if s.Machines == nil {
		http.Error(w, "remote machines are not configured", http.StatusNotFound)
		return
	}
	var req machines.Machine
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	st, err := s.Machines.Add(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, st)
}

// machineAction adapts one MachineControl method taking a machine ID into a
// POST handler with an {"id": ...} body.
func (s *Server) machineAction(fn func(MachineControl, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Machines == nil {
			http.Error(w, "remote machines are not configured", http.StatusNotFound)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if err := fn(s.Machines, req.ID); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, machines.ErrUnknown) {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

// handleMachineProxy forwards /v1/machines/{id}/<path> to that machine's
// control API as /<path>. The local token got the caller in here; the
// machine's own token, which only the link knows, gets the request through.
func (s *Server) handleMachineProxy(w http.ResponseWriter, r *http.Request) {
	if s.Machines == nil {
		http.Error(w, "remote machines are not configured", http.StatusNotFound)
		return
	}
	h, err := s.Machines.Proxy(r.PathValue("id"))
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, machines.ErrUnknown) {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + r.PathValue("rest")
	r2.URL.RawPath = ""
	h.ServeHTTP(w, r2)
}
