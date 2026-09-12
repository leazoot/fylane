package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)

func TestMemoryPageIsRewrittenAndBounded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateWorkspace(ctx, testWorkspace("ws-m")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMemoryState(ctx, "ws-m"); err != ErrNotFound {
		t.Fatalf("fresh workspace: %v", err)
	}
	page := MemoryPage{Goal: "ship v1", Progress: "auth done", Next: "billing", Decisions: []string{"sqlite"}}
	if err := s.SaveMemoryState(ctx, "ws-m", "claude", page, testTime); err != nil {
		t.Fatal(err)
	}
	page.Progress = "auth and billing done"
	page.Decisions = nil
	if err := s.SaveMemoryState(ctx, "ws-m", "chatgpt", page, testTime); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMemoryState(ctx, "ws-m")
	if err != nil {
		t.Fatal(err)
	}
	if got.Page.Progress != "auth and billing done" || len(got.Page.Decisions) != 0 || got.Provider != "chatgpt" || !got.UpdatedAt.Equal(testTime) {
		t.Fatalf("page after rewrite = %+v", got)
	}
	for _, bad := range []MemoryPage{
		{Goal: strings.Repeat("g", MemoryGoalBytes+1)},
		{Decisions: make([]string, MemoryListItems+1)},
		{Open: []string{strings.Repeat("o", MemoryListItemBytes+1)}},
		{Next: "\xff"},
	} {
		if err := s.SaveMemoryState(ctx, "ws-m", "", bad, testTime); err == nil {
			t.Errorf("page %+v was accepted", bad)
		}
	}
}

func TestMemoryNotesArePagedSearchedAndScopedToTheirWorkspace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"ws-a", "ws-b"} {
		if err := s.CreateWorkspace(ctx, testWorkspace(id)); err != nil {
			t.Fatal(err)
		}
	}
	for i, title := range []string{"login form", "billing page", "login bug fixed", "100% coverage"} {
		n := &MemoryNote{WorkspaceID: "ws-a", Provider: "claude", Title: title, Body: "body " + title}
		if i == 1 {
			n.WorkspaceID = "ws-b"
		}
		if err := s.AddMemoryNote(ctx, n); err != nil {
			t.Fatal(err)
		}
		if n.ID == 0 || n.CreatedAt.IsZero() {
			t.Fatalf("note %q: id %d created %s", title, n.ID, n.CreatedAt)
		}
	}
	page, err := s.ListMemoryNotes(ctx, "ws-a", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Title != "100% coverage" || page[1].Title != "login bug fixed" {
		t.Fatalf("first page = %v", titles(page))
	}
	rest, _ := s.ListMemoryNotes(ctx, "ws-a", page[1].ID, 10)
	if len(rest) != 1 || rest[0].Title != "login form" {
		t.Fatalf("second page = %v", titles(rest))
	}
	found, err := s.SearchMemoryNotes(ctx, "ws-a", "LOGIN bug", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Title != "login bug fixed" {
		t.Fatalf("search = %v", titles(found))
	}
	if found, _ := s.SearchMemoryNotes(ctx, "ws-a", "100%", 10); len(found) != 1 {
		t.Fatalf("a literal %% was read as a wildcard: %v", titles(found))
	}
	if found, _ := s.SearchMemoryNotes(ctx, "ws-a", "billing", 10); len(found) != 0 {
		t.Fatalf("a note of another workspace was found: %v", titles(found))
	}
	other, _ := s.ListMemoryNotes(ctx, "ws-b", 0, 10)
	if got, _ := s.GetMemoryNotes(ctx, "ws-a", []int64{other[0].ID, page[0].ID}); len(got) != 1 || got[0].ID != page[0].ID {
		t.Fatalf("notes by id crossed workspaces: %v", titles(got))
	}
	if live, archived, _ := s.CountMemoryNotes(ctx, "ws-a"); live != 3 || archived != 0 {
		t.Fatalf("count = %d live, %d archived", live, archived)
	}
	if err := s.AddMemoryNote(ctx, &MemoryNote{WorkspaceID: "ws-a", Title: "x", Body: strings.Repeat("b", MemoryBodyBytes+1)}); err == nil {
		t.Fatal("an oversized body was accepted")
	}
	if err := s.DeleteMemory(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	if live, _, _ := s.CountMemoryNotes(ctx, "ws-a"); live != 0 {
		t.Fatal("memory survived deletion")
	}
	if other, _ := s.ListMemoryNotes(ctx, "ws-b", 0, 10); len(other) != 1 {
		t.Fatal("deleting one workspace's memory took another's")
	}
}

func titles(notes []*MemoryNote) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Title)
	}
	return out
}

func TestMemoryNotesAreArchivedInOrderAndStayFindable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateWorkspace(ctx, testWorkspace("ws-c")); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, title := range []string{"one", "two", "three"} {
		n := &MemoryNote{WorkspaceID: "ws-c", Title: title, Body: "b"}
		if err := s.AddMemoryNote(ctx, n); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, n.ID)
	}
	oldest, _ := s.OldestMemoryNotes(ctx, "ws-c", 2)
	if len(oldest) != 2 || oldest[0].Title != "one" || oldest[1].Title != "two" {
		t.Fatalf("oldest = %v", titles(oldest))
	}
	if n, err := s.ArchiveMemoryNotes(ctx, "ws-c", ids[1]); err != nil || n != 2 {
		t.Fatalf("archived %d, %v", n, err)
	}
	if live, archived, _ := s.CountMemoryNotes(ctx, "ws-c"); live != 1 || archived != 2 {
		t.Fatalf("count = %d live, %d archived", live, archived)
	}
	if page, _ := s.ListMemoryNotes(ctx, "ws-c", 0, 10); len(page) != 1 || page[0].Title != "three" {
		t.Fatalf("listing shows archived notes: %v", titles(page))
	}
	if found, _ := s.SearchMemoryNotes(ctx, "ws-c", "one", 10); len(found) != 1 || !found[0].Archived {
		t.Fatalf("an archived note is not searchable: %v", titles(found))
	}
	if got, _ := s.GetMemoryNotes(ctx, "ws-c", ids[:1]); len(got) != 1 {
		t.Fatal("an archived note is not readable by id")
	}
	if n, _ := s.ArchiveMemoryNotes(ctx, "ws-c", ids[1]); n != 0 {
		t.Fatal("archiving is not idempotent")
	}
}
