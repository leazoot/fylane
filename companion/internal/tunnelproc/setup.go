package tunnelproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The browser authorization flow. `cloudflared tunnel login` and `tailscale
// up` both print a URL and then block until the user approves in a browser.
// Running them here is what turns "paste a token" into "click, approve, done".

// SetupPhase is where an authorization has got to.
type SetupPhase string

const (
	// SetupIdle: no authorization is in progress.
	SetupIdle SetupPhase = "idle"
	// SetupStarting: the command is running but has not printed a URL yet.
	SetupStarting SetupPhase = "starting"
	// SetupWaiting: the URL is out and the user is in their browser.
	SetupWaiting SetupPhase = "waiting"
	// SetupReady: the provider wrote its credential; the tunnel can start.
	SetupReady SetupPhase = "ready"
	// SetupFailed: the command exited without authorizing.
	SetupFailed SetupPhase = "failed"
)

// setupTimeout is how long the user gets in their browser before the command
// is abandoned. Generous, because signing in can mean creating an account.
const setupTimeout = 10 * time.Minute

// SetupState is what the connect screen shows about an authorization.
type SetupState struct {
	Provider string     `json:"provider,omitempty"`
	Phase    SetupPhase `json:"phase"`
	// URL is the page the user has to approve on. The desktop opens it; it
	// is also shown so a user whose browser did not open can copy it.
	URL    string `json:"url,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Setups runs one authorization at a time and remembers which providers were
// authorized in this process — the answer for a provider whose credential
// cannot be read off disk.
type Setups struct {
	log *slog.Logger

	mu     sync.Mutex
	state  SetupState
	cancel context.CancelFunc
	done   map[Kind]bool
}

// NewSetups returns an authorization runner.
func NewSetups(log *slog.Logger) *Setups {
	return &Setups{log: log, state: SetupState{Phase: SetupIdle}, done: map[Kind]bool{}}
}

// Snapshot is the current authorization state.
func (s *Setups) Snapshot() SetupState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Authorized reports whether this provider was authorized during this run,
// for providers whose stored credential Fylane cannot see.
func (s *Setups) Authorized(k Kind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done[k]
}

// Forget drops what this run remembers about a provider's sign-in, and clears
// a finished sign-in still on screen. Without it, signing out would leave the
// panel still saying "account connected" — the credential is gone from disk
// but this process would keep vouching for it.
func (s *Setups) Forget(k Kind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.done, k)
	if s.state.Provider == string(k) && (s.state.Phase == SetupReady || s.state.Phase == SetupFailed) {
		s.state = SetupState{Phase: SetupIdle}
	}
}

// Cancel abandons an authorization in progress.
func (s *Setups) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	if s.state.Phase == SetupStarting || s.state.Phase == SetupWaiting {
		s.state = SetupState{Phase: SetupIdle}
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Start begins the provider's own login flow. It returns as soon as the
// command is running: the URL arrives on a later snapshot, because the user
// is going to spend minutes in a browser and nothing should block on that.
func (s *Setups) Start(ctx context.Context, p Provider) error {
	args, ok := p.SetupArgs()
	if !ok {
		return fmt.Errorf("%s has no browser sign-in", p.Kind)
	}
	bin, installed := p.Available()
	if !installed {
		return fmt.Errorf("%s is not installed", p.Binary)
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("an authorization is already in progress")
	}
	runCtx, cancel := context.WithTimeout(ctx, setupTimeout)
	s.cancel = cancel
	s.state = SetupState{Provider: string(p.Kind), Phase: SetupStarting}
	s.mu.Unlock()

	go s.run(runCtx, cancel, p, bin, args)
	return nil
}

func (s *Setups) run(ctx context.Context, cancel context.CancelFunc, p Provider, bin string, args []string) {
	defer cancel()
	err := s.runOnce(ctx, p, bin, args)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	// A cancel is the user's own doing; Cancel already reset the state and
	// overwriting it here would put a failure on screen they asked for.
	if s.state.Phase == SetupIdle {
		return
	}
	if err != nil {
		s.state = SetupState{Provider: string(p.Kind), Phase: SetupFailed, Detail: err.Error()}
		s.log.Warn("tunnel authorization failed", "provider", p.Kind)
		return
	}
	// Exit code zero is not proof. `cloudflared tunnel login` returns 0 when
	// the user closes the page without finishing, and trusting it left the
	// panel saying "account connected" with no certificate on disk — a claim
	// the next step would fail on. Where the credential can be read, that
	// reading is the answer.
	if p.Checkable() {
		if !p.Authorized() {
			s.state = SetupState{Provider: string(p.Kind), Phase: SetupFailed, Detail: errNotFinished}
			s.log.Warn("tunnel authorization left no credential", "provider", p.Kind)
			return
		}
		// Nothing to remember: the credential on disk is the truth from here
		// on, and a remembered "yes" would outlive a later sign-out.
		s.state = SetupState{Provider: string(p.Kind), Phase: SetupReady}
		s.log.Info("tunnel authorized", "provider", p.Kind)
		return
	}
	s.done[p.Kind] = true
	s.state = SetupState{Provider: string(p.Kind), Phase: SetupReady}
	s.log.Info("tunnel authorized", "provider", p.Kind)
}

// errNotFinished is what the user is told when the command came back without
// the credential. It is not an error the program can explain further: from
// here it looks like a clean exit.
const errNotFinished = "the sign-in page was closed before it finished"

// runOnce runs the login command to completion, publishing the browser URL as
// soon as it appears in the output.
func (s *Setups) runOnce(ctx context.Context, p Provider, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", p.Binary, err)
	}

	var last string
	var lastMu sync.Mutex
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			lastMu.Lock()
			if t := trimmed(line); t != "" {
				last = t
			}
			lastMu.Unlock()
			// The URL carries a one-time authorization parameter, so it goes
			// to the screen and nowhere else — not to a log line.
			if u := p.SetupURL(line); u != "" {
				s.setWaiting(p, u)
			}
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return errors.New("sign-in timed out")
		}
		lastMu.Lock()
		detail := last
		lastMu.Unlock()
		if detail == "" {
			detail = err.Error()
		}
		return errors.New(detail)
	}
	return nil
}

func (s *Setups) setWaiting(p Provider, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Phase != SetupStarting && s.state.Phase != SetupWaiting {
		return
	}
	s.state = SetupState{Provider: string(p.Kind), Phase: SetupWaiting, URL: url}
}

func trimmed(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}
