package main

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// fakeCore writes a script that stands in for the Core binary and points
// FYLANE_COMPANION_BIN at it.
func fakeCore(t *testing.T, body string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("the stand-in Core is a shell script")
	}
	path := filepath.Join(t.TempDir(), "fylane-companion")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYLANE_COMPANION_BIN", path)
}

// The failure this is about: something else is on the Core's port, the Core
// says so on stderr and exits, and the shell reported "starting" anyway. The
// user was left with an offline banner that never explained itself, on a
// machine where the explanation had already been written down.
func TestAnImmediateCoreFailureIsReported(t *testing.T) {
	fakeCore(t, `echo "fylane-companion: listening on 127.0.0.1:8787: bind: address already in use" >&2
exit 1
`)
	a := &App{dataDir: t.TempDir()}

	status, err := a.StartCore()
	if err == nil {
		t.Fatalf("StartCore = %q, nil; want the failure reported", status)
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("error %q does not carry what the core said", err)
	}
	if status != "" {
		t.Errorf("a failed start still reported %q", status)
	}
}

// A Core that dies without saying anything still has to produce something
// better than silence.
func TestACoreThatDiesSilentlyStillReportsSomething(t *testing.T) {
	fakeCore(t, "exit 3\n")
	a := &App{dataDir: t.TempDir()}

	_, err := a.StartCore()
	if err == nil {
		t.Fatal("a core that exited 3 was reported as started")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error %q does not carry the exit status", err)
	}
}

// The watch window must not turn into a readiness check: a Core that is up
// and serving has not finished starting in two seconds, and waiting for that
// here would make every launch feel broken.
func TestACoreThatKeepsRunningIsReportedAsStarting(t *testing.T) {
	fakeCore(t, "sleep 6\n")
	a := &App{dataDir: t.TempDir()}

	start := time.Now()
	status, err := a.StartCore()
	if err != nil {
		t.Fatalf("StartCore: %v", err)
	}
	if status != "starting" {
		t.Errorf("status = %q, want %q", status, "starting")
	}
	if elapsed := time.Since(start); elapsed > startWatch+3*time.Second {
		t.Errorf("StartCore waited %v; it must not wait for readiness", elapsed)
	}
}

func TestStartFailurePrefersWhatTheCoreSaid(t *testing.T) {
	exit := errors.New("exit status 1")
	if got := startFailure(exit, "info: starting\nfatal: the port is taken\n"); got != "fatal: the port is taken" {
		t.Errorf("startFailure = %q, want the last line of stderr", got)
	}
	if got := startFailure(exit, "   \n\n"); got != exit.Error() {
		t.Errorf("startFailure with no output = %q, want the exit status", got)
	}
	if got := startFailure(nil, ""); got == "" {
		t.Error("startFailure returned nothing at all")
	}
}

// The tail, not the head: a program explains itself on the way out, so the
// bytes worth keeping are the last ones.
func TestTheKeptOutputIsTheEnd(t *testing.T) {
	b := &tailBuffer{limit: 8}
	b.Write([]byte("first line that is long"))
	b.Write([]byte("END"))
	if got, want := b.String(), " longEND"; got != want {
		t.Errorf("String = %q, want the last 8 bytes written (%q)", got, want)
	}
	if len(b.String()) > 8 {
		t.Errorf("kept %d bytes, over the limit", len(b.String()))
	}
}
