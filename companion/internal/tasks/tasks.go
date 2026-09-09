// Package tasks runs work that may outlive the call that started it.
//
// The shape is forced by a measured constraint, not a preference: platform
// tool calls die at roughly 60 seconds on ChatGPT and Grok and 300 on Claude
// , while a test suite or a delegated agent run
// takes minutes. So work gets a synchronous budget, and whatever does not
// finish inside it keeps running under a task id the caller can poll. A short
// command still costs exactly one round trip; only the long ones pay for the
// machinery.
//
// The table does not know what a command is. Work is a function, so the
// delegated-agent adapter plugs in without this package learning
// anything about agents.
package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Defaults. Budget is the synchronous wait, deliberately under the tightest
// platform wall so the caller gets a real answer rather than a dropped call.
const (
	DefaultBudget         = 45 * time.Second
	DefaultMaxOutputBytes = 1 << 20
	DefaultRetention      = time.Hour
	DefaultMaxTasks       = 200
	// DefaultMaxRuntime is how long any one task may run before it is stopped
	// and reported as timed out. A command carries its own, tighter timeout
	// from cmdexec; a delegated agent carried none at all, so nothing bounded
	// a run that never ended by itself. Hours rather than minutes
	// because the reason delegation exists is work that takes them: this is
	// the ceiling for "it never finished", not a budget for "it took a while".
	DefaultMaxRuntime = 2 * time.Hour
	// closeGrace bounds shutdown. Work is expected to honour its context;
	// this stops one that does not from holding the Core open.
	closeGrace = 10 * time.Second
)

var (
	ErrUnknownTask = errors.New("unknown task id")
	ErrClosed      = errors.New("task manager is shutting down")
)

// State is where a task got to.
type State string

const (
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	TimedOut  State = "timed_out"
	Canceled  State = "canceled"
	// Denied is only ever read back out of the audit log. The manager never
	// produces it: a command the user refused never became a task, and that
	// is exactly why the task screen could not show one.
	Denied State = "denied"
	// Interrupted is also read back rather than produced: it means Fylane
	// stopped while the work was running. It is deliberately not Failed or
	// Canceled — both of those say the work reached an end and report which.
	// This one says nobody knows, which is a different answer and the only
	// honest one available after the process died with its parent.
	Interrupted State = "interrupted"
)

// Terminal reports whether no further change is possible.
func (s State) Terminal() bool { return s != Running }

// StateFromOutcome maps an audited command outcome onto the state the
// screens and the MCP surface speak. It lives here because State is this
// package's type and two readers need the same answer: the desktop history
// list and task_status recalling a run the manager has forgotten. They
// disagreed for exactly as long as each had its own copy.
//
// An unrecognised outcome becomes Failed rather than being dropped: a row
// whose meaning is unknown is still a command that happened, and hiding it
// would be the worse of the two wrong answers.
func StateFromOutcome(outcome string) State {
	switch outcome {
	case "ok":
		return Succeeded
	case "timeout":
		return TimedOut
	case "denied", "refused":
		return Denied
	case "interrupted":
		return Interrupted
	default:
		return Failed
	}
}

// Outcome is what a Work reports when it finishes.
type Outcome struct {
	ExitCode int
	TimedOut bool
}

// Work is one unit of work. It must return when ctx is done, and it reports
// progress by writing to stdout and stderr as it goes rather than only at the
// end — otherwise a poller sees nothing until the task is over.
type Work func(ctx context.Context, stdout, stderr io.Writer) (Outcome, error)

// Meta describes a task to the user and to the deduplicator.
type Meta struct {
	// Key makes a retry idempotent. A platform that resends the same tool
	// call — which is exactly what happens when a response is slow — must
	// not start the work twice, so a repeated key returns the existing task
	// instead of a second one. Empty disables deduplication.
	Key string
	// Label is what the user sees in the desktop task view, e.g. "npm test".
	Label string
	// Dir is the workspace-relative working directory, for display. Never an
	// absolute path.
	Dir string
	// Provider names the platform that asked for this work. It exists for
	// the audit view: output a command produced was sent somewhere, and a
	// record that does not say where is only half a record.
	Provider string
	// Network is what this run gets from the outbound boundary
	// (readbox.Reach), carried so a caller asking about the task later gets
	// the same answer as the one who started it. Empty says nothing.
	Network string
	// Budget overrides how long Run waits before backgrounding this task.
	Budget time.Duration
	// ID, when set, is used instead of a generated one. It exists so a
	// caller that must journal this work under an id can use one identifier
	// for both, rather than keeping a mapping that a restart would lose —
	// which is the whole problem the journal exists to solve. It is ignored
	// when an existing task is re-attached by Key.
	ID string
}

// Snapshot is an immutable view of a task at one moment.
type Snapshot struct {
	ID       string `json:"task_id"`
	State    State  `json:"state"`
	Label    string `json:"label,omitempty"`
	Dir      string `json:"dir,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Network is readbox.Reach's word for this run, as recorded when it
	// started. It is not re-derived on read: the answer that matters is the
	// one the command actually ran under.
	Network  string `json:"network,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	// Cursors to pass to the next Status call to get only what is new.
	StdoutCursor    int           `json:"stdout_cursor"`
	StderrCursor    int           `json:"stderr_cursor"`
	StdoutTruncated bool          `json:"stdout_truncated,omitempty"`
	StderrTruncated bool          `json:"stderr_truncated,omitempty"`
	StartedAt       time.Time     `json:"started_at"`
	Duration        time.Duration `json:"duration"`
}

