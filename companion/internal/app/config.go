package app

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/leazoot/fylane/companion/internal/devicecred"
	"github.com/leazoot/fylane/companion/internal/tunnelproc"
)

// Config is the resolved companion configuration. Secrets are not part of it:
// the tunnel token is read from FYLANE_TUNNEL_TOKEN at connect time and never
// stored or logged.
type Config struct {
	// DataDir holds the database, state file, and instance lock.
	DataDir string
	// Addr is the local MCP listen address.
	Addr string
	// RelayURL, when set, connects the outbound tunnel (ws:// or wss://).
	RelayURL string
	// DirectAddr, when set, serves the public OAuth + MCP surface from this
	// process on that loopback address, for a tunnel to publish (direct mode).
	// Mutually exclusive with RelayURL: two ways in would be two things to
	// secure for one machine.
	DirectAddr string
	// PublicURL is the base URL the tunnel exposes DirectAddr at. It may be
	// empty at startup — a quick tunnel only names it once it is running.
	PublicURL string
	// TunnelProvider names the tunnel serve starts for direct mode; empty
	// means the user runs one themselves. TunnelHostname and TunnelDomain are
	// that provider's settings.
	TunnelProvider string
	TunnelHostname string
	TunnelDomain   string
	// Workspace optionally registers this root directory on startup and
	// selects it as the current workspace.
	Workspace string
	// EnableProbes exposes the wait_probe/payload_probe diagnostic tools.
	// Never enabled by default.
	EnableProbes bool
	// ApprovalMode selects the approval policy: "safe" (default) or
	// "balanced". There is no mode that approves everything.
	ApprovalMode string
	// UpdateManifestURL, when set, enables the daily notice-only update
	// check. Empty disables it entirely.
	UpdateManifestURL string
	// MaxInlineBytes caps content returned inline by one MCP read; zero
	// keeps the server default (1 MiB).
	MaxInlineBytes int
	// LogLevel is the minimum level for structured logs.
	LogLevel slog.Level
}

