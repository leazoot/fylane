package ctlapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
)

// The memory view (Fylane-V3 board 17). What a platform wrote through the
// memory tools is the user's to read, correct, export and delete from the
// desktop; this is the surface it does that on. A remote machine's memory
// arrives through the same handlers via the per-machine proxy, because it
// is the same Core on the other end.

// memoryPageSize is how many notes one answer carries; the desktop asks for
// the next page by id.
const memoryPageSize = 40

// memorySearchLimit bounds a search the way the tool's does.
const memorySearchLimit = 60

// memoryUserProvider marks a state page the user rewrote by hand, so the
// next workspace_info says so instead of crediting a platform.
const memoryUserProvider = "user"

type memoryDoc struct {
	State    *store.MemoryState  `json:"state"`
	Notes    []*store.MemoryNote `json:"notes"`
	Live     int                 `json:"live"`
	Archived int                 `json:"archived"`
	// NextBeforeID continues the listing; absent when this page is the last.
	NextBeforeID int64 `json:"next_before_id,omitempty"`
}

// memoryWorkspace resolves the workspace a request is about: the query or
// body field, else the current one.
func (s *Server) memoryWorkspace(r *http.Request, id string) (*store.Workspace, int, error) {
	if id == "" {
		rec, err := s.Manager.Current(r.Context())
		if err != nil {
			return nil, http.StatusBadRequest, errors.New("workspace_id is required")
		}
		return rec, 0, nil
	}
	rec, err := s.Manager.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, http.StatusNotFound, errors.New("unknown workspace")
		}
		return nil, http.StatusInternalServerError, err
	}
	return rec, 0, nil
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ws, code, err := s.memoryWorkspace(r, q.Get("workspace_id"))
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	archived := q.Get("archived") == "1"
	var before int64
	if v := q.Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil || before <= 0 {
			http.Error(w, "before must be a note id", http.StatusBadRequest)
			return
		}
	}
	ctx := r.Context()
	doc := memoryDoc{Notes: []*store.MemoryNote{}}
	if doc.State, err = s.Store.GetMemoryState(ctx, ws.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if doc.Live, doc.Archived, err = s.Store.CountMemoryNotes(ctx, ws.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var notes []*store.MemoryNote
	if query := strings.TrimSpace(q.Get("q")); query != "" {
		// Search reaches both halves of the trail; the filter on screen
		// still decides which half is shown.
		found, err := s.Store.SearchMemoryNotes(ctx, ws.ID, query, memorySearchLimit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, n := range found {
			if n.Archived == archived {
				notes = append(notes, n)
			}
		}
	} else {
		list := s.Store.ListMemoryNotes
		if archived {
			list = s.Store.ListArchivedMemoryNotes
		}
		if notes, err = list(ctx, ws.ID, before, memoryPageSize+1); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(notes) > memoryPageSize {
			notes = notes[:memoryPageSize]
			doc.NextBeforeID = notes[len(notes)-1].ID
		}
	}
	if notes != nil {
		doc.Notes = notes
	}
	writeJSON(w, doc)
}

type memoryPageRequest struct {
	WorkspaceID string           `json:"workspace_id"`
	Page        store.MemoryPage `json:"page"`
}

// handleMemoryPage rewrites the state page by the user's hand. The same
// bounds the tool enforces apply; an over-long field is refused, never cut,
// because the person typing it is right there to shorten it.
func (s *Server) handleMemoryPage(w http.ResponseWriter, r *http.Request) {
	var req memoryPageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ws, code, err := s.memoryWorkspace(r, req.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if err := s.Store.SaveMemoryState(r.Context(), ws.ID, memoryUserProvider, req.Page, time.Now()); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, store.ErrMemoryBounds) {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	state, err := s.Store.GetMemoryState(r.Context(), ws.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, state)
}

type memoryNoteRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ID          int64  `json:"id"`
}

func (s *Server) handleMemoryNoteDelete(w http.ResponseWriter, r *http.Request) {
	var req memoryNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ws, code, err := s.memoryWorkspace(r, req.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if err := s.Store.DeleteMemoryNote(r.Context(), ws.ID, req.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "no such note", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"deleted": req.ID})
}

type memoryClearRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

// handleMemoryClear forgets a workspace's memory. The desktop asks twice
// before calling; the Core has nothing to add to that.
func (s *Server) handleMemoryClear(w http.ResponseWriter, r *http.Request) {
	var req memoryClearRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ws, code, err := s.memoryWorkspace(r, req.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if err := s.Store.DeleteMemory(r.Context(), ws.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"cleared": true})
}

type memoryExport struct {
	Filename string `json:"filename"`
	Markdown string `json:"markdown"`
}

// handleMemoryExport renders the whole memory as one Markdown document. The
// desktop shell puts it where the user chooses; the Core never writes it
// anywhere itself, so a remote machine's export lands on the machine the
// window is on.
func (s *Server) handleMemoryExport(w http.ResponseWriter, r *http.Request) {
	ws, code, err := s.memoryWorkspace(r, r.URL.Query().Get("workspace_id"))
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	ctx := r.Context()
	state, err := s.Store.GetMemoryState(ctx, ws.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var live, archived []*store.MemoryNote
	for before, more := int64(0), true; more; {
		page, err := s.Store.ListMemoryNotes(ctx, ws.ID, before, 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		live = append(live, page...)
		if more = len(page) == 200; more {
			before = page[len(page)-1].ID
		}
	}
	for before, more := int64(0), true; more; {
		page, err := s.Store.ListArchivedMemoryNotes(ctx, ws.ID, before, 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		archived = append(archived, page...)
		if more = len(page) == 200; more {
			before = page[len(page)-1].ID
		}
	}
	now := time.Now()
	writeJSON(w, memoryExport{
		Filename: exportFilename(ws.Name, now),
		Markdown: memoryMarkdown(ws.Name, state, live, archived, now),
	})
}

func exportFilename(name string, now time.Time) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r == ' ' || r == '/' || r == '\\' || r == ':':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`<>"|?*`, r):
		default:
			b.WriteRune(r)
		}
	}
	base := strings.Trim(b.String(), "-.")
	if base == "" {
		base = "workspace"
	}
	return fmt.Sprintf("%s-memory-%s.md", base, now.Format("2006-01-02"))
}

// memoryMarkdown lays the memory out the way a person would read it: the
// state page first, then the trail newest first, archived notes last under
// their own heading.
func memoryMarkdown(name string, state *store.MemoryState, live, archived []*store.MemoryNote, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Memory: %s\n\n", name)
	fmt.Fprintf(&b, "Exported %s by Fylane.\n", now.Format("2006-01-02 15:04"))
	if state != nil {
		b.WriteString("\n## Current state\n")
		fmt.Fprintf(&b, "\nUpdated %s", state.UpdatedAt.Local().Format("2006-01-02 15:04"))
		if state.Provider != "" {
			fmt.Fprintf(&b, " by %s", state.Provider)
		}
		b.WriteString(".\n")
		section := func(title, body string) {
			if strings.TrimSpace(body) == "" {
				return
			}
			fmt.Fprintf(&b, "\n### %s\n\n%s\n", title, strings.TrimSpace(body))
		}
		section("Goal", state.Page.Goal)
		section("Progress", state.Page.Progress)
		section("Next", state.Page.Next)
		list := func(title string, items []string) {
			if len(items) == 0 {
				return
			}
			fmt.Fprintf(&b, "\n### %s\n\n", title)
			for _, it := range items {
				fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(it))
			}
		}
		list("Decisions", state.Page.Decisions)
		list("Open questions", state.Page.Open)
	}
	notes := func(title string, list []*store.MemoryNote) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n", title)
		for _, n := range list {
			fmt.Fprintf(&b, "\n### %s\n\n", strings.TrimSpace(n.Title))
			var facts []string
			facts = append(facts, n.CreatedAt.Local().Format("2006-01-02 15:04"))
			if n.Provider != "" {
				facts = append(facts, n.Provider)
			}
			if n.ChangeSetID != "" {
				facts = append(facts, "change set "+n.ChangeSetID)
			}
			if n.RunID != "" {
				facts = append(facts, "command "+n.RunID)
			}
			fmt.Fprintf(&b, "_%s_\n", strings.Join(facts, " · "))
			if body := strings.TrimSpace(n.Body); body != "" {
				fmt.Fprintf(&b, "\n%s\n", body)
			}
		}
	}
	notes("Notes", live)
	notes("Archived notes", archived)
	return b.String()
}
