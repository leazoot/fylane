package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/leazoot/fylane/companion/internal/lsp"
	"github.com/leazoot/fylane/companion/internal/machines"
	"github.com/leazoot/fylane/companion/internal/mcpgate"
	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/tunnelproc"
)

// Persisted user-level settings (config.json in the data dir). Secrets never
// live here — the tunnel credential stays in the OS keychain. Precedence is
// CLI flag over stored setting over default, so an auto-started Core (no
// flags) still reaches the relay the user paired with.

const settingsFileName = "config.json"

type settings struct {
	// Mode is how this machine is reached: "relay" or "direct". It exists so
	// both configurations can be kept — switching back to a paired relay must
	// not mean pairing again — and so neither is ambiguous when both are
	// present. Empty means "whichever of the two below is filled in", which is
	// what configs written before the connect screen look like.
	Mode string `json:"mode,omitempty"`
	// RelayURL is the tunnel endpoint (ws:// or wss://) used when serve is
	// started without -relay. Written by `fylane-companion pair`.
	RelayURL string `json:"relay_url,omitempty"`
	// ApprovalMode is the persisted approval policy ("safe" or "balanced"),
	// written when the desktop Safety page flips it. Empty means safe.
	ApprovalMode string `json:"approval_mode,omitempty"`
	// CommandRung is the persisted approval rung for commands ("strict",
	// "workspace", "open"). It is deliberately separate from ApprovalMode
	//: writes are transactional and reversible, commands are not, so
	// loosening one must not loosen the other. Empty means the default.
	CommandRung string `json:"command_rung,omitempty"`
	// DirectAddr and PublicURL configure direct mode: the Companion serves the
	// OAuth and MCP surface itself on DirectAddr (loopback) and a tunnel
	// publishes it at PublicURL. Empty DirectAddr means direct mode is off.
	DirectAddr string `json:"direct_addr,omitempty"`
	PublicURL  string `json:"public_url,omitempty"`
	// TunnelProvider is the tunnel serve starts for direct mode
	// ("cloudflare-quick", "tailscale-funnel"…); empty means the user runs
	// one themselves. Hostname and Domain are that provider's settings — the
	// token is a credential and lives in the OS keychain, never here.
	TunnelProvider string `json:"tunnel_provider,omitempty"`
	TunnelHostname string `json:"tunnel_hostname,omitempty"`
	TunnelDomain   string `json:"tunnel_domain,omitempty"`
	// TaskTimeoutSeconds is the ceiling the user set on how long a command
	// may run. It bounds what a caller may ask for; it is not a default the
	// caller can raise. Zero means the engine's own default.
	TaskTimeoutSeconds int `json:"task_timeout_seconds,omitempty"`
	// AllowStopTasks is whether the desktop offers a stop button on a running
	// task. A pointer because "never chosen" and "chosen: no" are different
	// answers and the default is yes.
	AllowStopTasks *bool `json:"allow_stop_tasks,omitempty"`
	// Rules are the route rules in priority order (see rules.go).
	Rules []routerule.Rule `json:"rules,omitempty"`
	// MCPProviders are the local MCP servers the gateway may proxy to.
	// They live here, in a file only this machine's user can write, and
	// nowhere else: a provider is a program Fylane will start, so being able
	// to add one remotely would make the gateway a way to run anything.
	MCPProviders []mcpgate.Provider `json:"mcp_providers,omitempty"`
	// ReadBoundary is whether the kernel read boundary is applied to the
	// programs this Companion starts. A pointer because "never chosen"
	// and "chosen: no" are different answers and the default is yes. Turning
	// it off is not a bypass flag: the path sandbox, the rule table, the
	// approval rung and the audit log are unchanged either way, and the
	// setting is visible.
	ReadBoundary *bool `json:"read_boundary,omitempty"`
	// LanguageServers are the language servers code_navigate may start
	//. They live here for the same reason the provider list does: a
	// language server is a program this machine starts, and a caller that
	// could name one would be naming a program to run. Entries override the
	// built-in table by name; a machine that has gopls needs no entry at all.
	LanguageServers []lsp.Server `json:"language_servers,omitempty"`
	// AgentEnvPassthrough names the environment variables a delegated coding
	// agent may inherit, keyed by agent name ("codex"). Names only — the
	// values stay in the environment. It lives here for the same reason the
	// provider list does: a delegated agent is a program this machine starts,
	// and what it is handed is the local user's decision, not a caller's.
	AgentEnvPassthrough map[string][]string `json:"agent_env_passthrough,omitempty"`
	// Machines are the remote computers this Companion reaches over ssh.
	// Names, hosts and logins only: ssh keys and the remote control tokens
	// never live here.
	Machines []machines.Machine `json:"machines,omitempty"`
	// CurrentMachine is the machine the window stands on; "" is this
	// computer. It decides which folder workspace_info calls current.
	CurrentMachine string `json:"current_machine,omitempty"`
}