// Options configure a Manager. Zero values take the package defaults.
type Options struct {
	Budget         time.Duration
	MaxOutputBytes int
	Retention      time.Duration
	MaxTasks       int
	MaxRuntime     time.Duration
	// now is injectable so retention can be tested without sleeping.
	now func() time.Time
}

// Manager owns every running task. One lives for the life of the Core.
type Manager struct {
	budget     time.Duration
	maxOutput  int
	retention  time.Duration
	maxTasks   int
	maxRuntime time.Duration
	now        func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	tasks  map[string]*task
	byKey  map[string]string
	closed bool
	// nextSeq orders tasks that share a startedAt. Clocks are coarse —
	// Windows ticks in milliseconds — so two tasks started back to back can
	// carry the same time, and "oldest" then has to mean "arrived first".
	nextSeq uint64
}

// New builds a Manager. Close must be called at shutdown: it is what turns
// "the Core exited" into "the processes it started exited too".
func New(opts Options) *Manager {
	m := &Manager{
		budget:     orDuration(opts.Budget, DefaultBudget),
		maxOutput:  orInt(opts.MaxOutputBytes, DefaultMaxOutputBytes),
		retention:  orDuration(opts.Retention, DefaultRetention),
		maxTasks:   orInt(opts.MaxTasks, DefaultMaxTasks),
		maxRuntime: orDuration(opts.MaxRuntime, DefaultMaxRuntime),
		now:        opts.now,
		tasks:      map[string]*task{},
		byKey:      map[string]string{},
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

// Run starts work and waits for it, but only up to the budget. It returns a
// terminal snapshot if the work finished in time and a running one if it did
// not — in both cases the caller has a task id and needs no second code path.
//
// ctx is the caller's request, not the task's lifetime: when the tool call is
// abandoned the task keeps running, because the whole point is to survive a
// platform that hung up at 60 seconds.
func (m *Manager) Run(ctx context.Context, meta Meta, work Work) (Snapshot, error) {
	t, fresh, err := m.acquire(meta)
	if err != nil {
		return Snapshot{}, err
	}
	if fresh {
		m.launch(t, work)
	}

	budget := orDuration(meta.Budget, m.budget)
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	return t.snapshot(0, 0), nil
}

// Status returns the task's current state plus whatever output arrived after
// the given cursors.
func (m *Manager) Status(id string, stdoutCursor, stderrCursor int) (Snapshot, error) {
	m.mu.Lock()
	m.purgeLocked()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrUnknownTask, id)
	}
	return t.snapshot(stdoutCursor, stderrCursor), nil
}

// Cancel stops a running task. It is not an error to cancel one that has
// already finished — a caller racing the task's own completion should not
// have to care which won.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownTask, id)
	}
	t.cancel()
	return nil
}

// List returns every retained task, newest first.
func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	m.purgeLocked()
	out := make([]*task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	m.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.startedAt.Equal(b.startedAt) {
			return a.startedAt.After(b.startedAt)
		}
		return a.seq > b.seq
	})
	snaps := make([]Snapshot, 0, len(out))
	for _, t := range out {
		snaps = append(snaps, t.snapshot(0, 0))
	}
	return snaps
}

// Clear forgets every finished task and reports how many went. A running task
// is not a record yet — it is still happening, and dropping it would leave a
// process nothing can report on or cancel — so those stay.
func (m *Manager) Clear() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleared := 0
	for id, t := range m.tasks {
		if terminal, _ := t.terminalAt(); terminal {
			m.forgetLocked(id, t)
			cleared++
		}
	}
	return cleared
}

// Close cancels every running task and waits for them to stop. After Close,
// Run refuses rather than starting work nothing will clean up.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()

	m.cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(closeGrace):
	}
}

// acquire returns the task for meta, creating one unless the idempotency key
// has been seen. The whole check-and-insert happens under one lock so two
// concurrent retries of the same call cannot both decide they are first.
func (m *Manager) acquire(meta Meta) (*task, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, ErrClosed
	}
	m.purgeLocked()

	if meta.Key != "" {
		if id, ok := m.byKey[meta.Key]; ok {
			// A key re-attaches only while the work is still in flight. Once
			// it has an answer, the same call means "do it again" — running
			// the test suite twice in a row is ordinary, and returning an
			// hour-old result instead would be a lie.
			if t, ok := m.tasks[id]; ok {
				if terminal, _ := t.terminalAt(); !terminal {
					return t, false, nil
				}
			}
			delete(m.byKey, meta.Key)
		}
	}

	id := meta.ID
	if id == "" {
		var err error
		if id, err = newID(); err != nil {
			return nil, false, err
		}
	} else if _, taken := m.tasks[id]; taken {
		return nil, false, fmt.Errorf("task id %s is already in use", id)
	}
	// The ceiling is on the task's own context rather than on the caller's:
	// the caller is a tool call that dies at 60 seconds, and the whole reason
	// this package exists is that the work outlives it.
	ctx, cancel := context.WithTimeout(m.ctx, m.maxRuntime)
	t := &task{
		id:         id,
		key:        meta.Key,
		label:      meta.Label,
		dir:        meta.Dir,
		provider:   meta.Provider,
		network:    meta.Network,
		state:      Running,
		startedAt:  m.now(),
		seq:        m.nextSeq,
		maxRuntime: m.maxRuntime,
		stdout:     newBuffer(m.maxOutput),
		stderr:     newBuffer(m.maxOutput),
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		now:        m.now,
	}
	m.nextSeq++
	m.tasks[id] = t
	if meta.Key != "" {
		m.byKey[meta.Key] = id
	}
	return t, true, nil
}

