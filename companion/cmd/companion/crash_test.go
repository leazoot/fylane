package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// A real process, killed for real.
//
// database.md has required since it was written that "crash recovery must
// clean up incomplete transactions", and txn.Recover implements it. What was
// never shown is the half in between: that a Companion killed mid-transaction
// actually leaves the state Recover expects to find. The existing test
// fabricates that state and then cleans it, which proves the cleaner and
// assumes the crash.
//
// So this one builds the binary, starts it, gets a change set as far as
// waiting for approval, sends SIGKILL — no defers, no shutdown hook, nothing
// the process can do about it — reads the database to confirm the transaction
// really was left open, then starts the binary again and reads the database
// once more.

func buildCompanion(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// SIGKILL has no Windows equivalent that leaves the same evidence,
		// and this test is about what an un-cleaned exit leaves behind.
		t.Skip("the crash is sent as SIGKILL")
	}
	bin := filepath.Join(t.TempDir(), "fylane-companion")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the companion binary: %v", err)
	}
	return bin
}

// freePort takes a port and gives it back. A bound port cannot be handed to
// a child, so there is a window; it is the same window every server test in
// this repository lives with.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// serveCompanion starts the binary and waits until it answers.
func serveCompanion(t *testing.T, bin, dataDir, workspace, addr string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin, "serve", "-workspace", workspace, "-addr", addr, "-data-dir", dataDir)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the companion: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the companion never started listening on %s", addr)
	return nil
}

// callTool posts one MCP tool call. Loopback needs no authentication, which
// is what makes this reachable from a test at all.
func callTool(ctx context.Context, addr, name, args string) error {
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		name, args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// changeSetStatus reads the row the engine wrote, from outside the process
// that wrote it. WAL is on, so this works while the Companion is running and
// after it has been killed.
func changeSetStatus(t *testing.T, dataDir string) (id, status string, ok bool) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer db.Close()
	row := db.QueryRow(`SELECT id, status FROM change_sets ORDER BY created_at DESC LIMIT 1`)
	switch err := row.Scan(&id, &status); err {
	case nil:
		return id, status, true
	case sql.ErrNoRows:
		return "", "", false
	default:
		// The table may not exist yet on the very first poll.
		return "", "", false
	}
}

func TestAKilledCompanionLeavesAnOpenTransactionAndTheNextStartClosesIt(t *testing.T) {
	bin := buildCompanion(t)
	dataDir, workspace, addr := t.TempDir(), t.TempDir(), freePort(t)

	cmd := serveCompanion(t, bin, dataDir, workspace, addr)

	// A write in the default (safe) mode needs a local human, and there is
	// no desktop here, so the call blocks and the change set sits in the
	// database exactly as it would while a real user was deciding.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go callTool(ctx, addr, "write_file", `{"path":"notes.md","content":"hello\n"}`)

	var id string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if got, status, ok := changeSetStatus(t, dataDir); ok && status == "pending" {
			id = got
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("no change set reached the database while the write waited for approval")
	}

	// The crash. Not Signal(os.Interrupt), not Close() — the process gets no
	// chance to tidy up, which is the only way this proves anything.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the companion: %v", err)
	}
	cmd.Wait()
	cancel()

	gotID, status, ok := changeSetStatus(t, dataDir)
	if !ok || gotID != id {
		t.Fatalf("after the kill the change set is %q/%v, want %q", gotID, ok, id)
	}
	if status != "pending" {
		t.Fatalf("the killed process left status %q; this test assumes the crash "+
			"leaves an open transaction, and it did not", status)
	}

	// Restart. Nothing else touches the database in between.
	addr2 := freePort(t)
	serveCompanion(t, bin, dataDir, workspace, addr2)

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, status, ok = changeSetStatus(t, dataDir); ok && status != "pending" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "failed" {
		t.Errorf("after restarting, the interrupted change set is %q, want %q — "+
			"database.md requires the next start to clean it up", status, "failed")
	}

	db, err := sql.Open("sqlite", filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM audit_events WHERE change_set_id = ? AND event_type = 'change_set_recovered'`,
		id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		// A row quietly changed from pending to failed is indistinguishable
		// from a user's rejection. The audit entry is what makes the cleanup
		// legible afterwards.
		t.Errorf("recovery audit rows = %d, want 1", n)
	}

	// Exactly once, across the crash. The write was never approved, so the
	// file must not exist — and retrying the same change_set_id, which is
	// what a platform does when a call comes back pending, must not apply
	// it now that the transaction has been closed as failed.
	if _, err := os.Stat(filepath.Join(workspace, "notes.md")); err == nil {
		t.Fatal("the file exists; a change set nobody approved was applied across the crash")
	}
	if err := callTool(context.Background(), addr2, "write_file",
		fmt.Sprintf(`{"path":"notes.md","content":"hello\n","change_set_id":%q}`, id)); err != nil {
		t.Fatalf("retrying the change set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "notes.md")); err == nil {
		t.Error("retrying the recovered change_set_id applied it; a change set " +
			"closed by crash recovery must have to be requested again")
	}
}

// control talks to the loopback control API — the surface the desktop uses.
// A headless test needs it because the one thing this harness cannot supply
// is a person clicking approve.
type control struct {
	addr, token string
}

func openControl(t *testing.T, dataDir string) control {
	t.Helper()
	var c struct {
		Addr  string `json:"addr"`
		Token string `json:"token"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(dataDir, "control.json"))
		if err == nil && json.Unmarshal(data, &c) == nil && c.Addr != "" {
			return control{addr: c.Addr, token: c.Token}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the companion never wrote control.json")
	return control{}
}

