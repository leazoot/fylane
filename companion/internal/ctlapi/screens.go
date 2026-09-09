package ctlapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/store"
)

// Endpoints backing the desktop screens that the approval and workspace
// endpoints do not already cover: route rules, source connection state, and
// clearing the trace.

func (s *Server) handleRules(w http.ResponseWriter, _ *http.Request) {
	if s.Rules == nil {
		http.Error(w, "route rules are not available", http.StatusServiceUnavailable)
		return
	}
	rules, err := s.Rules.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rules == nil {
		rules = []routerule.Rule{}
	}
	// Shadowing is derived, never stored: the user's order is the only
	// authority, and a stale flag would outlive the edit that fixed it.
	writeJSON(w, map[string]any{"rules": rules, "shadowed_by": routerule.ShadowedBy(rules)})
}

// handleRulesSave replaces the whole list, so the order the user sees is the
// order that is stored — order is priority, and Fylane never reorders.
func (s *Server) handleRulesSave(w http.ResponseWriter, r *http.Request) {
	if s.Rules == nil {
		http.Error(w, "route rules are not available", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Rules []routerule.Rule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.Rules.Save(req.Rules); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"rules": req.Rules, "shadowed_by": routerule.ShadowedBy(req.Rules)})
}

// sourceView is one row on the Sources screen. It reports connection state
// only — no host names, no permissions: the desktop does not ask sites for
// anything, it waits for content to be sent from the other side.
type sourceView struct {
	Provider     string `json:"provider"`
	Connected    bool   `json:"connected"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	LanesCarried int    `json:"lanes_carried"`
}

// knownProviders is the fixed set of sources the product supports; a
// connector for anything else is ignored rather than rendered.
var knownProviders = []string{"chatgpt", "claude", "grok"}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	connectors, err := s.Store.ListConnectors(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byProvider := map[string]*store.Connector{}
	for _, c := range connectors {
		p := strings.ToLower(c.Provider)
		if prev, ok := byProvider[p]; !ok || lastSeen(c).After(lastSeen(prev)) {
			byProvider[p] = c
		}
	}

	// "Lanes carried" is the count of change sets that actually landed from
	// that source — the only number here that is not a guess.
	carried := map[string]int{}
	if list, err := s.Store.ListChangeSetsByStatus(r.Context(), store.ChangeSetApplied); err == nil {
		for _, cs := range list {
			carried[strings.ToLower(cs.Provider)]++
		}
	}

	out := make([]sourceView, 0, len(knownProviders))
	for _, p := range knownProviders {
		v := sourceView{Provider: p, LanesCarried: carried[p]}
		if c, ok := byProvider[p]; ok {
			v.Connected = c.Status == connectorActive
			if t := lastSeen(c); !t.IsZero() {
				v.LastSeenAt = t.Format(timeLayout)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"sources": out})
}

const timeLayout = "2006-01-02T15:04:05Z07:00"

// connectorActive is the status an upserted connector carries while the
// platform still holds a working grant.
const connectorActive = "active"

// lastSeen is the most recent evidence the source is alive: a tool call
// beats a connection, because a connector can stay registered long after
// the platform stopped calling.
func lastSeen(c *store.Connector) time.Time {
	if c.LastToolCallAt.After(c.LastConnectedAt) {
		return c.LastToolCallAt
	}
	return c.LastConnectedAt
}

// handleBackupsClear removes the undo copies for a workspace without touching
// the record of what was written. The trace screen that used to own this is
// gone, and without an entry the copies were only ever reclaimed by the
// retention loop — a user watching disk fill up had nothing to press.
func (s *Server) handleBackupsClear(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		rec, err := s.Manager.Current(r.Context())
		if err != nil {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		workspaceID = rec.ID
	}
	cleared, backups, err := s.Store.ClearBackups(r.Context(), workspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The rows no longer promise an undo, so a copy left behind is wasted
	// space rather than a broken promise: report what went, do not roll back.
	removed := 0
	if s.BackupRoot != "" {
		for _, loc := range backups {
			if loc == "" || strings.ContainsAny(loc, `/\`) || loc == ".." {
				continue // backup locations are plain change-set ids
			}
			if err := os.RemoveAll(filepath.Join(s.BackupRoot, loc)); err == nil {
				removed++
			}
		}
	}
	writeJSON(w, map[string]any{"cleared": cleared, "backups_removed": removed})
}

// handleTraceClear deletes the recorded history. It is the one irreversible
// action on the desktop, and it touches no file on disk: the change sets and
// their backups go, the workspace does not.
func (s *Server) handleTraceClear(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		rec, err := s.Manager.Current(r.Context())
		if err != nil {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		workspaceID = rec.ID
	}
	cleared, backups, err := s.Store.DeleteChangeSets(r.Context(), workspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The rows are gone; drop the undo copies they referenced. A failure
	// here leaves disk space in use but no dangling promise, so it is
	// reported rather than rolled back.
	removed := 0
	if s.BackupRoot != "" {
		for _, loc := range backups {
			if loc == "" || strings.ContainsAny(loc, `/\`) || loc == ".." {
				continue // backup locations are plain change-set ids
			}
			if err := os.RemoveAll(filepath.Join(s.BackupRoot, loc)); err == nil {
				removed++
			}
		}
	}
	writeJSON(w, map[string]any{"cleared": cleared, "backups_removed": removed})
}

// Workspace availability. The desktop holds real paths, so a grant never
// expires on its own (design A04): the only ways to lose a folder are the
// volume not being mounted (UNAVAILABLE, reconnect it) or the directory
// having been moved or deleted (MISSING, relocate it). The two have
// different repairs, so they are reported separately.
const (
	availabilityOK          = "available"
	availabilityUnavailable = "unavailable"
	availabilityMissing     = "missing"
)

func availabilityOf(root string) string {
	if root == "" {
		return availabilityMissing
	}
	if _, err := os.Stat(root); err == nil {
		return availabilityOK
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Permission denied, I/O error on an unmounted network share, and
		// friends: the path may well come back, so it is not "missing".
		return availabilityUnavailable
	}
	// The directory itself is gone. If its parent is gone too, the whole
	// volume is more likely detached than the folder deleted.
	parent := filepath.Dir(root)
	if _, err := os.Stat(parent); err != nil {
		return availabilityUnavailable
	}
	return availabilityMissing
}
