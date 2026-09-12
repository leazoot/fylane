// Package machines connects this Companion to Companions running on other
// computers, over SSH.
//
// A remote machine runs its own, complete Companion: the files are local to
// it, the write transaction and the sandbox hold there exactly as they do
// here, and its audit log is its own. What this package adds is the control
// plane — it keeps an SSH session to each machine, forwards that machine's
// loopback control API and MCP listener to loopback ports here, and lets the
// desktop (and the MCP router) reach them as if they were local. The remote
// Companion publishes nothing itself: no tunnel, no pairing, no public
// surface. This Companion is its tunnel.
//
// SSH is the system `ssh`, on purpose. Keys stay in the user's agent or
// files, host fingerprints stay in known_hosts, and ~/.ssh/config aliases
// keep working — none of that is reimplemented here. BatchMode is always on,
// so nothing here can ever hang on a password prompt; a machine that needs
// one is reported, not waited for.
package machines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Machine is one remote computer, as the user described it. Host may be a
// hostname, an address, or an alias from ~/.ssh/config; User and Port are
// optional and default to whatever ssh would use.
type Machine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
	User string `json:"user,omitempty"`
	Port int    `json:"port,omitempty"`
}

// Store persists the machine list. Defined here, at the consumer, so the
// package does not depend on where settings live.
type Store interface {
	Load() ([]Machine, error)
	Save([]Machine) error
}

// State is where a machine's link is.
type State string

const (
	// StateOff means the user disconnected it; nothing is tried.
	StateOff State = "off"
	// StateConnecting means ssh is being established or the machine probed.
	StateConnecting State = "connecting"
	// StateMissing means the machine answered but has no Companion of this
	// version on it. Install is the way out.
	StateMissing State = "missing"
	// StateInstalling means the install script is running there.
	StateInstalling State = "installing"
	// StateStarting means the Companion is being launched there.
	StateStarting State = "starting"
	// StateOnline means the forwards are up and the remote control API
	// answered.
	StateOnline State = "online"
	// StateError means the last attempt failed; Detail says why and the
	// link keeps retrying.
	StateError State = "error"
)

// Status is what the desktop sees about one machine. It never carries the
// remote token or the forwarded ports.
type Status struct {
	Machine
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
	// Reason is a code for Detail when the window has its own words for it.
	Reason string `json:"reason,omitempty"`
	// Version is the remote Companion's version, once probed.
	Version string    `json:"version,omitempty"`
	Since   time.Time `json:"since"`
}

// Options configures a Manager.
type Options struct {
	Store Store
	// Dialer runs commands and holds forwards; nil means the system ssh.
	Dialer Dialer
	// Version is this Companion's version. The remote must match it (modulo
	// a -dev suffix), and it is what an install pins.
	Version string
	Log     *slog.Logger
}

// Manager owns the links to every configured machine.
type Manager struct {
	store   Store
	dial    Dialer
	version string
	log     *slog.Logger

	rt *router

	mu    sync.Mutex
	ctx   context.Context
	links map[string]*link
	order []string
}

