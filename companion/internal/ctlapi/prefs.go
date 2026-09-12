package ctlapi

import (
	"encoding/json"
	"net/http"
)

// The settings page's execution preferences: how long a command may run,
// whether the window offers a stop button, whether the Core comes back after a
// reboot, and whether the kernel read boundary is in force.
//
// None of them moves the approval line. The rung, the rule table, the path
// sandbox and the audit log are unaffected by anything here. The read
// boundary is the one that is a defence and not a convenience — it is a layer
// on top of those checks, so turning it off subtracts a layer without lifting
// a check, and it is never turned off quietly.

// PrefStore reads and writes those preferences. Implemented by the app
// package, which owns config.json and the login entry.
type PrefStore interface {
	Prefs() PrefDoc
	SetPrefs(p PrefPatch) (PrefDoc, error)
}

// AutostartDoc is the login-entry row. Supported is false where this build
// has no way to register one, and Detail says why — the row is then drawn
// disabled rather than offering a switch that would do nothing.
type AutostartDoc struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Detail    string `json:"detail,omitempty"`
}

// ReadBoundaryDoc is the kernel read boundary as the settings page sees it.
//
// State is one of "enforced", "absent" or "off", and it is a word rather than
// the pair of booleans AutostartDoc uses because those two admit a fourth
// combination that cannot exist — unsupported and enabled. The distinction
// this field carries is the entire point of it: absent is what the machine
// cannot do, off is what the user chose, and a screen that showed them the
// same way would undo the decision that separated them .
//
// Detail is one sentence saying why, safe to show: it never carries a path.
type ReadBoundaryDoc struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type PrefDoc struct {
	TaskTimeoutSeconds int             `json:"task_timeout_seconds"`
	AllowStopTasks     bool            `json:"allow_stop_tasks"`
	Autostart          AutostartDoc    `json:"autostart"`
	ReadBoundary       ReadBoundaryDoc `json:"read_boundary"`
	// Language is the window's language as last told to the Core; "" until
	// the window has said.
	Language string `json:"language,omitempty"`
}

// PrefPatch carries only what the caller is changing. Every field is a
// pointer: a settings page that flips one switch must not restate — and so
// risk overwriting — the values it did not touch.
type PrefPatch struct {
	TaskTimeoutSeconds *int    `json:"task_timeout_seconds,omitempty"`
	AllowStopTasks     *bool   `json:"allow_stop_tasks,omitempty"`
	Autostart          *bool   `json:"autostart,omitempty"`
	Language           *string `json:"language,omitempty"`
	// ReadBoundary is the one field here that touches a defence rather than a
	// convenience. It is still a preference and not a bypass: turning it off
	// restores the product as it shipped before the boundary existed, and the
	// sandbox, the rule table, the rung and the audit log are untouched by it
	//. What it must never be is quiet, which is why the page that
	// sets it keeps saying so afterwards.
	ReadBoundary *bool `json:"read_boundary,omitempty"`
}

func (s *Server) handlePrefs(w http.ResponseWriter, _ *http.Request) {
	if s.Prefs == nil {
		http.Error(w, "settings are not configured", http.StatusNotFound)
		return
	}
	writeJSON(w, s.Prefs.Prefs())
}

func (s *Server) handlePrefsSave(w http.ResponseWriter, r *http.Request) {
	if s.Prefs == nil {
		http.Error(w, "settings are not configured", http.StatusNotFound)
		return
	}
	var patch PrefPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, "invalid settings document", http.StatusBadRequest)
		return
	}
	doc, err := s.Prefs.SetPrefs(patch)
	if err != nil {
		// A rejected value is the caller's mistake; a login entry that could
		// not be written is the machine's. They read the same to the page,
		// which shows the sentence either way.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, doc)
}