func (m *Manager) launch(t *task, work Work) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer t.cancel()
		out, err := work(t.ctx, t.stdout, t.stderr)
		t.finish(out, err)
	}()
}

// purgeLocked drops terminal tasks that are past retention, then trims the
// oldest terminal ones if the table is still over its cap. Running tasks are
// never dropped: forgetting one would leave a process nothing can report on
// or cancel.
func (m *Manager) purgeLocked() {
	now := m.now()
	for id, t := range m.tasks {
		if s, ended := t.terminalAt(); s && now.Sub(ended) > m.retention {
			m.forgetLocked(id, t)
		}
	}
	if len(m.tasks) <= m.maxTasks {
		return
	}
	var finished []*task
	for _, t := range m.tasks {
		if s, _ := t.terminalAt(); s {
			finished = append(finished, t)
		}
	}
	sort.Slice(finished, func(i, j int) bool {
		a, b := finished[i], finished[j]
		if !a.startedAt.Equal(b.startedAt) {
			return a.startedAt.Before(b.startedAt)
		}
		return a.seq < b.seq
	})
	for _, t := range finished {
		if len(m.tasks) <= m.maxTasks {
			return
		}
		m.forgetLocked(t.id, t)
	}
}

func (m *Manager) forgetLocked(id string, t *task) {
	delete(m.tasks, id)
	if t.key != "" && m.byKey[t.key] == id {
		delete(m.byKey, t.key)
	}
}

type task struct {
	id         string
	key        string
	seq        uint64
	label      string
	dir        string
	provider   string
	network    string
	startedAt  time.Time
	maxRuntime time.Duration
	stdout     *buffer
	stderr     *buffer
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	now        func() time.Time

	mu       sync.Mutex
	state    State
	exitCode int
	errMsg   string
	endedAt  time.Time
}

func (t *task) finish(out Outcome, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.Terminal() {
		return
	}
	t.endedAt = t.now()
	t.exitCode = out.ExitCode
	switch {
	case out.TimedOut:
		t.state = TimedOut
	case errors.Is(t.ctx.Err(), context.DeadlineExceeded):
		// The runtime ceiling, which is a timeout and not a cancellation: no
		// user asked for this and the distinction is the only thing telling
		// them why the work stopped.
		t.state = TimedOut
		t.errMsg = fmt.Sprintf("stopped at the %s ceiling for a single task", t.maxRuntime)
	case t.ctx.Err() != nil:
		// Cancellation and failure look alike from the exit code, and
		// reporting a shutdown as a failed build would send the user
		// looking for a bug that is not there.
		t.state = Canceled
	case err != nil:
		t.state = Failed
		t.errMsg = err.Error()
	case out.ExitCode == 0:
		t.state = Succeeded
	default:
		t.state = Failed
	}
	close(t.done)
}

func (t *task) terminalAt() (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state.Terminal(), t.endedAt
}

func (t *task) snapshot(stdoutCursor, stderrCursor int) Snapshot {
	outText, outNext, outTrunc := t.stdout.since(stdoutCursor)
	errText, errNext, errTrunc := t.stderr.since(stderrCursor)

	t.mu.Lock()
	defer t.mu.Unlock()
	end := t.endedAt
	if !t.state.Terminal() {
		end = t.now()
	}
	return Snapshot{
		ID:              t.id,
		State:           t.state,
		Label:           t.label,
		Network:         t.network,
		Dir:             t.dir,
		Provider:        t.provider,
		ExitCode:        t.exitCode,
		Error:           t.errMsg,
		Stdout:          outText,
		Stderr:          errText,
		StdoutCursor:    outNext,
		StderrCursor:    errNext,
		StdoutTruncated: outTrunc,
		StderrTruncated: errTrunc,
		StartedAt:       t.startedAt,
		Duration:        end.Sub(t.startedAt),
	}
}

// NewID produces an id a caller can hand back in Meta.ID. It exists for the
// caller that has to know the identifier before the work starts — journalling
// a run under the same id the caller will later poll is the point.
func NewID() (string, error) { return newID() }

// newID produces an unguessable id. Task ids travel to the platform and back,
// so a sequential counter would let one conversation poll another's task.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating task id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func orDuration(v, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return v
}

func orInt(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}
