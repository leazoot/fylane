package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/tunnelget"
	"github.com/leazoot/fylane/companion/internal/tunnelproc"
)

// The download is an offer, not a default. Under `go test` stdin is at end of
// input, which is exactly the shape a scripted run has — and it must come out
// as a refusal that still says how to get the program.
func TestShareWithoutAYesInstallsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dataDir := t.TempDir()

	err := share([]string{"-data-dir", dataDir, t.TempDir()})
	if err == nil {
		t.Fatal("share ran with no tunnel installed")
	}
	if !strings.Contains(err.Error(), "cloudflared") || !strings.Contains(err.Error(), "brew install") {
		t.Errorf("error %q does not say what to install", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "tools")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused offer still created the download directory")
	}
	// Nothing is stored on the way to that refusal: a machine that cannot
	// publish itself must not be left configured as though it could.
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a failed share left this machine switched to direct mode")
	}
}

// What the user is agreeing to has to be on screen before the question: which
// program, which version, from where, and at which digest. An offer that only
// says "download it?" is not the consent the download prompt asks for.
func TestTheOfferSaysWhatWouldArrive(t *testing.T) {
	quick, ok := tunnelproc.Lookup(tunnelproc.CloudflareQuick)
	if !ok {
		t.Fatal("the quick tunnel provider is missing from this build")
	}
	pin, err := tunnelget.PinFor(quick.Binary, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no pinned build for this platform: %v", err)
	}

	var out strings.Builder
	if _, err := consented(strings.NewReader("n\n"), &out, quick, pin); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{quick.Binary, pin.Version, pin.URL, pin.SHA256, quick.Install} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the offer does not mention %q", want)
		}
	}
}

// Only a plain yes is a yes. Everything else — a blank line, a maybe, an
// empty pipe — leaves the machine as it was.
func TestOnlyAPlainYesIsConsent(t *testing.T) {
	quick, _ := tunnelproc.Lookup(tunnelproc.CloudflareQuick)
	pin := tunnelget.Pin{Binary: "cloudflared", Version: "1", Asset: "a", SHA256: "d", URL: "https://example.invalid/a"}

	for answer, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true, " y \n": true,
		"": false, "\n": false, "n\n": false, "no\n": false,
		"sure\n": false, "yes please\n": false, "yolo\n": false, "1\n": false,
	} {
		got, err := consented(strings.NewReader(answer), io.Discard, quick, pin)
		if err != nil {
			t.Fatalf("%q: %v", answer, err)
		}
		if got != want {
			t.Errorf("consented(%q) = %v, want %v", answer, got, want)
		}
	}
}

func TestShareTakesOneFolderOrTheCurrentOne(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := shareDir("")
	if err != nil || got != wd {
		t.Errorf("shareDir(\"\") = %q, %v; want the current directory", got, err)
	}

	dir := t.TempDir()
	rel, err := filepath.Rel(wd, dir)
	if err == nil {
		if got, err := shareDir(rel); err != nil || !filepath.IsAbs(got) {
			t.Errorf("a relative folder did not resolve to an absolute one: %q, %v", got, err)
		}
	}

	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := shareDir(file); err == nil {
		t.Error("a file was accepted as a workspace")
	}
	if _, err := shareDir(filepath.Join(dir, "nope")); err == nil {
		t.Error("a folder that does not exist was accepted")
	}
}

func TestShareRefusesMoreThanOneFolder(t *testing.T) {
	if err := share([]string{t.TempDir(), t.TempDir()}); err == nil {
		t.Error("share accepted two folders; only one of them would have been served")
	}
}

// A pairing code is the whole authorization in this mode — there is no desktop
// window to approve from — so an expired one on screen is the command quietly
// ceasing to work.
func TestTheAnnouncerReplacesTheCodeWhenItExpires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	var mints int
	var mu sync.Mutex
	mint := func(context.Context) (string, time.Duration, error) {
		mu.Lock()
		defer mu.Unlock()
		mints++
		return "code-" + string(rune('a'+mints-1)), 20 * time.Millisecond, nil
	}

	(&shareAnnouncer{ctx: ctx, out: out}).announce("https://example.test/mcp", mint)

	// Wait for what the assertion reads, not for the counter: mint returns
	// — and mints is incremented — before the caller has printed the code,
	// so a loop that stops at mints == 2 can read the buffer a beat too
	// early. A loaded runner is where that beat is long enough to matter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "code-b") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	n := mints
	mu.Unlock()
	if n < 2 {
		t.Fatalf("the code was issued %d time(s); an expired one stayed on screen", n)
	}
	if !strings.Contains(out.String(), "https://example.test/mcp") {
		t.Errorf("output %q does not carry the connector URL", out.String())
	}
	if !strings.Contains(out.String(), "code-a") || !strings.Contains(out.String(), "code-b") {
		t.Errorf("output %q does not show the replacement code", out.String())
	}
	// The explanation is printed once, not with every refresh.
	if strings.Count(out.String(), "Ctrl-C") != 1 {
		t.Errorf("the help text was repeated on every refresh: %q", out.String())
	}
}

// A quick tunnel can announce more than once. The code is issued by this
// process, not by whatever hostname points at it, so a second address must not
// start a second loop printing a second code.
func TestASecondAddressDoesNotStartASecondCodeLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	started := make(chan struct{}, 4)
	mint := func(c context.Context) (string, time.Duration, error) {
		started <- struct{}{}
		<-c.Done()
		return "", 0, c.Err()
	}

	a := &shareAnnouncer{ctx: ctx, out: out}
	a.announce("https://one.test/mcp", mint)
	a.announce("https://two.test/mcp", mint)

	<-started
	time.Sleep(50 * time.Millisecond)
	if n := len(started); n != 0 {
		t.Fatalf("%d extra code loops were started", n)
	}
	// Both addresses are still reported: the second one is where callers go.
	if !strings.Contains(out.String(), "two.test") {
		t.Errorf("output %q does not carry the new address", out.String())
	}
}

func TestTheAnnouncerStopsWithTheCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan struct{})
	mint := func(c context.Context) (string, time.Duration, error) {
		<-c.Done()
		close(done)
		return "", 0, c.Err()
	}
	(&shareAnnouncer{ctx: ctx, out: out}).announce("https://example.test/mcp", mint)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the code loop outlived the command")
	}
	// A cancelled command is not an error to report at the user.
	if strings.Contains(out.String(), "could not issue") {
		t.Errorf("shutting down was reported as a failure: %q", out.String())
	}
}

// syncBuffer is an io.Writer safe to read while the announcer's goroutine
// writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