// New builds a Manager; Start connects it.
func New(opt Options) *Manager {
	if opt.Dialer == nil {
		opt.Dialer = &sshDialer{}
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	m := &Manager{store: opt.Store, dial: opt.Dialer, version: opt.Version, log: opt.Log,
		links: map[string]*link{}}
	m.rt = newRouter(m)
	return m
}

// Start loads the machine list and begins connecting to every machine. It
// returns once the loops are launched; ctx ending stops them all.
func (m *Manager) Start(ctx context.Context) error {
	list, err := m.store.Load()
	if err != nil {
		return fmt.Errorf("loading machines: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx = ctx
	for _, mc := range list {
		l := m.newLinkLocked(mc)
		l.start(ctx)
	}
	return nil
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validate rejects anything ssh could read as an option or that has no
// business in a host name. The host is passed as a positional argument, so
// a leading dash is the one shape that could turn a name into a flag.
func validate(mc Machine) error {
	if strings.TrimSpace(mc.Name) == "" {
		return errors.New("a machine needs a name")
	}
	if !namePattern.MatchString(mc.Host) {
		return fmt.Errorf("host %q: use a hostname, an address, or an ssh config alias", mc.Host)
	}
	if mc.User != "" && !namePattern.MatchString(mc.User) {
		return fmt.Errorf("user %q is not a valid login name", mc.User)
	}
	if mc.Port < 0 || mc.Port > 65535 {
		return fmt.Errorf("port %d is out of range", mc.Port)
	}
	return nil
}

// Add records a machine and starts connecting to it.
func (m *Manager) Add(mc Machine) (Status, error) {
	if err := validate(mc); err != nil {
		return Status{}, err
	}
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return Status{}, err
	}
	mc.ID = "m_" + hex.EncodeToString(buf[:])
	mc.Name = strings.TrimSpace(mc.Name)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return Status{}, errors.New("machines are not started")
	}
	list := m.listLocked()
	list = append(list, mc)
	if err := m.store.Save(list); err != nil {
		return Status{}, fmt.Errorf("saving machines: %w", err)
	}
	l := m.newLinkLocked(mc)
	l.start(m.ctx)
	return l.status(), nil
}

// Update replaces how a machine is reached and reconnects with the new
// details. A typo in a host is fixed here, not by removing the machine.
func (m *Manager) Update(mc Machine) (Status, error) {
	if err := validate(mc); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[mc.ID]
	if !ok {
		return Status{}, ErrUnknown
	}
	mc.Name = strings.TrimSpace(mc.Name)
	l.stop()
	l.m = mc
	if err := m.store.Save(m.listLocked()); err != nil {
		return Status{}, fmt.Errorf("saving machines: %w", err)
	}
	l.start(m.ctx)
	return l.status(), nil
}

// ProbeResult answers "what is at this address" for a machine that is not
// saved yet: the add sheet asks while the user types, so the answer arrives
// before the decision instead of after. Nothing is stored and no forward is
// opened; a machine that does not answer is a sentence, not an error.
type ProbeResult struct {
	Reachable bool   `json:"reachable"`
	Detail    string `json:"detail,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Version   string `json:"version,omitempty"`
	// Running is whether a Companion there has a live control file.
	Running bool `json:"running"`
	// Compatible is whether that version can be driven by this one.
	Compatible bool `json:"compatible"`
}

func (m *Manager) Probe(ctx context.Context, mc Machine) (ProbeResult, error) {
	if err := validate(mc); err != nil {
		return ProbeResult{}, err
	}
	l := &link{mgr: m, m: mc}
	p, err := l.probe(ctx)
	if err != nil {
		return ProbeResult{Detail: err.Error(), Reason: reasonOf(err)}, nil
	}
	return ProbeResult{Reachable: true, Version: p.version, Running: p.control != nil,
		Compatible: compatible(p.version, m.version)}, nil
}

// Remove disconnects a machine and forgets it. The remote Companion keeps
// running; nothing on that machine is touched.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok {
		return ErrUnknown
	}
	l.stop()
	delete(m.links, id)
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	if err := m.store.Save(m.listLocked()); err != nil {
		return fmt.Errorf("saving machines: %w", err)
	}
	return nil
}

// ErrUnknown is returned for a machine ID that is not configured.
var ErrUnknown = errors.New("no such machine")

// Connect (re)starts the link to a machine.
func (m *Manager) Connect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok {
		return ErrUnknown
	}
	if l.cancel != nil {
		return nil
	}
	l.start(m.ctx)
	return nil
}

// Disconnect stops the link and leaves the machine off until Connect.
func (m *Manager) Disconnect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok {
		return ErrUnknown
	}
	l.stop()
	return nil
}

// Install asks the link to install this Companion's version on the machine.
// Only a machine that answered and was found missing takes it: an install is
// something the user clicks for, never a side effect of connecting.
func (m *Manager) Install(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok {
		return ErrUnknown
	}
	if l.state != StateMissing {
		return fmt.Errorf("nothing to install: the machine is %s", l.state)
	}
	select {
	case l.install <- struct{}{}:
	default:
	}
	return nil
}

// List reports every machine, in the order they were added.
func (m *Manager) List() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.links[id].status())
	}
	return out
}

// Proxy returns a handler that forwards a request to the machine's control
// API, adding that machine's token. The caller strips its own prefix first.
func (m *Manager) Proxy(id string) (http.Handler, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok {
		return nil, ErrUnknown
	}
	if l.state != StateOnline || l.proxy == nil {
		return nil, fmt.Errorf("%s is not connected", l.m.Name)
	}
	return l.proxy, nil
}

// endpoints snapshots what the router needs from every online machine.
func (m *Manager) endpoints() []endpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]endpoint, 0, len(m.order))
	for _, id := range m.order {
		l := m.links[id]
		if l.state != StateOnline {
			continue
		}
		out = append(out, endpoint{id: id, name: l.m.Name, mcpBase: l.mcpBase, ctlBase: l.ctlBase, token: l.token})
	}
	return out
}

type endpoint struct {
	id, name, mcpBase, ctlBase, token string
}

// Online lists the machines whose links are up, for the router.
func (m *Manager) Online() []Status {
	all := m.List()
	out := all[:0]
	for _, s := range all {
		if s.State == StateOnline {
			out = append(out, s)
		}
	}
	return out
}

func (m *Manager) listLocked() []Machine {
	out := make([]Machine, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.links[id].m)
	}
	return out
}

func (m *Manager) newLinkLocked(mc Machine) *link {
	l := &link{mgr: m, m: mc, state: StateOff, since: time.Now(), install: make(chan struct{}, 1)}
	m.links[mc.ID] = l
	m.order = append(m.order, mc.ID)
	return l
}

// link is the live side of one machine.
type link struct {
	mgr *Manager
	m   Machine

	// Guarded by mgr.mu.
	state   State
	detail  string
	reason  string
	version string
	since   time.Time
	cancel  context.CancelFunc
	proxy   *httputil.ReverseProxy
	mcpBase string
	ctlBase string
	token   string

	install chan struct{}
}

func (l *link) status() Status {
	return Status{Machine: l.m, State: l.state, Detail: l.detail, Reason: l.reason, Version: l.version, Since: l.since}
}

// set is called from the loop goroutine. A loop whose ctx has ended has been
// stopped or replaced, and its last words must not overwrite the new state.
func (l *link) set(ctx context.Context, state State, detail string) {
	l.setWithReason(ctx, state, detail, "")
}

func (l *link) setWithReason(ctx context.Context, state State, detail, reason string) {
	l.mgr.mu.Lock()
	defer l.mgr.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if l.state != state || l.detail != detail {
		l.since = time.Now()
	}
	l.state, l.detail, l.reason = state, detail, reason
	if state != StateOnline {
		l.proxy, l.mcpBase, l.ctlBase, l.token = nil, "", "", ""
	}
	l.mgr.log.Info("machine", "name", l.m.Name, "state", state, "detail", detail)
}

func (l *link) setVersion(v string) {
	l.mgr.mu.Lock()
	defer l.mgr.mu.Unlock()
	l.version = v
}

// start launches the connect loop. Caller holds mgr.mu.
func (l *link) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.state, l.detail, l.reason, l.since = StateConnecting, "", "", time.Now()
	go l.loop(ctx)
}

// stop ends the loop and marks the machine off. Caller holds mgr.mu.
func (l *link) stop() {
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.state, l.detail, l.reason, l.since = StateOff, "", "", time.Now()
	l.proxy, l.mcpBase, l.ctlBase, l.token = nil, "", "", ""
}

const (
	probeTimeout   = 25 * time.Second
	installTimeout = 5 * time.Minute
	startTimeout   = 20 * time.Second
	healthTimeout  = 10 * time.Second
)

// Retry pacing; variables so tests do not wait on them.
var (
	minBackoff = 2 * time.Second
	maxBackoff = 30 * time.Second
)

// loop keeps one machine connected until ctx ends: probe, install if asked,
// start the remote Companion if it is not running, forward, verify, then
// wait for the session to drop and start over with backoff.
func (l *link) loop(ctx context.Context) {
	backoff := minBackoff
	for {
		err := l.attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			l.setWithReason(ctx, StateError, err.Error(), reasonOf(err))
		} else {
			// A session that was up and dropped reconnects promptly.
			backoff = minBackoff
			l.set(ctx, StateError, "connection lost; reconnecting")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// attempt runs one connection from probe to drop. A nil return means the
// session was established and later ended; an error means it never was.
func (l *link) attempt(ctx context.Context) error {
	l.set(ctx, StateConnecting, "")
	p, err := l.probe(ctx)
	if err != nil {
		return err
	}
	if !compatible(p.version, l.mgr.version) {
		if p.version == "" {
			l.set(ctx, StateMissing, "Fylane is not installed on this machine")
		} else {
			l.setVersion(p.version)
			l.set(ctx, StateMissing, fmt.Sprintf("this machine runs Fylane %s; this app is %s", p.version, l.mgr.version))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.install:
		}
		l.set(ctx, StateInstalling, "")
		if err := l.doInstall(ctx); err != nil {
			return fmt.Errorf("install failed: %w", err)
		}
		if p, err = l.probe(ctx); err != nil {
			return err
		}
		if !compatible(p.version, l.mgr.version) {
			return fmt.Errorf("installed, but the machine reports Fylane %q", p.version)
		}
	}
	l.setVersion(p.version)

	// A control file may be left by a Companion that is no longer running
	// (a reboot, a crash). The forward decides: if the control API does not
	// answer through it, the Companion is started and the file re-read.
	sess, err := l.bring(ctx, p.control)
	if errors.Is(err, errNotAnswering) || errors.Is(err, errNoControl) {
		l.set(ctx, StateStarting, "")
		if err := l.doStart(ctx, p.control); err != nil {
			return err
		}
		if p, err = l.probe(ctx); err != nil {
			return err
		}
		sess, err = l.bring(ctx, p.control)
	}
	if err != nil {
		return err
	}
	defer sess.link.Close()

	l.mgr.mu.Lock()
	if ctx.Err() == nil {
		l.proxy = sess.proxy
		l.mcpBase = sess.mcpBase
		l.ctlBase, l.token = sess.ctlBase, sess.token
	}
	l.mgr.mu.Unlock()
	l.set(ctx, StateOnline, "")

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sess.link.Done():
		return nil
	}
}

var (
	errNoControl    = errors.New("no control file")
	errNotAnswering = errors.New("the remote control API did not answer")
)

// remoteControl is the remote Companion's control.json.
type remoteControl struct {
	Addr    string `json:"addr"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	MCPAddr string `json:"mcp_addr"`
}

type probeResult struct {
	version string
	control *remoteControl
}

// remoteHome is where the Companion lives on a remote machine. Fixed so
// nothing here has to guess a config directory over ssh.
const remoteHome = "$HOME/.fylane"

const probeScript = `b="` + remoteHome + `/bin/fylane-companion"
if [ -x "$b" ]; then echo "version $("$b" version 2>/dev/null)"; else echo "version none"; fi
c="` + remoteHome + `/data/control.json"
if [ -f "$c" ]; then echo "control $(cat "$c")"; else echo "control none"; fi
`

func (l *link) probe(ctx context.Context) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, stderr, err := l.mgr.dial.Run(ctx, l.m, probeScript)
	if err != nil {
		return probeResult{}, explain(err, stderr)
	}
	var p probeResult
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "version "):
			// "fylane-companion 0.0.4" or "none".
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[len(fields)-1] != "none" {
				p.version = fields[len(fields)-1]
			}
		case strings.HasPrefix(line, "control "):
			raw := strings.TrimPrefix(line, "control ")
			if raw == "none" {
				continue
			}
			var c remoteControl
			if err := json.Unmarshal([]byte(raw), &c); err == nil && c.Addr != "" && c.Token != "" {
				p.control = &c
			}
		}
	}
	return p, nil
}

