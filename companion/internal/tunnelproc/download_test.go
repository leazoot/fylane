package tunnelproc

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// The transfer itself — https only, the redirect policy, the private-address
// guard, the digest check and the discard on mismatch — is package tunnelget's
// and is tested there against its own local server. What is this package's, and
// what is tested here, is everything around it: which downloads are refused
// before one starts, and the state the connect screen reads while one runs.

// isolate makes the "is it installed" question answerable: an empty PATH and a
// download directory of our own, so a cloudflared the developer happens to have
// does not decide what these tests assert.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	prev := DownloadDir()
	SetDownloadDir(dir)
	t.Cleanup(func() { SetDownloadDir(prev) })
	return dir
}

func TestOfferComesFromThePinTable(t *testing.T) {
	p, _ := Lookup(CloudflareQuick)
	offer, pinned := OfferFor(p)
	if !pinned {
		t.Fatal("no pinned cloudflared for the platform running the tests")
	}
	if offer.Binary != "cloudflared" || offer.Version == "" {
		t.Fatalf("offer = %+v", offer)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(offer.SHA256) {
		t.Fatalf("digest %q is not a sha-256", offer.SHA256)
	}
	// The source is shown next to the version, not with it folded in: a line
	// that already repeats the version reads as a different address each
	// release, which is the opposite of what a pinned source should look like.
	if !strings.HasPrefix(offer.Source, "https://") {
		t.Fatalf("source %q is not https", offer.Source)
	}
	if strings.Contains(offer.Source, offer.Version) {
		t.Fatalf("source %q carries the version", offer.Source)
	}
}

// A provider whose program this build has no pin for must answer "no offer"
// rather than an offer that cannot be honoured. This is the board's fourth
// state, and it is reached by asking, not by guessing from the platform.
func TestNoOfferForAnUnpinnedProgram(t *testing.T) {
	for _, kind := range []Kind{TailscaleFunnel, Ngrok} {
		p, ok := Lookup(kind)
		if !ok {
			t.Fatalf("no provider %s", kind)
		}
		if _, pinned := OfferFor(p); pinned {
			t.Fatalf("%s claims a pinned %s, but pins.txt has none", kind, p.Binary)
		}
	}
}

func TestDownloadRefusesWhatCannotSucceed(t *testing.T) {
	t.Run("no pin for this program", func(t *testing.T) {
		isolate(t)
		p, _ := Lookup(TailscaleFunnel)
		d := NewDownloads(quietLog())
		if err := d.Start(context.Background(), p); err == nil {
			t.Fatal("started a download for an unpinned program")
		}
		if d.Snapshot().Phase != DownloadIdle {
			t.Fatalf("a refused start left the screen at %s", d.Snapshot().Phase)
		}
	})

	t.Run("already on this machine", func(t *testing.T) {
		isolate(t)
		// Present and executable is the whole test: re-fetching a working
		// program is a download nobody needed, and the offer should never
		// have been on screen to press.
		fakeBinary(t, "cloudflared", `exit 0`)
		p, _ := Lookup(CloudflareQuick)
		if _, installed := p.Available(); !installed {
			t.Fatal("the fake cloudflared was not found")
		}
		d := NewDownloads(quietLog())
		if err := d.Start(context.Background(), p); err == nil {
			t.Fatal("started a download for a program that is already here")
		}
	})

	t.Run("nowhere to install it", func(t *testing.T) {
		isolate(t)
		SetDownloadDir("")
		p, _ := Lookup(CloudflareQuick)
		d := NewDownloads(quietLog())
		if err := d.Start(context.Background(), p); err == nil {
			t.Fatal("started a download with no directory to install into")
		}
	})
}

// One at a time. Two downloads of the same program racing to rename onto the
// same path is not a state the screen can describe, and it is not one the user
// asked for either — the second press is almost always the first one again.
//
// The occupied slot is set up directly rather than by letting a Start succeed.
// A successful Start reaches for github, which would put the network in a unit
// test and make the assertion a race against however fast that answered.
func TestOnlyOneDownloadRunsAtATime(t *testing.T) {
	isolate(t)
	p, _ := Lookup(CloudflareQuick)
	d := NewDownloads(quietLog())

	started := false
	_, cancel := context.WithCancel(context.Background())
	d.cancel = func() { started = true; cancel() }
	d.state = DownloadState{Provider: string(CloudflareQuick), Phase: DownloadRunning}

	if err := d.Start(context.Background(), p); err == nil {
		t.Fatal("a second download started alongside the first")
	}

	// Cancel is the user's own doing, so it leaves the screen idle rather than
	// reporting a failure they caused — and it does pull the context, or the
	// transfer would run on under a screen that says nothing is happening.
	d.Cancel()
	if st := d.Snapshot(); st.Phase != DownloadIdle {
		t.Fatalf("phase after Cancel = %s (%s)", st.Phase, st.Detail)
	}
	if !started {
		t.Fatal("Cancel left the download's context uncancelled")
	}
	if d.cancel != nil {
		t.Fatal("Cancel left the slot occupied, so nothing could be retried")
	}
}

func TestForgetClearsAFinishedDownload(t *testing.T) {
	d := NewDownloads(quietLog())
	d.state = DownloadState{Provider: string(CloudflareQuick), Phase: DownloadReady}

	d.Forget(Ngrok)
	if d.Snapshot().Phase != DownloadReady {
		t.Fatal("forgetting one provider cleared another's outcome")
	}
	d.Forget(CloudflareQuick)
	if d.Snapshot().Phase != DownloadIdle {
		t.Fatalf("phase after Forget = %s", d.Snapshot().Phase)
	}

	// A running download is not something to forget: it would leave the
	// goroutine writing into a state the screen says is idle.
	d.state = DownloadState{Provider: string(CloudflareQuick), Phase: DownloadRunning}
	d.Forget(CloudflareQuick)
	if d.Snapshot().Phase != DownloadRunning {
		t.Fatal("Forget cleared a download that was still running")
	}
}