// MachineStore adapts the settings file to machines.Store.
type MachineStore struct{ DataDir string }

func (r MachineStore) Load() ([]machines.Machine, error) {
	s, err := loadSettings(r.DataDir)
	if err != nil {
		return nil, err
	}
	return s.Machines, nil
}

func (r MachineStore) LoadCurrent() (string, error) {
	s, err := loadSettings(r.DataDir)
	if err != nil {
		return "", err
	}
	return s.CurrentMachine, nil
}

func (r MachineStore) SaveCurrent(id string) error {
	s, err := loadSettings(r.DataDir)
	if err != nil {
		return err
	}
	s.CurrentMachine = id
	return saveSettings(r.DataDir, s)
}

func (r MachineStore) Save(list []machines.Machine) error {
	s, err := loadSettings(r.DataDir)
	if err != nil {
		return err
	}
	s.Machines = list
	return saveSettings(r.DataDir, s)
}

// LoadMCPProviders reads the configured local MCP providers. An unreadable or
// malformed settings file is reported rather than silently treated as "no
// providers": a gateway that quietly disappears is worse than one that says
// why it did.
func LoadMCPProviders(dataDir string) ([]mcpgate.Provider, error) {
	s, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	return s.MCPProviders, nil
}

// ReadBoundaryEnabled reports whether the kernel read boundary is on. An
// unreadable settings file leaves it on: the safe reading of "we could not
// tell" is the one that keeps the boundary, not the one that drops it.
func ReadBoundaryEnabled(dataDir string) bool {
	s, err := loadSettings(dataDir)
	if err != nil || s.ReadBoundary == nil {
		return true
	}
	return *s.ReadBoundary
}

// LoadLanguageServers reads the configured language servers. It fails the same
// way LoadMCPProviders does: a navigation surface that quietly disappears is
// harder to diagnose than one that says why.
func LoadLanguageServers(dataDir string) ([]lsp.Server, error) {
	s, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	return s.LanguageServers, nil
}

// LoadAgentEnvPassthrough reads the per-agent environment passthrough. It
// fails the same way LoadMCPProviders does rather than defaulting to empty: an
// agent that silently loses the variable it authenticates with looks like a
// broken agent, not like a settings file nobody could read.
func LoadAgentEnvPassthrough(dataDir string) (map[string][]string, error) {
	s, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	return s.AgentEnvPassthrough, nil
}

// Connection modes, as stored and as reported to the connect screen.
const (
	ModeRelay  = "relay"
	ModeDirect = "direct"
)

// DefaultDirectAddr is the loopback listener direct mode uses when the user
// switched from the desktop and never named one. It must differ from the
// local MCP listener: that one is unauthenticated and is never published.
const DefaultDirectAddr = "127.0.0.1:8788"

// SwitchToDirect stores direct mode and the tunnel that will publish it. The
// relay settings are left in place so switching back does not mean pairing
// again, and the public URL is cleared: the old address belonged to whatever
// was publishing this machine before.
func SwitchToDirect(dataDir, addr, provider, hostname, domain string) error {
	if addr == "" {
		addr = DefaultDirectAddr
	}
	if err := requireLoopback(addr); err != nil {
		return fmt.Errorf("direct address: %w", err)
	}
	if provider != "" {
		if _, ok := tunnelproc.Lookup(tunnelproc.Kind(provider)); !ok {
			return fmt.Errorf("unknown tunnel provider %q", provider)
		}
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.Mode = ModeDirect
	s.DirectAddr = addr
	s.PublicURL = ""
	s.TunnelProvider, s.TunnelHostname, s.TunnelDomain = provider, hostname, domain
	return saveSettings(dataDir, s)
}

// SwitchToRelay stores relay mode. It refuses when this machine never paired
// with a relay: there would be nothing to connect to.
func SwitchToRelay(dataDir string) error {
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	if s.RelayURL == "" {
		return fmt.Errorf("no relay is configured: run `fylane-companion pair -relay <url>` first")
	}
	s.Mode = ModeRelay
	return saveSettings(dataDir, s)
}

// StoredRelayURL reports the relay this machine paired with, if any, whatever
// mode it is running in — the connect screen offers it as a way back.
func StoredRelayURL(dataDir string) string {
	s, err := loadSettings(dataDir)
	if err != nil {
		return ""
	}
	return s.RelayURL
}

// SaveApprovalMode persists the approval policy chosen on the Safety page.
func SaveApprovalMode(dataDir, mode string) error {
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.ApprovalMode = mode
	return saveSettings(dataDir, s)
}

// SaveCommandRung persists the command approval rung.
func SaveCommandRung(dataDir, rung string) error {
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.CommandRung = rung
	return saveSettings(dataDir, s)
}

// LoadCommandRung reads the stored rung; an empty result means the default.
func LoadCommandRung(dataDir string) (string, error) {
	s, err := loadSettings(dataDir)
	if err != nil {
		return "", err
	}
	return s.CommandRung, nil
}

// DefaultDataDir resolves the default data directory shared by every
// subcommand (user config dir + /fylane).
func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determining data directory (set -data-dir): %w", err)
	}
	return filepath.Join(base, "fylane"), nil
}

