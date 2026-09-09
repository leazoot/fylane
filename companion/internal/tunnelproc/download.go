package tunnelproc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"

	"github.com/leazoot/fylane/companion/internal/tunnelget"
)

// Fetching a provider's program onto a machine whose Fylane package did not
// carry one.
//
// Shaped like Setups next door on purpose: one at a time, Start returns as
// soon as the work is running, and progress is read from a snapshot the
// connect screen already polls. What it is deliberately not shaped like is a
// fetcher: Start takes a provider and nothing else. No URL, no version, no
// digest crosses this boundary — the pin table compiled into this build
// answers all three, and there is no argument here that could override it.
//
// Consent belongs to the caller, because only the caller can ask. What this
// type guarantees is that it never starts on its own.

// DownloadPhase is where a download has got to.
type DownloadPhase string

const (
	// DownloadIdle: nothing is being fetched.
	DownloadIdle DownloadPhase = "idle"
	// DownloadRunning: bytes are moving and the digest is being computed.
	DownloadRunning DownloadPhase = "running"
	// DownloadReady: the program is verified and installed.
	DownloadReady DownloadPhase = "ready"
	// DownloadFailed: nothing was installed. A checksum mismatch lands here,
	// and tunnelget has already deleted what it fetched.
	DownloadFailed DownloadPhase = "failed"
)

// DownloadState is what the connect screen shows about a download.
type DownloadState struct {
	Provider string        `json:"provider,omitempty"`
	Phase    DownloadPhase `json:"phase"`
	Detail   string        `json:"detail,omitempty"`
}

// Offer is what the user is told before being asked. Every field comes from
// the pin, so the screen shows the same three facts the download is actually
// bound by.
type Offer struct {
	Binary  string `json:"binary"`
	Version string `json:"version"`
	Source  string `json:"source"`
	SHA256  string `json:"sha256"`
}

// OfferFor returns what this build would download for a provider on this
// platform, or false when there is no pin for it. No pin is a real answer and
// not an error here: the screen has a different thing to say in that case, and
// an offer that cannot be honoured is worse than no offer.
func OfferFor(p Provider) (Offer, bool) {
	pin, err := tunnelget.PinFor(p.Binary, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Offer{}, false
	}
	return Offer{
		Binary:  pin.Binary,
		Version: pin.Version,
		Source:  pin.Base,
		SHA256:  pin.SHA256,
	}, true
}

// Downloads runs one download at a time.
type Downloads struct {
	log *slog.Logger

	mu     sync.Mutex
	state  DownloadState
	cancel context.CancelFunc
}

// NewDownloads returns a download runner.
func NewDownloads(log *slog.Logger) *Downloads {
	return &Downloads{log: log, state: DownloadState{Phase: DownloadIdle}}
}

// Snapshot is the current download state.
func (d *Downloads) Snapshot() DownloadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// Cancel abandons a download in progress. tunnelget writes under a temporary
// name and removes it on the way out, so a cancelled download leaves nothing
// half-installed.
func (d *Downloads) Cancel() {
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = nil
	if d.state.Phase == DownloadRunning {
		d.state = DownloadState{Phase: DownloadIdle}
	}
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Start fetches the pinned build of this provider's program.
//
// The caller must already have the user's consent for this specific download.
// The checks before the goroutine are all refusals to start something that
// could not succeed — an already-installed program, an unpinned platform, no
// directory to install into.
func (d *Downloads) Start(ctx context.Context, p Provider) error {
	if _, installed := p.Available(); installed {
		return fmt.Errorf("%s is already on this machine", p.Binary)
	}
	pin, err := tunnelget.PinFor(p.Binary, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	dir := DownloadDir()
	if dir == "" {
		return errors.New("this Companion has no data directory to install into")
	}

	d.mu.Lock()
	if d.cancel != nil {
		d.mu.Unlock()
		return errors.New("a download is already in progress")
	}
	// No timeout of our own: tunnelget's client already bounds the whole
	// transfer. This context exists so the cancel button has something to
	// pull.
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.state = DownloadState{Provider: string(p.Kind), Phase: DownloadRunning}
	d.mu.Unlock()

	go d.run(runCtx, cancel, p, pin, dir)
	return nil
}

func (d *Downloads) run(ctx context.Context, cancel context.CancelFunc, p Provider, pin tunnelget.Pin, dir string) {
	defer cancel()
	g := &tunnelget.Getter{}
	_, err := g.Fetch(ctx, pin, dir)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancel = nil
	// A cancel is the user's own doing; Cancel already reset the state and
	// overwriting it here would put a failure on screen they asked for.
	if d.state.Phase == DownloadIdle {
		return
	}
	if err != nil {
		d.state = DownloadState{Provider: string(p.Kind), Phase: DownloadFailed, Detail: err.Error()}
		// The provider and the outcome, not the message: a failure detail can
		// carry the digest that was actually received, and the log is not
		// where that belongs.
		d.log.Warn("tunnel binary download failed", "provider", p.Kind, "binary", p.Binary)
		return
	}
	d.state = DownloadState{Provider: string(p.Kind), Phase: DownloadReady}
	d.log.Info("tunnel binary installed", "provider", p.Kind, "binary", p.Binary, "version", pin.Version)
}

// Forget clears a finished download still on screen, so a row that has moved
// on to "installed" does not keep a stale outcome under it.
func (d *Downloads) Forget(k Kind) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state.Provider == string(k) && (d.state.Phase == DownloadReady || d.state.Phase == DownloadFailed) {
		d.state = DownloadState{Phase: DownloadIdle}
	}
}