// installURL is where the install script for a pinned tag lives. The tag is
// this Companion's own version, never anything a caller supplied.
func installURL(tag string) string {
	return "https://raw.githubusercontent.com/leazoot/fylane/" + tag + "/scripts/install.sh"
}

func (l *link) doInstall(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	tag := "v" + baseVersion(l.mgr.version)
	script := `set -e
mkdir -p "` + remoteHome + `"
s="` + remoteHome + `/install.sh"
u="` + installURL(tag) + `"
if command -v curl >/dev/null 2>&1; then curl -fsSL -o "$s" "$u"; else wget -qO "$s" "$u"; fi
FYLANE_VERSION="` + tag + `" FYLANE_HOME="` + remoteHome + `" FYLANE_BIN="` + remoteHome + `/bin" sh "$s"
rm -f "$s"
`
	_, stderr, err := l.mgr.dial.Run(ctx, l.m, script)
	if err != nil {
		return explain(err, stderr)
	}
	return nil
}

// doStart launches the remote Companion detached and waits for it to write
// a control file that differs from the stale one, if there was one.
func (l *link) doStart(ctx context.Context, stale *remoteControl) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	script := `mkdir -p "` + remoteHome + `/data"
cd "$HOME"
nohup "` + remoteHome + `/bin/fylane-companion" serve -data-dir "` + remoteHome + `/data" -addr 127.0.0.1:0 >> "` + remoteHome + `/serve.log" 2>&1 < /dev/null &
`
	if _, stderr, err := l.mgr.dial.Run(ctx, l.m, script); err != nil {
		return fmt.Errorf("starting Fylane on %s: %w", l.m.Name, explain(err, stderr))
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("Fylane did not start on %s", l.m.Name)
		case <-time.After(500 * time.Millisecond):
		}
		p, err := l.probe(ctx)
		if err != nil {
			return err
		}
		if p.control != nil && (stale == nil || p.control.PID != stale.PID || p.control.Addr != stale.Addr) {
			return nil
		}
	}
}