func loadSettings(dataDir string) (settings, error) {
	var s settings
	raw, err := os.ReadFile(filepath.Join(dataDir, settingsFileName))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("reading %s: %w", settingsFileName, err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("parsing %s: %w", settingsFileName, err)
	}
	return s, nil
}

func saveSettings(dataDir string, s settings) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, settingsFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", settingsFileName, err)
	}
	return nil
}

// SaveRelayURL validates and persists the tunnel endpoint for future serves.
func SaveRelayURL(dataDir, relayURL string) error {
	if err := validateRelayURL(relayURL); err != nil {
		return err
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.RelayURL = relayURL
	// Pairing with a relay is a statement about how this machine is reached.
	s.Mode = ModeRelay
	return saveSettings(dataDir, s)
}

// SaveDirect persists the direct-mode listener and public base URL. An empty
// publicURL is allowed and means "no tunnel is up right now" — the address a
// quick tunnel hands out is different on every start. An empty addr turns
// direct mode off and clears both fields.
func SaveDirect(dataDir, addr, publicURL string) error {
	if addr == "" {
		publicURL = ""
	} else if err := requireLoopback(addr); err != nil {
		return fmt.Errorf("direct address: %w", err)
	}
	if err := validatePublicURL(publicURL); err != nil {
		return err
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.DirectAddr = addr
	s.PublicURL = publicURL
	if addr == "" {
		// Direct mode off: fall back to the relay when there is one.
		s.Mode = ""
		if s.RelayURL != "" {
			s.Mode = ModeRelay
		}
	} else {
		s.Mode = ModeDirect
	}
	return saveSettings(dataDir, s)
}

// SaveTunnel persists which tunnel serve should start for direct mode, and
// that provider's non-secret settings. An empty provider means "none": the
// user publishes the listener some other way.
func SaveTunnel(dataDir, provider, hostname, domain string) error {
	if provider != "" {
		if _, ok := tunnelproc.Lookup(tunnelproc.Kind(provider)); !ok {
			return fmt.Errorf("unknown tunnel provider %q", provider)
		}
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.TunnelProvider = provider
	s.TunnelHostname = hostname
	s.TunnelDomain = domain
	return saveSettings(dataDir, s)
}

// SavePublicURL records the address the tunnel currently publishes, so a
// restart can show it before the tunnel is back up. Only stable providers get
// this: remembering a quick tunnel's address would be remembering a lie.
func SavePublicURL(dataDir, publicURL string) error {
	if err := validatePublicURL(publicURL); err != nil {
		return err
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.PublicURL = publicURL
	return saveSettings(dataDir, s)
}

// BaseURLFromTunnel is the inverse of TunnelURLFromBase: it recovers the
// relay's https:// (or loopback http://) base URL from the stored tunnel
// endpoint, for API calls like pairing-code minting.
func BaseURLFromTunnel(tunnelURL string) (string, error) {
	u, err := url.Parse(tunnelURL)
	if err != nil {
		return "", fmt.Errorf("invalid tunnel URL %q: %w", tunnelURL, err)
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("tunnel URL must be ws(s), got %q", tunnelURL)
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/tunnel"), "/")
	return u.String(), nil
}

// TunnelURLFromBase converts a relay base URL (https://host[/prefix], as
// passed to `pair`) into its tunnel endpoint (wss://host[/prefix]/tunnel).
// Plain http:// maps to ws:// and stays subject to the loopback-only rule.
func TunnelURLFromBase(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid relay URL %q: %w", base, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("relay base URL must be http(s), got %q", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/tunnel"
	tunnel := u.String()
	if err := validateRelayURL(tunnel); err != nil {
		return "", err
	}
	return tunnel, nil
}
