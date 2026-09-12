package machines

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Forward is one local port that ssh carries to an address on the machine's
// loopback.
type Forward struct {
	LocalPort  int
	RemoteAddr string
}

// Link is an open set of forwards. Done closes when the session ends for any
// reason; Close ends it.
type Link interface {
	Done() <-chan struct{}
	Close()
}

// Dialer is what the Manager needs from ssh: run a script on a machine, and
// hold forwards open. Tests substitute it; production is the system ssh.
type Dialer interface {
	// Run feeds script to `sh -s` on the machine and returns what it printed.
	Run(ctx context.Context, m Machine, script string) (stdout, stderr string, err error)
	// Forward opens the forwards and returns once ssh is running. Whether
	// the remote side answers is the caller's to check.
	Forward(ctx context.Context, m Machine, forwards []Forward) (Link, error)
}

// sshDialer is the system ssh.
type sshDialer struct {
	// bin overrides the ssh program; empty means "ssh" on PATH.
	bin string
}

func (d *sshDialer) program() string {
	if d.bin != "" {
		return d.bin
	}
	return "ssh"
}

// baseArgs is what every invocation gets. BatchMode is the one that matters:
// with it ssh fails instead of prompting, so no call here can ever block on
// a password or a host-key question.
func baseArgs(m Machine) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if m.Port > 0 {
		args = append(args, "-p", strconv.Itoa(m.Port))
	}
	target := m.Host
	if m.User != "" {
		target = m.User + "@" + m.Host
	}
	return append(args, target)
}

func (d *sshDialer) Run(ctx context.Context, m Machine, script string) (string, string, error) {
	args := append(baseArgs(m), "sh", "-s")
	cmd := exec.CommandContext(ctx, d.program(), args...)
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	hideConsole(cmd)
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func (d *sshDialer) Forward(ctx context.Context, m Machine, forwards []Forward) (Link, error) {
	args := []string{"-N", "-o", "ExitOnForwardFailure=yes"}
	for _, f := range forwards {
		args = append(args, "-L", fmt.Sprintf("127.0.0.1:%d:%s", f.LocalPort, f.RemoteAddr))
	}
	args = append(args, baseArgs(m)...)
	cmd := exec.CommandContext(ctx, d.program(), args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ssh: %w", err)
	}
	l := &procLink{done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		l.mu.Lock()
		l.err = explain(err, errb.String())
		l.mu.Unlock()
		close(l.done)
	}()
	l.kill = func() { _ = cmd.Process.Kill() }
	return l, nil
}

type procLink struct {
	done chan struct{}
	kill func()
	mu   sync.Mutex
	err  error
}

func (l *procLink) Done() <-chan struct{} { return l.done }

func (l *procLink) Close() {
	select {
	case <-l.done:
	default:
		l.kill()
	}
}
