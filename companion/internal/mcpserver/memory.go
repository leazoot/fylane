package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/store"
)

// Project memory: what a model should know when a new conversation opens
// on a workspace it has worked in before. A page that is rewritten (goal,
// progress, next, decisions, open questions) and a trail of notes that is
// only ever appended to.
//
// What comes back is bounded by construction. The page has fixed fields
// with fixed limits; recall lists titles, not bodies; the trail is
// searched or paged, never returned whole; and every answer stops at the
// platform's inline budget. Anything over a limit is cut at the way in
// and the caller is told which field was cut, so the length problem is
// solved where text arrives rather than by compressing later.

const (
	memoryRecallDefault = 10
	memoryRecallMax     = 30
	memorySearchDefault = 10
	memorySearchMax     = 30
	memoryReadDefault   = 5
	memoryReadMax       = 20
	memoryReadIDs       = 10
	memoryQueryBytes    = 200
	memorySnippetBytes  = 160
	memoryHintBytes     = 200
)

// MemoryStore keeps the page and the trail. Implemented by *store.Store.
type MemoryStore interface {
	SaveMemoryState(ctx context.Context, workspaceID, provider string, page store.MemoryPage, at time.Time) error
	GetMemoryState(ctx context.Context, workspaceID string) (*store.MemoryState, error)
	AddMemoryNote(ctx context.Context, n *store.MemoryNote) error
	ListMemoryNotes(ctx context.Context, workspaceID string, beforeID int64, limit int) ([]*store.MemoryNote, error)
	GetMemoryNotes(ctx context.Context, workspaceID string, ids []int64) ([]*store.MemoryNote, error)
	SearchMemoryNotes(ctx context.Context, workspaceID, query string, limit int) ([]*store.MemoryNote, error)
	CountMemoryNotes(ctx context.Context, workspaceID string) (live, archived int, err error)
	GetChangeSet(ctx context.Context, id string) (*store.ChangeSet, error)
}