// session is one established set of forwards.
type session struct {
	link    Link
	proxy   *httputil.ReverseProxy
	mcpBase string
	ctlBase string
	token   string
}

// bring opens the forwards for a control file and checks that the control
// API answers through them.
func (l *link) bring(ctx context.Context, c *remoteControl) (*session, error) {
	if c == nil {
		return nil, errNoControl
	}
	ctlPort, err := freePort()
	if err != nil {
		return nil, err
	}
	mcpPort, err := freePort()
	if err != nil {
		return nil, err
	}
	forwards := []Forward{{LocalPort: ctlPort, RemoteAddr: c.Addr}}
	if c.MCPAddr != "" {
		forwards = append(forwards, Forward{LocalPort: mcpPort, RemoteAddr: c.MCPAddr})
	}
	lk, err := l.mgr.dial.Forward(ctx, l.m, forwards)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", ctlPort)
	if err := health(ctx, lk, base, c.Token); err != nil {
		lk.Close()
		return nil, err
	}
	target, _ := url.Parse(base)
	token := c.Token
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "the machine did not answer", http.StatusBadGateway)
		},
	}
	s := &session{link: lk, proxy: proxy, ctlBase: base, token: token}
	if c.MCPAddr != "" {
		s.mcpBase = fmt.Sprintf("http://127.0.0.1:%d", mcpPort)
	}
	return s, nil
}

