package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/store"
)

// Compaction keeps the trail from growing without end. There is no model
// on this machine, so the summary is written by the caller: memory_compact
// first hands over the oldest notes, then takes the summary back and
// archives what it covers. Archived notes are not deleted — they stay
// readable by id and searchable — and the page is untouched: the summary
// is one more note, the trail's own memory of itself.

const (
	// memoryCompactAt is the live-note count at which recall starts
	// asking for a compaction. Bounded answers make it a nudge, not a
	// need: nothing breaks above it.
	memoryCompactAt = 200
	// memoryCompactBatch is how many of the oldest notes one compaction
	// covers at most; the inline budget can make it fewer.
	memoryCompactBatch = 40
	memorySummaryBytes = 1500
)

type memoryCompactInput struct {
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Summary     string `json:"summary,omitempty" jsonschema:"Second call: the summary of the notes the first call returned, up to 1500 bytes. Keep decisions, outcomes and anything a later conversation would otherwise have to rediscover."`
	ThroughID   int64  `json:"through_id,omitempty" jsonschema:"Second call: the through_id the first call returned; every live note up to it is archived."`
}

type memoryCompactOutput struct {
	Notes     []store.MemoryNote `json:"notes,omitempty" jsonschema:"First call: the oldest notes, oldest first, to summarize."`
	ThroughID int64              `json:"through_id,omitempty" jsonschema:"First call: pass back with the summary."`
	Archived  int                `json:"archived,omitempty" jsonschema:"Second call: how many notes the summary now stands for."`
	SummaryID int64              `json:"summary_note_id,omitempty"`
	Live      int                `json:"live_notes"`
	Hint      string             `json:"hint"`
}

// MemoryCompactor is the store side of compaction. Implemented by
// *store.Store alongside MemoryStore.
type MemoryCompactor interface {
	OldestMemoryNotes(ctx context.Context, workspaceID string, limit int) ([]*store.MemoryNote, error)
	ArchiveMemoryNotes(ctx context.Context, workspaceID string, throughID int64) (int, error)
}

func (t *toolset) memoryCompact(ctx context.Context, _ *mcp.CallToolRequest, in memoryCompactInput) (*mcp.CallToolResult, memoryCompactOutput, error) {
	var zero memoryCompactOutput
	compactor, ok := t.memory.(MemoryCompactor)
	if t.memory == nil || !ok {
		return nil, zero, fmt.Errorf("memory compaction is not available on this Companion")
	}
	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}
	batch, err := t.compactBatch(ctx, compactor, ws.ID())
	if err != nil {
		return nil, zero, err
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" && in.ThroughID == 0 {
		return t.compactOffer(ctx, ws.ID(), batch)
	}
	switch {
	case summary == "" || in.ThroughID == 0:
		return nil, zero, fmt.Errorf("the second call needs both summary and through_id")
	case len(summary) > memorySummaryBytes:
		return nil, zero, fmt.Errorf("summary is %d bytes; the limit is %d", len(summary), memorySummaryBytes)
	case len(batch) == 0:
		return nil, zero, fmt.Errorf("there is nothing to compact")
	case in.ThroughID > batch[len(batch)-1].ID || in.ThroughID < batch[0].ID:
		return nil, zero, fmt.Errorf("through_id %d is not what the first call returned (%d); call memory_compact without arguments again", in.ThroughID, batch[len(batch)-1].ID)
	}
	note := &store.MemoryNote{WorkspaceID: ws.ID(), Provider: t.provider,
		Title: fmt.Sprintf("Summary of notes up to #%d (from %s)", in.ThroughID, summaryDate(batch[0])),
		Body:  summary}
	if err := t.memory.AddMemoryNote(ctx, note); err != nil {
		return nil, zero, err
	}
	archived, err := compactor.ArchiveMemoryNotes(ctx, ws.ID(), in.ThroughID)
	if err != nil {
		return nil, zero, err
	}
	live, _, err := t.memory.CountMemoryNotes(ctx, ws.ID())
	if err != nil {
		return nil, zero, err
	}
	out := memoryCompactOutput{Archived: archived, SummaryID: note.ID, Live: live,
		Hint: "The summary is now a note; the notes it covers are archived and still searchable. Rewrite the page with memory_note if the summary changed what is current."}
	if live > memoryCompactAt {
		out.Hint += " The trail is still long; call memory_compact again for the next batch."
	}
	return nil, out, nil
}

// compactBatch is the oldest live notes one compaction covers: at most
// memoryCompactBatch, and no more than the inline budget carries. Both
// calls compute it the same way, so the second call can check the first
// call's through_id without remembering anything in between.
func (t *toolset) compactBatch(ctx context.Context, c MemoryCompactor, workspaceID string) ([]*store.MemoryNote, error) {
	notes, err := c.OldestMemoryNotes(ctx, workspaceID, memoryCompactBatch)
	if err != nil {
		return nil, err
	}
	used := 0
	for i, n := range notes {
		size := len(n.Title) + len(n.Body)
		if i > 0 && used+size > t.budget() {
			return notes[:i], nil
		}
		used += size
	}
	return notes, nil
}

func (t *toolset) compactOffer(ctx context.Context, workspaceID string, batch []*store.MemoryNote) (*mcp.CallToolResult, memoryCompactOutput, error) {
	live, _, err := t.memory.CountMemoryNotes(ctx, workspaceID)
	if err != nil {
		return nil, memoryCompactOutput{}, err
	}
	out := memoryCompactOutput{Live: live}
	if len(batch) == 0 {
		out.Hint = "There is nothing to compact."
		return nil, out, nil
	}
	if len(batch) == 1 && live == 1 {
		out.Hint = "One note does not need compacting."
		return nil, out, nil
	}
	out.Notes = make([]store.MemoryNote, 0, len(batch))
	for _, n := range batch {
		out.Notes = append(out.Notes, *n)
	}
	out.ThroughID = batch[len(batch)-1].ID
	out.Hint = fmt.Sprintf("Summarize these %d notes in up to %d bytes — decisions, outcomes, what a later conversation would otherwise rediscover — then call memory_compact again with summary and through_id=%d.", len(batch), memorySummaryBytes, out.ThroughID)
	return nil, out, nil
}

// compactDue is what recall adds when the trail has grown long.
func compactDue(live int) string {
	if live <= memoryCompactAt {
		return ""
	}
	return fmt.Sprintf(" The trail has %d notes; call memory_compact to fold the oldest into a summary.", live)
}

// summaryDate is the day a batch starts, for the summary note's title.
func summaryDate(n *store.MemoryNote) string { return n.CreatedAt.Format(time.DateOnly) }