// ParseArgs parses `serve` command-line arguments into a Config.
func ParseArgs(args []string) (*Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfg := &Config{}
	var dataDir, level string
	fs.StringVar(&dataDir, "data-dir", "", "data directory (default: user config dir + /fylane)")
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:8787", "local MCP listen address")
	fs.StringVar(&cfg.RelayURL, "relay", "", "relay tunnel endpoint (ws:// or wss://); requires FYLANE_TUNNEL_TOKEN")
	fs.StringVar(&cfg.DirectAddr, "direct-addr", "", "serve the public OAuth + MCP surface here (loopback) for a tunnel to publish; direct mode, no relay")
	fs.StringVar(&cfg.PublicURL, "public-url", "", "public base URL the tunnel exposes -direct-addr at (direct mode)")
	fs.StringVar(&cfg.TunnelProvider, "tunnel", "", "tunnel to start for direct mode: cloudflare-quick, cloudflare-named, tailscale-funnel, ngrok (default: stored setting)")
	fs.StringVar(&cfg.Workspace, "workspace", "", "register this directory and select it as the current workspace")
	fs.BoolVar(&cfg.EnableProbes, "probe", false, "expose diagnostic probe tools (development only)")
	fs.StringVar(&cfg.ApprovalMode, "approval-mode", "", "approval policy: safe or balanced (default: stored setting, else safe)")
	fs.StringVar(&cfg.UpdateManifestURL, "update-manifest", "", "https URL of the release manifest for the daily update notice (empty = disabled)")
	fs.IntVar(&cfg.MaxInlineBytes, "max-inline-bytes", 0, "cap on inline content bytes per MCP read (0 = 1 MiB default)")
	fs.StringVar(&level, "log-level", "info", "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	if dataDir == "" {
		var err error
		if dataDir, err = DefaultDataDir(); err != nil {
			return nil, err
		}
	}
	cfg.DataDir = dataDir

	// The local MCP listener is unauthenticated by design (remote callers
	// come in through the relay tunnel), so it must never bind beyond
	// loopback.
	if err := requireLoopback(cfg.Addr); err != nil {
		return nil, fmt.Errorf("-addr: %w", err)
	}

	// Without the connection flags the stored settings apply, so an
	// auto-started Core keeps the connection and policy the user chose. A
	// relay flag suppresses the stored direct-mode settings and vice versa:
	// the two modes are alternatives, and a flag is the more recent intent.
	if cfg.RelayURL == "" && cfg.DirectAddr == "" || cfg.ApprovalMode == "" {
		s, err := loadSettings(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		if cfg.RelayURL == "" && cfg.DirectAddr == "" {
			// Both configurations are kept so switching back does not mean
			// pairing again; the stored mode says which one is live. A config
			// written before the connect screen has no mode, and there only
			// one of the two is ever filled in.
			switch s.Mode {
			case ModeRelay:
				cfg.RelayURL = s.RelayURL
			case ModeDirect:
				cfg.DirectAddr = s.DirectAddr
				if cfg.DirectAddr == "" {
					cfg.DirectAddr = DefaultDirectAddr
				}
			default:
				cfg.RelayURL, cfg.DirectAddr = s.RelayURL, s.DirectAddr
			}
			if cfg.PublicURL == "" && cfg.DirectAddr != "" {
				cfg.PublicURL = s.PublicURL
			}
		}
		if cfg.ApprovalMode == "" {
			cfg.ApprovalMode = s.ApprovalMode
		}
		// Tunnel settings belong to direct mode. Loading them in relay mode
		// would fail the "-tunnel needs direct mode" check below on a machine
		// that merely used direct mode once.
		if cfg.DirectAddr != "" {
			if cfg.TunnelProvider == "" {
				cfg.TunnelProvider = s.TunnelProvider
			}
			cfg.TunnelHostname, cfg.TunnelDomain = s.TunnelHostname, s.TunnelDomain
		}
	}
	if cfg.RelayURL != "" && cfg.DirectAddr != "" {
		return nil, fmt.Errorf("-relay and -direct-addr are alternatives: a relay forwards to this machine, direct mode is this machine")
	}
	if cfg.RelayURL != "" {
		if err := validateRelayURL(cfg.RelayURL); err != nil {
			return nil, err
		}
	}
	if cfg.DirectAddr != "" {
		// Same rule as the MCP listener: the tunnel connects locally, so
		// binding wider only widens what is exposed without it.
		if err := requireLoopback(cfg.DirectAddr); err != nil {
			return nil, fmt.Errorf("-direct-addr: %w", err)
		}
		if cfg.DirectAddr == cfg.Addr {
			return nil, fmt.Errorf("-direct-addr must differ from -addr: the local MCP listener is unauthenticated and must never be published")
		}
	}
	if err := validatePublicURL(cfg.PublicURL); err != nil {
		return nil, err
	}
	if cfg.PublicURL != "" && cfg.DirectAddr == "" {
		return nil, fmt.Errorf("-public-url only applies to direct mode; pass -direct-addr as well")
	}
	if cfg.TunnelProvider != "" {
		if _, ok := tunnelproc.Lookup(tunnelproc.Kind(cfg.TunnelProvider)); !ok {
			return nil, fmt.Errorf("unknown -tunnel %q", cfg.TunnelProvider)
		}
		if cfg.DirectAddr == "" {
			return nil, fmt.Errorf("-tunnel only applies to direct mode; pass -direct-addr as well")
		}
	}

	if cfg.UpdateManifestURL != "" && !strings.HasPrefix(cfg.UpdateManifestURL, "https://") {
		return nil, fmt.Errorf("-update-manifest must be an https:// URL")
	}

	switch strings.ToLower(level) {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid -log-level %q", level)
	}
	return cfg, nil
}

// validateRelayURL enforces the tunnel endpoint rules shared by the -relay
// flag and the persisted setting: ws:// or wss:// only, and plaintext ws://
// exclusively for loopback relays — anything else would expose the tunnel
// credential and all file traffic on the network.
func validateRelayURL(relayURL string) error {
	if !strings.HasPrefix(relayURL, "ws://") && !strings.HasPrefix(relayURL, "wss://") {
		return fmt.Errorf("relay endpoint %q must be a ws:// or wss:// URL", relayURL)
	}
	if u, err := url.Parse(relayURL); err != nil || (u.Scheme == "ws" && !isLoopbackHost(u.Hostname())) {
		return fmt.Errorf("relay endpoint %q: ws:// is only allowed for loopback relays; use wss://", relayURL)
	}
	return nil
}

// validatePublicURL enforces the direct-mode public base URL: https only,
// with plain http allowed exclusively for loopback (local testing). Anything
// else would carry OAuth codes and file traffic in the clear. An empty value
// is valid — it means no tunnel has named this machine yet.
func validatePublicURL(publicURL string) error {
	if publicURL == "" {
		return nil
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("public URL %q must be an absolute https:// URL", publicURL)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("public URL %q: http:// is only allowed for loopback; use https://", publicURL)
}

// requireLoopback rejects listen addresses that would expose the local MCP
// server beyond this machine.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("listen address %q is not loopback; the MCP listener is local-only (remote access goes through the relay tunnel)", addr)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// TunnelToken resolves the tunnel credential: FYLANE_TUNNEL_TOKEN (legacy
// shared-token self-hosting) takes precedence, otherwise the device
// credentials stored in the OS keychain by `fylane-companion pair`. There is
// no unauthenticated mode.
func (c *Config) TunnelToken() (string, error) {
	if token := os.Getenv("FYLANE_TUNNEL_TOKEN"); token != "" {
		return token, nil
	}
	creds, err := devicecred.Load(c.RelayURL)
	if err != nil {
		return "", err
	}
	return creds.TunnelToken(), nil
}
