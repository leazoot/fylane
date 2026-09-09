package lsp

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/leazoot/fylane/companion/internal/readbox"
)

// DefaultIdleTimeout is how long a language server may sit unused before it is
// reclaimed. gopls holds a whole repository's type information in memory, so
// leaving one running for a workspace nobody is working in costs real RAM;
// starting one costs seconds. Twenty minutes is long enough that a reading
// session never pays the start cost twice and short enough that a machine left
// alone gets its memory back.
const DefaultIdleTimeout = 20 * time.Minute

// ErrClosed is returned once the supervisor has been shut down.
var ErrClosed = errors.New("the Companion is shutting down")

// Supervisor owns every language server this Companion has started.
//
// It is the answer to the two objections mcpgate raised against long-lived
// foreign processes. The owner is this object, and it is created and closed by
// the one place that owns the Core's lifetime. The shutdown path is Close,
// which stops every server it started, and a reaper that stops the ones nobody
// is using before then.
type Supervisor struct {
	reg  *Registry
	idle time.Duration
	box  *readbox.Box

	mu       sync.Mutex
	clients  map[string]*client
	starting map[string]chan struct{}
	// approved remembers that the local user said this server could run in
	// this workspace. It lives for as long as this Companion process does and
	// is not persisted: the question is cheap to ask again after a restart,
	// and a stored answer would be a grant nothing in the UI can show or
	// withdraw. Idle reclaim deliberately does not clear it — the user
	// authorized the server, not the process.
	approved map[string]bool
	closed   bool

	stop context.CancelFunc
	done chan struct{}
}

// NewSupervisor starts the reaper and returns a supervisor the caller must
// Close. idle <= 0 means DefaultIdleTimeout.
func NewSupervisor(reg *Registry, idle time.Duration, box *readbox.Box) *Supervisor {
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		reg:      reg,
		idle:     idle,
		box:      box,
		clients:  map[string]*client{},
		starting: map[string]chan struct{}{},
		approved: map[string]bool{},
		stop:     cancel,
		done:     make(chan struct{}),
	}
	go s.reap(ctx)
	return s
}

// Registry is the set of servers this supervisor can start.
func (s *Supervisor) Registry() *Registry { return s.reg }

// Approved reports whether the local user has authorized this server in this
// workspace during this run.
func (s *Supervisor) Approved(workspaceID, server string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approved[key(workspaceID, server)]
}

// Approve records that authorization. Only the approval path calls it.
func (s *Supervisor) Approve(workspaceID, server string) {
	s.mu.Lock()
	s.approved[key(workspaceID, server)] = true
	s.mu.Unlock()
}

// Running reports whether a server for this workspace is already up, so the
// caller can tell "starting one" from "asking one".
func (s *Supervisor) Running(workspaceID, server string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.clients[key(workspaceID, server)]
	return ok
}

// RunningServers names the servers that are up right now, in any workspace.
//
// Names only. Which workspace a server is indexing is not what the settings
// page is asking, and a workspace is not something this answer has any reason
// to be able to carry.
func (s *Supervisor) RunningServers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, c := range s.clients {
		if !seen[c.server.Name] {
			seen[c.server.Name] = true
			out = append(out, c.server.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Close stops every server and the reaper. It is safe to call twice.
func (s *Supervisor) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	running := make([]*client, 0, len(s.clients))
	for _, c := range s.clients {
		running = append(running, c)
	}
	s.clients = map[string]*client{}
	s.mu.Unlock()

	s.stop()
	<-s.done
	var wg sync.WaitGroup
	for _, c := range running {
		wg.Add(1)
		go func(c *client) { defer wg.Done(); c.stop() }(c)
	}
	wg.Wait()
}

// get returns the running server for this workspace, starting one if needed.
// Two callers arriving together share one start rather than racing two gopls
// processes onto the same repository.
func (s *Supervisor) get(ctx context.Context, workspaceID, root string, srv Server) (*client, error) {
	k := key(workspaceID, srv.Name)
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		if c, ok := s.clients[k]; ok {
			s.mu.Unlock()
			c.touch()
			return c, nil
		}
		if ch, ok := s.starting[k]; ok {
			s.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		s.starting[k] = ch
		s.mu.Unlock()

		c := newClient(srv, root, s.box)
		err := c.start(ctx)

		s.mu.Lock()
		delete(s.starting, k)
		close(ch)
		switch {
		case err != nil:
			s.mu.Unlock()
			return nil, err
		case s.closed:
			s.mu.Unlock()
			c.stop()
			return nil, ErrClosed
		}
		s.clients[k] = c
		s.mu.Unlock()
		c.touch()
		return c, nil
	}
}

// drop removes a client and stops it. Used when a server has died under a
// request: the next call should start a fresh one rather than keep talking to
// a pipe nobody is reading.
func (s *Supervisor) drop(workspaceID, server string) {
	k := key(workspaceID, server)
	s.mu.Lock()
	c := s.clients[k]
	delete(s.clients, k)
	s.mu.Unlock()
	if c != nil {
		go c.stop()
	}
}

func (s *Supervisor) reap(ctx context.Context) {
	defer close(s.done)
	tick := time.NewTicker(s.idle / 2)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cutoff := time.Now().Add(-s.idle)
			s.mu.Lock()
			var stale []*client
			for k, c := range s.clients {
				if c.idleSince().Before(cutoff) {
					stale = append(stale, c)
					delete(s.clients, k)
				}
			}
			s.mu.Unlock()
			for _, c := range stale {
				go c.stop()
			}
		}
	}
}

func key(workspaceID, server string) string { return workspaceID + "\x00" + server }