type memoryNoteInput struct {
	WorkspaceID string            `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Title       string            `json:"title,omitempty" jsonschema:"One line: what was done or learned. Required unless only the page is rewritten. Up to 120 bytes."`
	Body        string            `json:"body,omitempty" jsonschema:"What, why, and what it means next time. Up to 2000 bytes; longer is cut."`
	ChangeSetID string            `json:"change_set_id,omitempty" jsonschema:"The change set this note is about, if any."`
	RunID       string            `json:"run_id,omitempty" jsonschema:"The task_id of the command run this note is about, if any."`
	Page        *store.MemoryPage `json:"page,omitempty" jsonschema:"Rewrite the workspace's current-state page. The whole page is replaced: send every field that should stay."`
}

type memoryNoteOutput struct {
	NoteID      int64    `json:"note_id,omitempty"`
	PageUpdated bool     `json:"page_updated,omitempty"`
	Cut         []string `json:"cut,omitempty" jsonschema:"Fields that were longer than their limit and were cut to it."`
	Notes       int      `json:"notes" jsonschema:"How many notes the workspace now has."`
}

func (t *toolset) memoryNote(ctx context.Context, _ *mcp.CallToolRequest, in memoryNoteInput) (*mcp.CallToolResult, memoryNoteOutput, error) {
	var zero memoryNoteOutput
	if t.memory == nil {
		return nil, zero, fmt.Errorf("memory is not available on this Companion")
	}
	if strings.TrimSpace(in.Title) == "" && in.Page == nil {
		return nil, zero, fmt.Errorf("nothing to remember: give a title (and body), a page, or both")
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	var out memoryNoteOutput
	if in.Page != nil {
		page, cut := boundPage(*in.Page)
		out.Cut = append(out.Cut, cut...)
		if err := t.memory.SaveMemoryState(ctx, ws.ID(), t.provider, page, time.Time{}); err != nil {
			return nil, zero, err
		}
		out.PageUpdated = true
	}
	if strings.TrimSpace(in.Title) != "" {
		if err := t.memoryRefs(ctx, ws.ID(), in.ChangeSetID, in.RunID); err != nil {
			return nil, zero, err
		}
		n := &store.MemoryNote{WorkspaceID: ws.ID(), Provider: t.provider, ChangeSetID: in.ChangeSetID, RunID: in.RunID}
		var cut bool
		if n.Title, cut = bound(strings.TrimSpace(in.Title), store.MemoryTitleBytes); cut {
			out.Cut = append(out.Cut, "title")
		}
		if n.Body, cut = bound(strings.TrimSpace(in.Body), store.MemoryBodyBytes); cut {
			out.Cut = append(out.Cut, "body")
		}
		if err := t.memory.AddMemoryNote(ctx, n); err != nil {
			return nil, zero, err
		}
		out.NoteID = n.ID
	}
	if out.Notes, _, err = t.memory.CountMemoryNotes(ctx, ws.ID()); err != nil {
		return nil, zero, err
	}
	return nil, out, nil
}

// memoryRefs checks that what a note points at exists and belongs to the
// same workspace: a note is the one place a model writes an id back, and
// a wrong one would send the next conversation down a wrong trail.
func (t *toolset) memoryRefs(ctx context.Context, workspaceID, changeSetID, runID string) error {
	if changeSetID != "" {
		if len(changeSetID) > store.MemoryRefBytes {
			return fmt.Errorf("change_set_id is not one this Companion issued")
		}
		c, err := t.memory.GetChangeSet(ctx, changeSetID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && c.WorkspaceID != workspaceID) {
			return fmt.Errorf("unknown change_set_id %q in this workspace", changeSetID)
		}
		if err != nil {
			return err
		}
	}
	if runID != "" {
		if len(runID) > store.MemoryRefBytes {
			return fmt.Errorf("run_id is not one this Companion issued")
		}
		if t.runs == nil {
			return fmt.Errorf("run_id cannot be checked on this Companion; leave it out")
		}
		e, err := t.runs.FindRun(ctx, runID)
		if err != nil {
			return err
		}
		if e == nil || e.WorkspaceID != workspaceID {
			return fmt.Errorf("unknown run_id %q in this workspace", runID)
		}
	}
	return nil
}

// bound cuts s to limit bytes on a rune boundary and says whether it did.
func bound(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	return truncateUTF8(s, limit), true
}

// boundPage cuts every field of a page to its limit and names the ones it
// cut.
func boundPage(p store.MemoryPage) (store.MemoryPage, []string) {
	var cut []string
	var was bool
	if p.Goal, was = bound(strings.TrimSpace(p.Goal), store.MemoryGoalBytes); was {
		cut = append(cut, "page.goal")
	}
	if p.Progress, was = bound(strings.TrimSpace(p.Progress), store.MemoryProgressBytes); was {
		cut = append(cut, "page.progress")
	}
	if p.Next, was = bound(strings.TrimSpace(p.Next), store.MemoryNextBytes); was {
		cut = append(cut, "page.next")
	}
	p.Decisions, was = boundList(p.Decisions)
	if was {
		cut = append(cut, "page.decisions")
	}
	p.Open, was = boundList(p.Open)
	if was {
		cut = append(cut, "page.open")
	}
	return p, cut
}

func boundList(items []string) ([]string, bool) {
	cut := false
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(out) == store.MemoryListItems {
			cut = true
			break
		}
		item, was := bound(item, store.MemoryListItemBytes)
		cut = cut || was
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, cut
	}
	return out, cut
}

type memoryRecallInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Limit       int    `json:"limit,omitempty" jsonschema:"How many recent note titles to list. Default 10, maximum 30."`
}

type noteHead struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Provider    string    `json:"provider,omitempty"`
	Title       string    `json:"title"`
	ChangeSetID string    `json:"change_set_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
}

type memoryRecallOutput struct {
	Page      *store.MemoryPage `json:"page,omitempty" jsonschema:"The current-state page, as last rewritten."`
	UpdatedAt time.Time         `json:"page_updated_at,omitzero"`
	UpdatedBy string            `json:"page_updated_by,omitempty"`
	Notes     []noteHead        `json:"notes" jsonschema:"The newest notes, titles only, newest first."`
	NoteCount int               `json:"note_count"`
	Archived  int               `json:"archived_count,omitempty" jsonschema:"Notes folded into the page by a compaction; still readable by id and searchable."`
	Hint      string            `json:"hint"`
}

