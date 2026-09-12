package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
)

func TestWorkspaceInfoSaysNothingForAFreshWorkspace(t *testing.T) {
	root := t.TempDir()
	src, st := testSource(t, root)
	tools := &toolset{src: src, provider: "claude", activityLog: st}
	_, out, err := tools.workspaceInfo(context.Background(), nil, workspaceInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.LastActivity != nil {
		t.Fatalf("a fresh workspace has activity: %+v", out.LastActivity)
	}
}

func TestWorkspaceInfoSaysWhereTheLastSessionLeftOff(t *testing.T) {
	root := t.TempDir()
	src, st := testSource(t, root)
	ctx := context.Background()
	rec, err := src.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	activityNow = func() time.Time { return base.Add(3 * time.Hour) }
	t.Cleanup(func() { activityNow = time.Now })

	// Twelve changes, the newest touching a sensitive file, one rolled
	// back, one still waiting; two commands, the last one failed.
	for i := 0; i < 12; i++ {
		c := &store.ChangeSet{ID: fmt.Sprintf("cs_%02d", i), WorkspaceID: rec.ID, Provider: "chatgpt",
			Summary:    fmt.Sprintf("change %d %s", i, strings.Repeat("x", 200)),
			Operations: json.RawMessage(`[{"op":"write"}]`), Status: txn.StatusApplied,
			AfterHashes: map[string]string{fmt.Sprintf("src/f%d.go", i): "h"},
			CreatedAt:   base.Add(time.Duration(i) * time.Minute)}
		switch i {
		case 3:
			c.Status = txn.StatusRolledBack
		case 7:
			c.Status = txn.StatusPending
		case 11:
			c.AfterHashes = map[string]string{"src/app.go": "h", ".env": "h", "a": "h", "b": "h", "c": "h", "d": "h", "e": "h"}
		}
		if err := st.CreateChangeSet(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []*store.ExecEvent{
		{WorkspaceID: rec.ID, Argv: []string{"go", "test", "./..."}, Outcome: "ok", Provider: "claude", CreatedAt: base.Add(5 * time.Minute)},
		{WorkspaceID: rec.ID, Argv: []string{"go", "vet"}, Outcome: "failed", ExitCode: 2, Provider: "claude", CreatedAt: base.Add(9 * time.Minute)},
	} {
		if err := st.AppendExecEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	tools := &toolset{src: src, provider: "claude", activityLog: st}
	_, out, err := tools.workspaceInfo(ctx, nil, workspaceInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	act := out.LastActivity
	if act == nil {
		t.Fatal("no last activity")
	}
	if act.Ago != "2 hours" || !act.At.Equal(base.Add(11*time.Minute)) {
		t.Errorf("ago = %q at %s", act.Ago, act.At)
	}
	want := "Last activity 2 hours ago (chatgpt). Recent changes: 8 applied, 1 rolled back, 1 awaiting approval. Last command `go vet` failed (exit 2)."
	if act.Summary != want {
		t.Errorf("summary =\n%s\nwant\n%s", act.Summary, want)
	}
	if len(act.Recent) != activityEntries {
		t.Fatalf("recent has %d entries, want %d", len(act.Recent), activityEntries)
	}
	newest := act.Recent[0]
	if newest.Kind != "change" || len(newest.Summary) > activitySummaryBytes || !strings.HasPrefix(newest.Summary, "change 11") {
		t.Errorf("newest = %+v", newest)
	}
	paths := strings.Join(newest.Paths, ",")
	if strings.Contains(paths, ".env") || !strings.Contains(paths, "(1 sensitive file(s))") || !strings.Contains(paths, "(+1 more)") || len(newest.Paths) != activityPaths+2 {
		t.Errorf("paths = %v", newest.Paths)
	}
	if act.Recent[3].Kind != "command" || act.Recent[3].Status != "failed" {
		t.Errorf("commands are not interleaved by time: %+v", act.Recent)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), root) {
		t.Fatalf("an absolute path leaked: %s", raw)
	}
	if len(raw) > 4096 {
		t.Fatalf("the answer is %d bytes; it must stay small", len(raw))
	}
}
