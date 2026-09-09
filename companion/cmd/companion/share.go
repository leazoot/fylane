package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leazoot/fylane/companion/internal/app"
	"github.com/leazoot/fylane/companion/internal/tunnelget"
	"github.com/leazoot/fylane/companion/internal/tunnelproc"
)

// share is the shortest path from a fresh checkout to a working endpoint:
// one command, one folder, no account anywhere.
//
// The competitor spells the same idea `npx <package> share`. This gets to the
// same place by a different route: a tunnel the machine already has — from
// the Fylane package, from an earlier download, or from PATH — and when there
// is none, an offer to fetch the one build this release pinned. The offer is
// a question with a default of no; it is never a fetch that happens
// because nobody objected.
//
// Everything else it does, it does by reusing what the connect screen already
// uses: the same direct-mode switch, the same tunnel manager, the same pairing
// codes. It is a different front door onto the same house, not a second house.
func share(args []string) error {
	fs := flag.NewFlagSet("share", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory (default: user config dir + /fylane)")
	addr := fs.String("direct-addr", app.DefaultDirectAddr, "loopback address the tunnel publishes")
	level := fs.String("log-level", "warn", "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("share takes at most one directory, got %d arguments", fs.NArg())
	}

	dir, err := shareDir(fs.Arg(0))
	if err != nil {
		return err
	}
	if *dataDir == "" {
		if *dataDir, err = app.DefaultDataDir(); err != nil {
			return err
		}
	}
	// Checked before anything is stored: a machine that cannot publish itself
	// should not be left configured as though it could.
	quick, ok := tunnelproc.Lookup(tunnelproc.CloudflareQuick)
	if !ok {
		return fmt.Errorf("the quick tunnel provider is missing from this build")
	}
	tunnelproc.SetDownloadDir(tunnelget.Dir(*dataDir))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, installed := quick.Available(); !installed {
		if err := offerTunnelDownload(ctx, os.Stdin, os.Stdout, quick, *dataDir); err != nil {
			return err
		}
	}
	if err := app.SwitchToDirect(*dataDir, *addr, string(quick.Kind), "", ""); err != nil {
		return err
	}

	cfg, err := app.ParseArgs([]string{
		"-workspace", dir,
		"-data-dir", *dataDir,
		"-log-level", *level,
	})
	if err != nil {
		return err
	}

	fmt.Printf("sharing %s — starting a tunnel, this takes a few seconds\n", filepath.Base(dir))
	a := app.New(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel})))
	a.Ready = (&shareAnnouncer{ctx: ctx, out: os.Stdout}).announce
	return a.Run(ctx)
}

// offerTunnelDownload asks whether to fetch the pinned tunnel binary, and
// fetches it only on a yes.
//
// Everything about the shape of this function is the consent requirement in
// It prints what would be fetched, from where, and at which digest,
// before asking. It reads one line and treats anything that is not a plain
// yes as a no — including end of input, which is what a pipe gives, so a
// scripted run gets the refusal rather than a download nobody watched.
func offerTunnelDownload(ctx context.Context, in io.Reader, out io.Writer, p tunnelproc.Provider, dataDir string) error {
	pin, err := tunnelget.PinFor(p.Binary, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("%s is not installed and this build has no pinned one for your platform: %s",
			p.Binary, p.Install)
	}

	yes, err := consented(in, out, p, pin)
	if err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("not downloading %s; install it yourself and run this again: %s", p.Binary, p.Install)
	}

	fmt.Fprintf(out, "\ndownloading %s %s\n", p.Binary, pin.Version)
	g := &tunnelget.Getter{}
	if _, err := g.Fetch(ctx, pin, tunnelget.Dir(dataDir)); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s is ready\n", p.Binary, pin.Version)
	return nil
}

// consented prints what would arrive and reads the answer. It is separate
// from the download so that what counts as a yes can be checked without a
// network in the room.
//
// Anything that is not a plain yes is a no, end of input included — that is
// what a pipe gives, so a scripted run gets the refusal rather than a
// download nobody watched.
func consented(in io.Reader, out io.Writer, p tunnelproc.Provider, pin tunnelget.Pin) (bool, error) {
	fmt.Fprintf(out, `%s is not on this machine, and Fylane needs it to publish a tunnel.

  Program   %s %s
  From      %s
  SHA-256   %s

It goes in Fylane's own data directory, not on your PATH, and Fylane checks
the digest above before it runs anything. Alternatively install it yourself:
%s

Download it now? [y/N] `, p.Binary, p.Binary, pin.Version, pin.URL, pin.SHA256, p.Install)

	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("could not read your answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// shareDir resolves the folder to share. An empty argument means the current
// directory, which is what someone typing this in a project expects.
func shareDir(arg string) (string, error) {
	if arg == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determining the current directory: %w", err)
		}
		arg = wd
	}
	dir, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", arg, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("cannot share %q: %w", filepath.Base(dir), err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cannot share %q: it is a file, and a workspace is a folder", filepath.Base(dir))
	}
	return dir, nil
}

// shareAnnouncer prints what a person needs to connect, and keeps the pairing
// code on screen valid.
type shareAnnouncer struct {
	ctx  context.Context
	out  io.Writer
	once sync.Once
}

func (s *shareAnnouncer) announce(connectorURL string, mint func(context.Context) (string, time.Duration, error)) {
	fmt.Fprintf(s.out, "\n  Connector URL   %s\n", connectorURL)
	// One refresh loop, however many addresses the tunnel announces: the code
	// is issued by this process, not by whatever hostname is pointing at it.
	s.once.Do(func() { go s.keepCodeValid(mint) })
}

// keepCodeValid prints a pairing code and replaces it when it expires. A code
// is the whole authorization in this mode — there is no desktop window here to
// approve from — so an expired one on screen would be the command quietly
// ceasing to work.
func (s *shareAnnouncer) keepCodeValid(mint func(context.Context) (string, time.Duration, error)) {
	first := true
	for {
		code, ttl, err := mint(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil {
				fmt.Fprintf(s.out, "  could not issue a pairing code: %v\n", err)
			}
			return
		}
		fmt.Fprintf(s.out, "  Pairing code    %s   (valid for %s)\n", code, ttl.Round(time.Second))
		if first {
			fmt.Fprint(s.out, shareHelp)
			first = false
		}
		timer := time.NewTimer(ttl)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

const shareHelp = `
Add the connector URL to ChatGPT, Claude or Grok as a custom connector, then
enter the pairing code when it asks. A fresh code prints here when this one
expires.

Files stay on this machine: the platform asks, Fylane answers, and every write
and every command stops here for your approval first. Ctrl-C stops sharing.
`