// health waits for the forwarded control API to answer /v1/status. Two
// failures look alike from HTTP and mean opposite things: ssh has not bound
// the local port yet (keep waiting), or it has and the remote side refuses,
// which is what a control file from a dead Companion looks like (give up
// and start one). A plain TCP dial tells them apart.
func health(ctx context.Context, lk Link, base, token string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second}
	refused := 0
	for {
		if c, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), time.Second); err == nil {
			c.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/status", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
				if resp.StatusCode == http.StatusUnauthorized {
					return errNotAnswering
				}
			}
			if refused++; refused >= 3 {
				return errNotAnswering
			}
		}
		select {
		case <-ctx.Done():
			return errNotAnswering
		case <-lk.Done():
			return errors.New("the ssh session ended")
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// baseVersion strips a -dev (or any pre-release) suffix.
func baseVersion(v string) string {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[:i]
	}
	return v
}

// compatible is whether a remote version can be driven by this one. Exact
// base versions only: the control API and the tool set are not versioned
// separately, so anything else is a guess.
func compatible(remote, local string) bool {
	return remote != "" && baseVersion(remote) == baseVersion(local)
}

// Failure is one ssh outcome the user can act on, with a code the window
// can put into its own words and the sentence the Core would say.
type Failure struct {
	Code string
	Msg  string
}

func (f *Failure) Error() string { return f.Msg }

// Reason codes. Anything else is "" and the detail is the only reading.
const (
	ReasonHostKey     = "host_key"
	ReasonAuth        = "auth"
	ReasonResolve     = "resolve"
	ReasonUnreachable = "unreachable"
)

// explain turns ssh's stderr into the one sentence the user needs. The
// raw text is kept after it: a diagnosis should never hide its evidence.
func explain(err error, stderr string) error {
	s := strings.TrimSpace(stderr)
	last := s
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		last = strings.TrimSpace(s[i+1:])
	}
	switch {
	case strings.Contains(s, "Host key verification failed"):
		return &Failure{ReasonHostKey, "this machine is not in known_hosts yet; ssh to it once from a terminal"}
	case strings.Contains(s, "Permission denied"):
		return &Failure{ReasonAuth, "ssh refused the login; key-based login is required (no password prompts here)"}
	case strings.Contains(s, "Could not resolve hostname"):
		return &Failure{ReasonResolve, "could not resolve the host name"}
	case strings.Contains(s, "Connection refused"), strings.Contains(s, "Connection timed out"),
		strings.Contains(s, "No route to host"), strings.Contains(s, "Operation timed out"):
		return &Failure{ReasonUnreachable, "could not reach the machine: " + last}
	case last != "":
		return fmt.Errorf("%s", last)
	default:
		return err
	}
}

// reasonOf is the code behind an error, if it has one.
func reasonOf(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}