func (t *toolset) memoryRecall(ctx context.Context, _ *mcp.CallToolRequest, in memoryRecallInput) (*mcp.CallToolResult, memoryRecallOutput, error) {
	var zero memoryRecallOutput
	if t.memory == nil {
		return nil, zero, fmt.Errorf("memory is not available on this Companion")
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	limit, err := boundLimit(in.Limit, memoryRecallDefault, memoryRecallMax)
	if err != nil {
		return nil, zero, err
	}
	out := memoryRecallOutput{Notes: []noteHead{}}
	st, err := t.memory.GetMemoryState(ctx, ws.ID())
	switch {
	case err == nil:
		page := st.Page
		out.Page, out.UpdatedAt, out.UpdatedBy = &page, st.UpdatedAt, st.Provider
	case !errors.Is(err, store.ErrNotFound):
		return nil, zero, err
	}
	notes, err := t.memory.ListMemoryNotes(ctx, ws.ID(), 0, limit)
	if err != nil {
		return nil, zero, err
	}
	for _, n := range notes {
		out.Notes = append(out.Notes, noteHead{ID: n.ID, At: n.CreatedAt, Provider: n.Provider, Title: n.Title, ChangeSetID: n.ChangeSetID, RunID: n.RunID})
	}
	if out.NoteCount, out.Archived, err = t.memory.CountMemoryNotes(ctx, ws.ID()); err != nil {
		return nil, zero, err
	}
	switch {
	case out.Page == nil && out.NoteCount == 0:
		out.Hint = "Nothing is remembered about this workspace yet. When something worth keeping happens, write it with memory_note; rewrite the page when the plan changes."
	default:
		out.Hint = "Full text of a note: memory_read with its id. Older notes: memory_read with before_id. By topic: memory_search. Keep the page current with memory_note." + compactDue(out.NoteCount)
	}
	return nil, out, nil
}

type memorySearchInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Query       string `json:"query" jsonschema:"Words to look for; every word must appear in the title or body. Up to 200 bytes."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Default 10, maximum 30."`
}

type memoryMatch struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Title    string    `json:"title"`
	Snippet  string    `json:"snippet" jsonschema:"The part of the body around the first word, bounded."`
	Archived bool      `json:"archived,omitempty"`
}

type memorySearchOutput struct {
	Matches []memoryMatch `json:"matches" jsonschema:"Newest first."`
}

