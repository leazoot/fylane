package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
)

func memoryFixture(t *testing.T) (*toolset, *store.Store, string) {
	t.Helper()
	src, st := testSource(t, t.TempDir())
	rec, err := src.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &toolset{src: src, provider: "claude", activityLog: st, memory: st, runs: st}, st, rec.ID
}

func TestMemoryStartsEmptyAndIsAnnouncedByWorkspaceInfoOnceWritten(t *testing.T) {
	tools, _, _ := memoryFixture(t)
	ctx := context.Background()
	_, recall, err := tools.memoryRecall(ctx, nil, memoryRecallInput{})
	if err != nil {
		t.Fatal(err)
	}
	if recall.Page != nil || recall.NoteCount != 0 || len(recall.Notes) != 0 || !strings.Contains(recall.Hint, "Nothing") {
		t.Fatalf("fresh recall = %+v", recall)
	}
	_, info, _ := tools.workspaceInfo(ctx, nil, workspaceInfoInput{})
	if info.Memory != nil {
		t.Fatalf("workspace_info announces memory that does not exist: %+v", info.Memory)
	}

	long := strings.Repeat("g", store.MemoryGoalBytes+50)
	_, wrote, err := tools.memoryNote(ctx, nil, memoryNoteInput{
		Title: "  auth done  ", Body: "JWT with refresh rotation; see change set later",
		Page: &store.MemoryPage{Goal: long, Next: "billing", Decisions: make([]string, 12), Open: []string{" pricing ", ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wrote.NoteID == 0 || !wrote.PageUpdated || wrote.Notes != 1 {
		t.Fatalf("note = %+v", wrote)
	}
	if got := strings.Join(wrote.Cut, ","); got != "page.goal" {
		t.Fatalf("cut = %q (empty list items are dropped, not cut)", got)
	}

	_, recall, err = tools.memoryRecall(ctx, nil, memoryRecallInput{})
	if err != nil {
		t.Fatal(err)
	}
	if recall.Page == nil || len(recall.Page.Goal) != store.MemoryGoalBytes || recall.Page.Next != "billing" || len(recall.Page.Open) != 1 || recall.Page.Open[0] != "pricing" {
		t.Fatalf("page = %+v", recall.Page)
	}
	if len(recall.Notes) != 1 || recall.Notes[0].Title != "auth done" || recall.UpdatedBy != "claude" || recall.NoteCount != 1 {
		t.Fatalf("recall = %+v", recall)
	}

	_, info, _ = tools.workspaceInfo(ctx, nil, workspaceInfoInput{})
	if info.Memory == nil || info.Memory.Notes != 1 || len(info.Memory.Goal) != memoryHintBytes || info.Memory.Next != "billing" || !strings.Contains(info.Memory.Hint, "memory_recall") {
		t.Fatalf("workspace_info memory = %+v", info.Memory)
	}
}

func TestMemoryNoteChecksWhatItPointsAt(t *testing.T) {
	tools, st, ws := memoryFixture(t)
	ctx := context.Background()
	if err := st.CreateChangeSet(ctx, &store.ChangeSet{ID: "cs_here", WorkspaceID: ws, Operations: json.RawMessage(`[{}]`), Status: txn.StatusApplied}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "ws_other", Name: "o", RootPath: t.TempDir(), Mode: store.ModeReadWrite, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateChangeSet(ctx, &store.ChangeSet{ID: "cs_there", WorkspaceID: "ws_other", Operations: json.RawMessage(`[{}]`), Status: txn.StatusApplied}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendExecEvent(ctx, &store.ExecEvent{WorkspaceID: ws, Argv: []string{"go", "test"}, Outcome: "ok", RunID: "run_here"}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []memoryNoteInput{
		{Title: "x", ChangeSetID: "cs_nowhere"},
		{Title: "x", ChangeSetID: "cs_there"},
		{Title: "x", RunID: "run_nowhere"},
		{},
	} {
		if _, _, err := tools.memoryNote(ctx, nil, in); err == nil {
			t.Errorf("%+v was accepted", in)
		}
	}
	_, out, err := tools.memoryNote(ctx, nil, memoryNoteInput{Title: "tests pass after the auth change", ChangeSetID: "cs_here", RunID: "run_here"})
	if err != nil {
		t.Fatal(err)
	}
	_, read, _ := tools.memoryRead(ctx, nil, memoryReadInput{IDs: []int64{out.NoteID}})
	if len(read.Notes) != 1 || read.Notes[0].ChangeSetID != "cs_here" || read.Notes[0].RunID != "run_here" {
		t.Fatalf("read = %+v", read)
	}
}

func TestMemoryIsSearchedAndPagedWithinTheBudget(t *testing.T) {
	tools, _, _ := memoryFixture(t)
	ctx := context.Background()
	for i := 1; i <= 7; i++ {
		body := fmt.Sprintf("note %d %s", i, strings.Repeat("filler ", 250))
		if i == 4 {
			body = "the Login handler now rotates refresh tokens " + strings.Repeat("filler ", 250)
		}
		if _, _, err := tools.memoryNote(ctx, nil, memoryNoteInput{Title: fmt.Sprintf("step %d", i), Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	_, found, err := tools.memorySearch(ctx, nil, memorySearchInput{Query: "refresh login"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Matches) != 1 || found.Matches[0].Title != "step 4" || !strings.Contains(found.Matches[0].Snippet, "refresh") || len(found.Matches[0].Snippet) > memorySnippetBytes+6 {
		t.Fatalf("search = %+v", found)
	}
	if _, _, err := tools.memorySearch(ctx, nil, memorySearchInput{Query: "   "}); err == nil {
		t.Error("an empty query was accepted")
	}

	// Bodies are ~1.8 KB each; a 4 KB budget fits two per page.
	tools.inlineBudget = 4000
	_, page, err := tools.memoryRead(ctx, nil, memoryReadInput{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Notes) != 2 || page.Notes[0].Title != "step 7" || page.NextBeforeID != page.Notes[1].ID {
		t.Fatalf("first page = %d notes, next %d", len(page.Notes), page.NextBeforeID)
	}
	seen := len(page.Notes)
	for page.NextBeforeID != 0 {
		_, page, err = tools.memoryRead(ctx, nil, memoryReadInput{Limit: 5, BeforeID: page.NextBeforeID})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page.Notes)
		if seen > 7 {
			t.Fatal("paging does not end")
		}
	}
	if seen != 7 {
		t.Fatalf("paged through %d notes, want 7", seen)
	}
	_, recall, _ := tools.memoryRecall(ctx, nil, memoryRecallInput{Limit: 3})
	if len(recall.Notes) != 3 || recall.NoteCount != 7 {
		t.Fatalf("recall = %d listed of %d", len(recall.Notes), recall.NoteCount)
	}
	raw, _ := json.Marshal(recall)
	if len(raw) > 1500 {
		t.Fatalf("recall with 7 notes is %d bytes; titles only was the point", len(raw))
	}
	if _, _, err := tools.memoryRead(ctx, nil, memoryReadInput{IDs: make([]int64, memoryReadIDs+1)}); err == nil {
		t.Error("too many ids were accepted")
	}
}
