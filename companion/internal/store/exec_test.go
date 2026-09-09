package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAppendAndListExecEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ws := testWorkspace("ws-exec")
	if err := s.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	for _, e := range []*ExecEvent{
		{WorkspaceID: ws.ID, DirRelative: "packages/web", Argv: []string{"npm", "test"},
			Outcome: "ok", DurationMS: 1200},
		{WorkspaceID: ws.ID, Argv: []string{"rm", "-rf", "/etc"},
			Outcome: "refused", RuleID: "delete-outside-workspace", Reason: "deletes a path outside the workspace"},
	} {
		if err := s.AppendExecEvent(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
		if e.ID == 0 {
			t.Fatal("row id was not written back")
		}
		if e.CreatedAt.IsZero() {
			t.Fatal("created_at was not defaulted")
		}
	}

	got, err := s.ListExecEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	// Newest first: the desktop history view reads top-down.
	if got[0].Outcome != "refused" {
		t.Fatalf("got %q first, want the refusal", got[0].Outcome)
	}
	// A refused attempt is a record, not a gap — it is the more interesting
	// security signal, not the less.
	if got[0].RuleID != "delete-outside-workspace" {
		t.Fatalf("rule id was not preserved: %q", got[0].RuleID)
	}
	if len(got[1].Argv) != 2 || got[1].Argv[0] != "npm" || got[1].Argv[1] != "test" {
		t.Fatalf("argv round-trip failed: %v", got[1].Argv)
	}
	if got[1].DirRelative != "packages/web" || got[1].DurationMS != 1200 {
		t.Fatalf("unexpected row %+v", got[1])
	}
}

func TestExecEventsRejectIncompleteRecords(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.AppendExecEvent(ctx, &ExecEvent{Outcome: "ok"}); err == nil {
		t.Fatal("an event with no argv was accepted")
	}
	if err := s.AppendExecEvent(ctx, &ExecEvent{Argv: []string{"ls"}}); err == nil {
		t.Fatal("an event with no outcome was accepted")
	}
}

func TestExecEventsAcceptNoWorkspace(t *testing.T) {
	// A command refused before a workspace could be resolved still has to be
	// recorded; a foreign key that forced one would drop exactly the attempts
	// worth keeping.
	s := openTestStore(t)
	e := &ExecEvent{Argv: []string{"sudo", "rm"}, Outcome: "refused", RuleID: "privilege-escalation"}
	if err := s.AppendExecEvent(context.Background(), e); err != nil {
		t.Fatalf("append without a workspace: %v", err)
	}
	got, err := s.ListExecEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].WorkspaceID != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestExecEventsStoreNoAbsolutePaths(t *testing.T) {
	// The schema has no column for one, and the writer must not smuggle one
	// into the ones it does have.
	s := openTestStore(t)
	ctx := context.Background()
	e := &ExecEvent{
		DirRelative: "packages/web",
		Argv:        []string{"go", "test", "./..."},
		Outcome:     "ok",
		CreatedAt:   time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := s.AppendExecEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT dir_relative, argv, reason FROM exec_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var dir, argv, reason string
		if err := rows.Scan(&dir, &argv, &reason); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{dir, argv, reason} {
			if strings.HasPrefix(v, "/") || strings.Contains(v, ":\\") {
				t.Fatalf("an absolute path reached the database: %q", v)
			}
		}
	}
}