func (t *toolset) memorySearch(ctx context.Context, _ *mcp.CallToolRequest, in memorySearchInput) (*mcp.CallToolResult, memorySearchOutput, error) {
	var zero memorySearchOutput
	if t.memory == nil {
		return nil, zero, fmt.Errorf("memory is not available on this Companion")
	}
	query := strings.TrimSpace(in.Query)
	switch {
	case query == "":
		return nil, zero, fmt.Errorf("query is required")
	case len(query) > memoryQueryBytes:
		return nil, zero, fmt.Errorf("query is %d bytes; the limit is %d", len(query), memoryQueryBytes)
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	limit, err := boundLimit(in.Limit, memorySearchDefault, memorySearchMax)
	if err != nil {
		return nil, zero, err
	}
	notes, err := t.memory.SearchMemoryNotes(ctx, ws.ID(), query, limit)
	if err != nil {
		return nil, zero, err
	}
	out := memorySearchOutput{Matches: []memoryMatch{}}
	first := strings.Fields(query)[0]
	for _, n := range notes {
		out.Matches = append(out.Matches, memoryMatch{ID: n.ID, At: n.CreatedAt, Title: n.Title, Snippet: snippet(n.Body, first), Archived: n.Archived})
	}
	return nil, out, nil
}

// snippet is the bounded piece of body around the first occurrence of
// word, or the body's start when the word is only in the title.
func snippet(body, word string) string {
	at := strings.Index(strings.ToLower(body), strings.ToLower(word))
	if at < 0 {
		at = 0
	}
	start := max(at-memorySnippetBytes/3, 0)
	for start > 0 && start < len(body) && (body[start]&0xC0) == 0x80 {
		start--
	}
	piece := truncateUTF8(body[start:], memorySnippetBytes)
	if start > 0 {
		piece = "…" + piece
	}
	if start+len(piece) < len(body) {
		piece += "…"
	}
	return piece
}

type memoryReadInput struct {
	WorkspaceID string  `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	IDs         []int64 `json:"ids,omitempty" jsonschema:"Notes to read in full, up to 10. Leave empty to page through the trail newest first."`
	BeforeID    int64   `json:"before_id,omitempty" jsonschema:"Paging: return notes older than this id, from next_before_id of the previous answer."`
	Limit       int     `json:"limit,omitempty" jsonschema:"Paging: how many notes. Default 5, maximum 20; fewer come back when the inline budget is reached."`
}

type memoryReadOutput struct {
	Notes        []store.MemoryNote `json:"notes" jsonschema:"Newest first."`
	NextBeforeID int64              `json:"next_before_id,omitempty" jsonschema:"Present when older notes remain; pass as before_id."`
}

func (t *toolset) memoryRead(ctx context.Context, _ *mcp.CallToolRequest, in memoryReadInput) (*mcp.CallToolResult, memoryReadOutput, error) {
	var zero memoryReadOutput
	if t.memory == nil {
		return nil, zero, fmt.Errorf("memory is not available on this Companion")
	}
	if len(in.IDs) > memoryReadIDs {
		return nil, zero, fmt.Errorf("ids has %d entries; the limit is %d", len(in.IDs), memoryReadIDs)
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	var notes []*store.MemoryNote
	paging := len(in.IDs) == 0
	limit := 0
	if paging {
		if limit, err = boundLimit(in.Limit, memoryReadDefault, memoryReadMax); err != nil {
			return nil, zero, err
		}
		if in.BeforeID < 0 {
			return nil, zero, fmt.Errorf("before_id must be positive")
		}
		notes, err = t.memory.ListMemoryNotes(ctx, ws.ID(), in.BeforeID, limit+1)
	} else {
		notes, err = t.memory.GetMemoryNotes(ctx, ws.ID(), in.IDs)
	}
	if err != nil {
		return nil, zero, err
	}
	out := memoryReadOutput{Notes: []store.MemoryNote{}}
	more := paging && len(notes) > limit
	if more {
		notes = notes[:limit]
	}
	used := 0
	for i, n := range notes {
		size := len(n.Title) + len(n.Body)
		// The first note always fits: an answer with nothing in it and a
		// cursor pointing at the same place would loop forever.
		if i > 0 && used+size > t.budget() {
			more = paging
			break
		}
		used += size
		out.Notes = append(out.Notes, *n)
	}
	if more && len(out.Notes) > 0 {
		out.NextBeforeID = out.Notes[len(out.Notes)-1].ID
	}
	return nil, out, nil
}

func boundLimit(n, def, max int) (int, error) {
	switch {
	case n < 0:
		return 0, fmt.Errorf("limit must be positive")
	case n == 0:
		return def, nil
	case n > max:
		return 0, fmt.Errorf("limit may not exceed %d", max)
	}
	return n, nil
}

// memoryHint is what workspace_info carries so a conversation that never
// calls the memory tools still starts from where the last one stopped.
type memoryHint struct {
	UpdatedAt time.Time `json:"page_updated_at,omitzero"`
	Goal      string    `json:"goal,omitempty" jsonschema:"From the page, bounded."`
	Next      string    `json:"next,omitempty" jsonschema:"From the page, bounded."`
	Notes     int       `json:"notes"`
	Hint      string    `json:"hint"`
}

func (t *toolset) memoryPreview(ctx context.Context, workspaceID string) (*memoryHint, error) {
	if t.memory == nil {
		return nil, nil
	}
	live, archived, err := t.memory.CountMemoryNotes(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	st, err := t.memory.GetMemoryState(ctx, workspaceID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if st == nil && live+archived == 0 {
		return nil, nil
	}
	h := &memoryHint{Notes: live + archived, Hint: "Call memory_recall before starting: it has the page and the recent notes from earlier conversations."}
	if st != nil {
		h.UpdatedAt = st.UpdatedAt
		h.Goal, _ = bound(st.Page.Goal, memoryHintBytes)
		h.Next, _ = bound(st.Page.Next, memoryHintBytes)
	}
	return h, nil
}
