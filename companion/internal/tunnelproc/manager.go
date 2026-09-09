package tunnelproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// State is what the desktop shows about the tunnel process.
type State string

const (
	// Stopped means no process is running and none is wanted.
	Stopped State = "stopped"
	// Starting means the process is up but has not announced an address yet.
	Starting State = "starting"
	// Running means an address is published.
	Running State = "running"
	// Failed means the process keeps exiting; the manager is still retrying.
	Failed State = "failed"
)

const (
	minBackoff = 2 * time.Second
	maxBackoff = time.Minute
	// stopGrace is how long a tunnel gets to exit on its own before it is
	// killed. Cloudflared and ngrok both close their connections on SIGINT.
	stopGrace = 5 * time.Second
)

// Manager supervises one tunnel process: it starts it, reads the public URL
// out of its output, restarts it when it dies, and stops it on request.
//
// The command line can carry a Cloudflare tunnel token, so it is never
// logged. Logs name the provider and the state, nothing else.
type Manager struct {
	provider Provider
	options  Options
	log      *slog.Logger
	// onURL is called whenever the published address changes, including when
	// a restart produces a different one (quick tunnels always do).
	onURL func(string)

	mu     sync.Mutex
	state  State
	detail string
	url    string
	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a manager for one provider and its options.
func New(p Provider, opt Options, log *slog.Logger, onURL func(string)) *Manager {
	return &Manager{provider: p, options: opt, log: log, onURL: onURL, state: Stopped}
}

// Provider returns the configured provider.
func (m *Manager) Provider() Provider { return m.provider }

// Status reports the current state, a human-readable detail (empty when
// nothing is wrong), and the published URL.
func (m *Manager) Status() (State, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.detail, m.url
}

// PublicURL is the currently published address, empty when there is none.
func (m *Manager) PublicURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.url
}

// Connected reports whether an address is published (control API status).
func (m *Manager) Connected() bool { return m.PublicURL() != "" }

// Start launches the tunnel and supervises it until Stop or ctx ends.
// Starting an already-running manager is an error, not a second process.
func (m *Manager) Start(ctx context.Context) error {
	if _, ok := m.provider.Available(); !ok {
		return fmt.Errorf("%s is not installed: %s", m.provider.Binary, m.provider.Install)
	}
	if err := m.provider.Validate(m.options); err != nil {
		return err
	}
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return errors.New("tunnel is already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.cancel, m.done = cancel, done
	m.state, m.detail = Starting, ""
	m.mu.Unlock()

	go m.supervise(runCtx, done)
	return nil
}

// Stop ends the tunnel and waits for the process to go away.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	m.setURL("")
	m.mu.Lock()
	m.state, m.detail = Stopped, ""
	m.mu.Unlock()
}

// supervise runs the child process, restarting it with backoff until the
// context ends. A tunnel that dies at 3am should be back before morning.
func (m *Manager) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)
	backoff := minBackoff
	for {
		start := time.Now()
		err := m.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		m.setURL("")
		// A process that stayed up a while and then died is a fresh problem,
		// not an escalating one.
		if time.Since(start) > time.Minute {
			backoff = minBackoff
		}
		m.mu.Lock()
		m.state = Failed
		m.detail = fmt.Sprintf("%s exited: %v; retrying in %s", m.provider.Binary, err, backoff)
		m.mu.Unlock()
		m.log.Warn("tunnel process exited", "provider", m.provider.Kind, "retry_in", backoff.String())

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// runOnce runs the process to completion and returns why it ended.
func (m *Manager) runOnce(ctx context.Context) error {
	args, err := m.provider.Args(m.options)
	if err != nil {
		return err
	}
	// Resolved per run, not once at Start: the binary must be the shipped one
	// when there is one, and a restart after a crash should pick up a copy the
	// user installed in the meantime.
	bin, ok := m.provider.Available()
	if !ok {
		return fmt.Errorf("%s is not installed: %s", m.provider.Binary, m.provider.Install)
	}
	cmd := exec.Command(bin, args...)
	// A credential passed this way stays out of the process table, where an
	// argv would be readable by anything on the machine.
	if extra := m.provider.Env(m.options); len(extra) > 0 {
		cmd.Env = append(os.Environ(), extra...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", m.provider.Binary, err)
	}
	m.mu.Lock()
	m.state, m.detail = Starting, ""
	m.mu.Unlock()
	m.log.Info("tunnel process started", "provider", m.provider.Kind)

	// A named tunnel publishes an address the user already told us about;
	// there is nothing to wait for in the output.
	if fixed := m.provider.PublicURL(m.options); fixed != "" {
		m.setURL(fixed)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.scan(stdout) }()
	go func() { defer wg.Done(); m.scan(stderr) }()

	waitErr := make(chan error, 1)
	go func() { wg.Wait(); waitErr <- cmd.Wait() }()

	select {
	case err := <-waitErr:
		if err == nil {
			return errors.New("exited")
		}
		return err
	case <-ctx.Done():
		// Ask the child to close its connections, then insist. Leaving a
		// tunnel process behind would keep publishing a listener the user
		// just took down.
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			cmd.Process.Kill()
		}
		select {
		case <-waitErr:
		case <-time.After(stopGrace):
			cmd.Process.Kill()
			<-waitErr
		}
		return ctx.Err()
	}
}

// scan reads the process output line by line looking for the address. The
// lines themselves are not logged: a tunnel's output can name hostnames and
// request paths, and this process has no business copying those anywhere.
func (m *Manager) scan(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if u := m.provider.ParseURL(sc.Text()); u != "" {
			m.setURL(u)
		}
	}
}

// setURL records a new published address and notifies the caller once.
func (m *Manager) setURL(u string) {
	m.mu.Lock()
	if m.url == u {
		m.mu.Unlock()
		return
	}
	m.url = u
	if u != "" {
		m.state, m.detail = Running, ""
	}
	m.mu.Unlock()

	if u != "" {
		m.log.Info("tunnel published", "provider", m.provider.Kind, "url", u)
	}
	if m.onURL != nil {
		m.onURL(u)
	}
}