func (c control) post(t *testing.T, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+c.addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// approveWhenAsked answers every approval the Companion raises, until the
// test stops it. It polls because there is no push channel a test can hold.
func (c control) approveWhenAsked(t *testing.T, stop <-chan struct{}) {
	t.Helper()
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			req, err := http.NewRequest(http.MethodGet, "http://"+c.addr+"/v1/approvals", nil)
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+c.token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			var pending struct {
				Approvals []struct {
					ChangeSetID string `json:"change_set_id"`
				} `json:"approvals"`
			}
			json.NewDecoder(resp.Body).Decode(&pending)
			resp.Body.Close()
			for _, p := range pending.Approvals {
				if id := p.ChangeSetID; id != "" {
					c.post(t, "/v1/approvals/resolve",
						fmt.Sprintf(`{"change_set_id":%q,"approved":true}`, id))
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
}

func TestACommandKilledWithTheCompanionIsAnsweredOnTheNextStart(t *testing.T) {
	// The other half of the same requirement, and the one reconcileRuns was
	// written for: a child process dies with its parent, so the execution
	// engine's call never returns and never writes an outcome. Until the
	// next start says so, a command that really ran and really may have
	// written to disk leaves the task screen showing nothing at all.
	bin := buildCompanion(t)
	dataDir, workspace, addr := t.TempDir(), t.TempDir(), freePort(t)

	cmd := serveCompanion(t, bin, dataDir, workspace, addr)
	ctl := openControl(t, dataDir)
	stop := make(chan struct{})
	ctl.approveWhenAsked(t, stop)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go callTool(ctx, addr, "run_command",
		// Long enough that the kill lands mid-run, short enough that the
		// orphan it leaves behind is gone before anyone notices. It does
		// outlive the Companion: see the note on reconcileRuns.
		`{"command":["sleep","30"],"timeout_seconds":300}`)

	// Wait until the command is actually running, not merely requested: the
	// 'started' row is what a crash is supposed to leave dangling.
	runID := waitForStartedRun(t, dataDir)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the companion: %v", err)
	}
	cmd.Wait()
	close(stop)
	cancel()

	if outcomes := runOutcomes(t, dataDir, runID); len(outcomes) != 1 || outcomes[0] != "started" {
		t.Fatalf("after the kill the run has outcomes %v; this test assumes the "+
			"crash leaves the command unanswered, and it did not", outcomes)
	}

	restarted := serveCompanion(t, bin, dataDir, workspace, freePort(t))

	deadline := time.Now().Add(10 * time.Second)
	var outcomes []string
	for time.Now().Before(deadline) {
		outcomes = runOutcomes(t, dataDir, runID)
		if len(outcomes) > 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(outcomes) != 2 || outcomes[1] != "interrupted" {
		t.Fatalf("run outcomes after restarting = %v, want the started row "+
			"answered with 'interrupted'", outcomes)
	}

	// Append-only: the orphan is answered, not edited. A second start must
	// find nothing left to answer. This one exits normally — only the first
	// stop is a crash, and the instance lock will not let two run at once.
	restarted.Process.Signal(os.Interrupt)
	restarted.Wait()
	serveCompanion(t, bin, dataDir, workspace, freePort(t))
	time.Sleep(time.Second)
	if got := runOutcomes(t, dataDir, runID); len(got) != 2 {
		t.Errorf("a second restart wrote more rows: %v — the answer is terminal "+
			"and must be found only once", got)
	}
}

func waitForStartedRun(t *testing.T, dataDir string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		db, err := sql.Open("sqlite", filepath.Join(dataDir, "fylane.db"))
		if err == nil {
			var runID string
			err = db.QueryRow(
				`SELECT run_id FROM exec_events WHERE outcome = 'started' ORDER BY id DESC LIMIT 1`).Scan(&runID)
			db.Close()
			if err == nil && runID != "" {
				return runID
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no command ever reached the audit log as started")
	return ""
}

func runOutcomes(t *testing.T, dataDir, runID string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT outcome FROM exec_events WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return out
		}
		out = append(out, o)
	}
	return out
}
