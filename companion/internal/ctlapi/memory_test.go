package ctlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
)

func TestMemoryEndpointsReadCorrectExportAndForget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ws, err := f.manager.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Empty: a document with nothing in it, never a 404.
	resp, raw := f.call(t, "GET", "/v1/memory", f.token, nil)
	var doc memoryDoc
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &doc) != nil || doc.State != nil || len(doc.Notes) != 0 {
		t.Fatalf("fresh memory = %d %s", resp.StatusCode, raw)
	}

	// The platform writes; the desktop reads it back with counts and paging.
	page := store.MemoryPage{Goal: "ship idle sync", Progress: "layer done", Decisions: []string{"imap idle"}, Open: []string{"icloud heartbeat?"}}
	if err := f.st.SaveMemoryState(ctx, ws.ID, "chatgpt", page, time.Now()); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < memoryPageSize+2; i++ {
		n := &store.MemoryNote{WorkspaceID: ws.ID, Provider: "chatgpt", Title: "step " + strings.Repeat("x", i%3) + string(rune('a'+i%26)), Body: "body"}
		if err := f.st.AddMemoryNote(ctx, n); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, n.ID)
	}
	if _, err := f.st.ArchiveMemoryNotes(ctx, ws.ID, ids[1]); err != nil {
		t.Fatal(err)
	}
	resp, raw = f.call(t, "GET", "/v1/memory?workspace_id="+ws.ID, f.token, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("memory = %d %s", resp.StatusCode, raw)
	}
	if doc.State == nil || doc.State.Page.Goal != "ship idle sync" || doc.State.Provider != "chatgpt" {
		t.Fatalf("state = %+v", doc.State)
	}
	if doc.Live != memoryPageSize || doc.Archived != 2 || len(doc.Notes) != memoryPageSize || doc.NextBeforeID != 0 {
		t.Fatalf("first page: live %d archived %d notes %d next %d", doc.Live, doc.Archived, len(doc.Notes), doc.NextBeforeID)
	}
	if doc.Notes[0].ID != ids[len(ids)-1] {
		t.Fatalf("listing is not newest first: %d", doc.Notes[0].ID)
	}
	// Fresh documents per answer: decoding into a reused one would keep an
	// omitted false "archived" from the previous page.
	var past memoryDoc
	resp, raw = f.call(t, "GET", "/v1/memory?workspace_id="+ws.ID+"&archived=1", f.token, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &past) != nil || len(past.Notes) != 2 || !past.Notes[0].Archived {
		t.Fatalf("archived page = %d %s", resp.StatusCode, raw)
	}
	var found memoryDoc
	resp, raw = f.call(t, "GET", "/v1/memory?workspace_id="+ws.ID+"&q=step", f.token, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &found) != nil || len(found.Notes) != memoryPageSize || found.Notes[0].Archived {
		t.Fatalf("search = %d %s", resp.StatusCode, raw)
	}
	resp, raw = f.call(t, "GET", "/v1/memory?workspace_id=ws_nope", f.token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown workspace = %d %s", resp.StatusCode, raw)
	}

	// The user corrects the page by hand: it is credited to them, and an
	// over-long field is refused rather than cut.
	page.Progress = "layer done, poller still running"
	resp, raw = f.call(t, "POST", "/v1/memory/page", f.token, map[string]any{"workspace_id": ws.ID, "page": page})
	var state store.MemoryState
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &state) != nil || state.Provider != memoryUserProvider || state.Page.Progress != page.Progress {
		t.Fatalf("page save = %d %s", resp.StatusCode, raw)
	}
	long := page
	long.Goal = strings.Repeat("g", store.MemoryGoalBytes+1)
	resp, raw = f.call(t, "POST", "/v1/memory/page", f.token, map[string]any{"workspace_id": ws.ID, "page": long})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "limit") {
		t.Fatalf("over-long page = %d %s", resp.StatusCode, raw)
	}

	// One note goes; a second try says it is gone.
	resp, raw = f.call(t, "POST", "/v1/memory/notes/delete", f.token, map[string]any{"workspace_id": ws.ID, "id": ids[5]})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d %s", resp.StatusCode, raw)
	}
	resp, _ = f.call(t, "POST", "/v1/memory/notes/delete", f.token, map[string]any{"workspace_id": ws.ID, "id": ids[5]})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d", resp.StatusCode)
	}

	// Export carries the page, both halves of the trail, and no absolute path.
	resp, raw = f.call(t, "GET", "/v1/memory/export?workspace_id="+ws.ID, f.token, nil)
	var out memoryExport
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil {
		t.Fatalf("export = %d %s", resp.StatusCode, raw)
	}
	if !strings.HasSuffix(out.Filename, ".md") || strings.ContainsAny(out.Filename, "/ ") {
		t.Fatalf("filename = %q", out.Filename)
	}
	for _, want := range []string{"# Memory: ", "### Goal\n\nship idle sync", "- imap idle", "### Open questions", "## Notes\n", "## Archived notes\n", "_", "poller still running"} {
		if !strings.Contains(out.Markdown, want) {
			t.Errorf("export lacks %q:\n%s", want, out.Markdown)
		}
	}
	if strings.Contains(out.Markdown, f.root) {
		t.Fatal("export names the root path")
	}

	// Forgetting is total.
	resp, raw = f.call(t, "POST", "/v1/memory/clear", f.token, map[string]any{"workspace_id": ws.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear = %d %s", resp.StatusCode, raw)
	}
	var gone memoryDoc
	resp, raw = f.call(t, "GET", "/v1/memory?workspace_id="+ws.ID+"&archived=1", f.token, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &gone) != nil || gone.State != nil || gone.Live+gone.Archived != 0 || len(gone.Notes) != 0 {
		t.Fatalf("after clear = %d %s", resp.StatusCode, raw)
	}
}

func TestExportFilenameIsSafeToSave(t *testing.T) {
	at := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"InboxKit":     "InboxKit-memory-2026-09-12.md",
		"my project/2": "my-project-2-memory-2026-09-12.md",
		`a<b>:"c"|?*`:  "ab-c-memory-2026-09-12.md",
		"   ":          "workspace-memory-2026-09-12.md",
		"..":           "workspace-memory-2026-09-12.md",
		"收件箱 同步":       "收件箱-同步-memory-2026-09-12.md",
	} {
		if got := exportFilename(in, at); got != want {
			t.Errorf("exportFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
