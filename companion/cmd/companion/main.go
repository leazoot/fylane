package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/termapprove"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/leazoot/fylane/companion/internal/app"
	"github.com/leazoot/fylane/companion/internal/connectinfo"
	"github.com/leazoot/fylane/companion/internal/crashlog"
	"github.com/leazoot/fylane/companion/internal/devicecred"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/shared/buildinfo"
)

var version = buildinfo.Version

func main() {
	// The read-boundary shim comes before everything, including argument
	// parsing. This process was started only to restrict itself and become
	// another program (readbox); anything it did first would run outside the
	// boundary it exists to apply.
	if readbox.IsShim(os.Args) {
		if err := readbox.RunShim(os.Args); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("fylane-companion", version)
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
	case "pair":
		if err := pair(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
	case "share":
		if err := share(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
	case "direct":
		if err := direct(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
	case "connect":
		if err := connect(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
	case "diagnostics":
		if err := diagnostics(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fylane-companion: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

// diagnostics writes a shareable crash-log bundle: crash captures and
// build info only — never tokens, databases, or workspace content. Sharing
// is entirely the user's manual choice.
func diagnostics(args []string) error {
	fs := flag.NewFlagSet("diagnostics", flag.ContinueOnError)
	out := fs.String("out", "fylane-diagnostics.zip", "output bundle path (must not exist)")
	dataDir := fs.String("data-dir", "", "companion data directory (default: user config dir + /fylane)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		var err error
		if *dataDir, err = defaultDataDir(); err != nil {
			return err
		}
	}
	if err := crashlog.Diagnostics(*dataDir, version, *out); err != nil {
		return err
	}
	fmt.Printf("diagnostics bundle written to %s\n", *out)
	fmt.Println("it contains crash logs and build info only; review before sharing")
	return nil
}

// defaultDataDir mirrors the serve default; FYLANE_DATA_DIR overrides it
// (same convention as the desktop shell) for non-default setups and tests.
func defaultDataDir() (string, error) {
	if dir := os.Getenv("FYLANE_DATA_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determining data directory: %w", err)
	}
	return filepath.Join(base, "fylane"), nil
}

// pair registers this device with a relay (storing the credentials in the
// OS keychain) and prints a pairing code to enter on the platform's OAuth
// consent page.
func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	relay := fs.String("relay", "", "relay base URL (https://relay.example)")
	name := fs.String("name", "", "device display name (default: hostname)")
	register := fs.Bool("register", false, "force fresh device registration even when credentials exist")
	dataDir := fs.String("data-dir", "", "data directory (default: user config dir + /fylane)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *relay == "" {
		return fmt.Errorf("-relay is required")
	}
	if *name == "" {
		if host, err := os.Hostname(); err == nil {
			*name = host
		}
	}
	ctx := context.Background()
	if _, err := devicecred.Load(*relay); err != nil || *register {
		if _, err := devicecred.Register(ctx, *relay, *name); err != nil {
			return err
		}
		fmt.Println("device registered with relay and credentials stored in the OS keychain")
	}

	// Persist the tunnel endpoint so every later serve — including the one
	// the desktop shell auto-starts — connects without a -relay flag.
	if *dataDir == "" {
		var err error
		if *dataDir, err = app.DefaultDataDir(); err != nil {
			return err
		}
	}
	tunnelURL, err := app.TunnelURLFromBase(*relay)
	if err != nil {
		return err
	}
	if err := app.SaveRelayURL(*dataDir, tunnelURL); err != nil {
		return err
	}
	fmt.Printf("relay saved: serve connects to %s automatically\n", tunnelURL)

	code, expiresIn, err := devicecred.PairingCode(ctx, *relay)
	if err != nil {
		return err
	}
	fmt.Printf("pairing code: %s (valid for %s)\n", code, expiresIn)
	fmt.Println("enter this code on the platform's connection page to bind it to this device")
	return nil
}

// direct switches this machine to direct mode: serve runs the OAuth and MCP
// surface itself on a loopback port, and a tunnel publishes it. No relay is
// involved, so there is no device registration and no keychain credential —
// pairing codes come from the desktop app or `serve`'s control API.
func direct(args []string) error {
	fs := flag.NewFlagSet("direct", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8788", "loopback address the tunnel publishes")
	publicURL := fs.String("public-url", "", "public https base URL the tunnel exposes (may be set later)")
	off := fs.Bool("off", false, "turn direct mode off")
	dataDir := fs.String("data-dir", "", "data directory (default: user config dir + /fylane)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		var err error
		if *dataDir, err = app.DefaultDataDir(); err != nil {
			return err
		}
	}
	if *off {
		if err := app.SaveDirect(*dataDir, "", ""); err != nil {
			return err
		}
		fmt.Println("direct mode off")
		return nil
	}
	if err := app.SaveDirect(*dataDir, *addr, *publicURL); err != nil {
		return err
	}
	fmt.Printf("direct mode on: serve publishes %s\n", *addr)
	if *publicURL == "" {
		fmt.Println("no public URL yet — start a tunnel to this address, then rerun with -public-url")
		return nil
	}
	fmt.Printf("connector URL for the platform: %s/mcp\n", strings.TrimRight(*publicURL, "/"))
	return nil
}

// connect preflights a relay and prints the connector URL and per-platform
// onboarding steps. It exits non-zero when the relay is not ready so the
// check can gate scripted setups.
func connect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	relay := fs.String("relay", "", "relay base URL (https://relay.example)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *relay == "" {
		return fmt.Errorf("-relay is required")
	}
	report, err := connectinfo.Inspect(context.Background(), *relay)
	if err != nil {
		return err
	}
	if creds, err := devicecred.Load(*relay); err == nil {
		report.DeviceID = creds.DeviceID
	} else {
		report.PairHint = fmt.Sprintf("run `fylane-companion pair -relay %s`", *relay)
	}
	connectinfo.Render(os.Stdout, report)
	if len(report.Problems) > 0 {
		return fmt.Errorf("preflight found %d problem(s)", len(report.Problems))
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  fylane-companion share   [<dir>] [-direct-addr 127.0.0.1:8788] [-data-dir <dir>]
                           (one command, no account: publishes <dir> (default:
                           the current directory) through a quick tunnel and
                           prints the connector URL and a pairing code)
  fylane-companion serve   [-workspace <dir>] [-addr 127.0.0.1:8787] [-relay wss://host/tunnel]
                           [-data-dir <dir>] [-approval-mode safe] [-log-level info]
                           [-update-manifest <https url>]
  fylane-companion pair    -relay <url> [-name <device name>] [-register] [-data-dir <dir>]
                           (also persists the relay so `+"`serve`"+` needs no -relay flag)
  fylane-companion direct  [-addr 127.0.0.1:8788] [-public-url <https url>] [-off]
                           (direct mode: serve publishes its own OAuth + MCP
                           surface for a tunnel; no relay, no device pairing)
  fylane-companion connect -relay <url>
  fylane-companion diagnostics [-out fylane-diagnostics.zip]
  fylane-companion version

The -relay flag connects outbound to a Fylane relay using the device
credentials stored by `+"`pair`"+` (or FYLANE_TUNNEL_TOKEN in legacy
shared-token mode).`)
}

func serve(args []string) error {
	cfg, err := app.ParseArgs(args)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := app.New(cfg, logger)
	a.Ask = terminalApprover()
	return a.Run(ctx)
}

// terminalApprover answers approvals on stdin when stdin is a terminal — a
// person is at this command — and leaves them to the desktop app otherwise.
// A service unit or a pipe has nobody to type y, and a prompt that could
// never be answered would only hide the request from the window that can.
func terminalApprover() func(*approval.Pending, func(string, bool, string) bool) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return termapprove.New(os.Stdin, os.Stdout).Ask
}
