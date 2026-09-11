// Package tunnelproc runs the tunnel a user chose and reports the public URL
// it hands out. Fylane never opens a port on the router: something has to
// publish the Companion's loopback listener, and the four options here are
// the ones people actually have — a throwaway Cloudflare tunnel, a Cloudflare
// tunnel on their own domain, Tailscale Funnel, or ngrok.
//
// Each option is a Provider: what to run, and how to read the address out of
// its output. Adding a fifth is writing one of these, not touching the
// manager.
package tunnelproc

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"

	"github.com/leazoot/fylane/companion/internal/tunnelget"
)

// Kind names a tunnel provider in configuration and on the wire.
type Kind string

const (
	// CloudflareQuick needs no account and no domain; the hostname is
	// different on every start, which is why it is the try-it option rather
	// than the keep-it one.
	CloudflareQuick Kind = "cloudflare-quick"
	// CloudflareNamed keeps one hostname on the user's own domain.
	CloudflareNamed Kind = "cloudflare-named"
	// TailscaleFunnel publishes on the user's tailnet name, no domain needed.
	TailscaleFunnel Kind = "tailscale-funnel"
	// Ngrok is the fourth common answer; a paid plan keeps the domain stable.
	Ngrok Kind = "ngrok"
)

// SetupKind says how a provider is authorized before it can publish. The
// point of the distinction is that the user never opens a terminal: either
// there is nothing to authorize, or the provider's own browser flow does it,
// or the vendor issues a token on a page we can open for them.
type SetupKind string

const (
	// SetupNone needs no account at all.
	SetupNone SetupKind = "none"
	// SetupBrowser runs the provider's own login command, which prints a URL;
	// the user approves in their browser and the provider writes its
	// credential itself. Nothing is typed into Fylane.
	SetupBrowser SetupKind = "browser"
	// SetupToken is for vendors with no CLI authorization flow. The token is
	// pasted once and kept in the OS keychain; Fylane still starts the
	// process, so `config add-authtoken` never has to be run by hand.
	SetupToken SetupKind = "token"
)

// Options are the per-provider settings a user supplies. Secrets are not
// here: the Cloudflare tunnel token comes from the keychain or the
// environment, and ngrok keeps its own authtoken in its own config file.
type Options struct {
	// LocalAddr is the Companion's direct-mode listener, e.g. 127.0.0.1:8891.
	LocalAddr string
	// Hostname is the fixed public hostname of a named Cloudflare tunnel.
	Hostname string
	// Domain is an ngrok reserved domain; empty uses an ephemeral one.
	Domain string
	// Token is the Cloudflare tunnel token, read from the keychain by the
	// caller. It is passed to the child process and never logged or stored
	// in the config file.
	Token string
}

// Provider describes one way to publish the local listener.
type Provider struct {
	Kind Kind
	// Binary is the executable the user must have installed.
	Binary string
	// Install is the hint shown when it is missing.
	Install string
	// NeedsToken and NeedsHostname gate the wizard's fields.
	NeedsToken    bool
	NeedsHostname bool
	// Stable reports whether the public address survives a restart.
	Stable bool
	// Setup is how this provider is authorized.
	Setup SetupKind
	// Download is the vendor's own download page, offered when the binary is
	// missing. The runtime never fetches an executable itself, so the
	// most the product does is take the user there.
	Download string
	// Credential is the page that issues the token, for SetupToken.
	Credential string
	// Tokened reports that this provider has a credential worth reading from
	// the keychain. Asking for the other two would be a keychain read that
	// can only fail.
	Tokened bool
	// OpensBrowser reports that the sign-in command opens the browser itself.
	// Fylane must not open it as well: two tabs for one authorization, one of
	// which flashes past, reads as something going wrong.
	OpensBrowser bool

	args  func(Options) []string
	parse *regexp.Regexp
	// fixedURL returns the address known before the process starts (a named
	// tunnel has one); empty means "wait for the output to say".
	fixedURL func(Options) string
	// setupArgs is the provider's own login command (SetupBrowser).
	setupArgs []string
	// setupURL matches the address that command prints for the browser.
	setupURL *regexp.Regexp
	// authorized answers "is the credential already here" from local state.
	// Nil means the question cannot be answered without running something,
	// and the caller must not treat that as "no".
	authorized func() bool
	// provision does the one-off account-side work (create the tunnel, point
	// DNS at it). It must be safe to run twice.
	provision func(context.Context, Options) error
	// signOut forgets the credential on this machine. It undoes what the
	// sign-in did here and nothing more: what lives on the vendor's account
	// is the user's, not this program's, to delete.
	signOut func() error
	// env adds process environment, for vendors that take their credential
	// that way instead of on the command line.
	env func(Options) []string
}

// Authorized reports whether the provider's credential is already on this
// machine. A provider whose state cannot be read without running something
// answers true: refusing to start on a guess would be worse than trying.
func (p Provider) Authorized() bool {
	if p.authorized == nil {
		return true
	}
	return p.authorized()
}

// Checkable reports whether Authorized is a real answer rather than the
// benefit of the doubt.
func (p Provider) Checkable() bool { return p.authorized != nil }

// SetupArgs is the provider's login command, and whether it has one.
func (p Provider) SetupArgs() ([]string, bool) {
	if len(p.setupArgs) == 0 {
		return nil, false
	}
	return append([]string(nil), p.setupArgs...), true
}

// SetupURL returns the browser address announced on one output line, or "".
func (p Provider) SetupURL(line string) string {
	if p.setupURL == nil {
		return ""
	}
	return p.setupURL.FindString(line)
}

// Env is the extra environment the child process needs.
func (p Provider) Env(opt Options) []string {
	if p.env == nil {
		return nil
	}
	return p.env(opt)
}

// Provision performs the account-side setup this provider needs before it can
// run. It is called on every apply, so it has to be idempotent.
func (p Provider) Provision(ctx context.Context, opt Options) error {
	if p.provision == nil {
		return nil
	}
	return p.provision(ctx, opt)
}

// CanSignOut reports whether Fylane can undo this provider's sign-in. It is
// false where the credential is not Fylane's to remove: Tailscale signs the
// whole machine in through its own daemon, and taking that away from behind a
// button in this window would disconnect everything else using the tailnet.
func (p Provider) CanSignOut() bool { return p.signOut != nil }

// SignOut forgets the credential this machine holds for the provider.
func (p Provider) SignOut() error {
	if p.signOut == nil {
		return fmt.Errorf("%s cannot be signed out from here", p.Kind)
	}
	return p.signOut()
}

// Args returns the command line for these options, after validating them.
func (p Provider) Args(opt Options) ([]string, error) {
	if err := p.Validate(opt); err != nil {
		return nil, err
	}
	return p.args(opt), nil
}

// Validate reports what the user still has to supply.
func (p Provider) Validate(opt Options) error {
	if _, _, err := net.SplitHostPort(opt.LocalAddr); err != nil {
		return fmt.Errorf("local address %q is not host:port", opt.LocalAddr)
	}
	if p.NeedsToken && opt.Token == "" && !p.Authorized() {
		return fmt.Errorf("%s needs a token from %s", p.Kind, p.Credential)
	}
	// A browser-authorized provider must not be started on a guess: the login
	// writes a credential the process reads, and without it the tunnel comes
	// up, fails, and retries forever with nothing on screen saying why.
	if p.Setup == SetupBrowser && p.Checkable() && !p.Authorized() && opt.Token == "" {
		return fmt.Errorf("%s is not connected to an account yet", p.Kind)
	}
	if p.NeedsHostname && opt.Hostname == "" {
		return fmt.Errorf("%s needs the public hostname of the tunnel", p.Kind)
	}
	return nil
}

// PublicURL returns the address this provider publishes, either known in
// advance or absent until the process prints it.
func (p Provider) PublicURL(opt Options) string {
	if p.fixedURL == nil {
		return ""
	}
	return p.fixedURL(opt)
}

// ParseURL returns the public URL announced on one output line, or "".
func (p Provider) ParseURL(line string) string {
	if p.parse == nil {
		return ""
	}
	return p.parse.FindString(line)
}

// Available reports the resolved binary path, or false when none of the three
// places Fylane looks has it.
func (p Provider) Available() (string, bool) {
	return lookup(p.Binary, shippedDir(), DownloadDir())
}

// downloadDir is where a binary the user consented to download was put
// . It is set once at startup from the data directory and is empty
// until then, which only means that rung of the search is skipped.
var (
	downloadMu  sync.RWMutex
	downloadDir string
)

// SetDownloadDir tells this package where to look for downloaded binaries.
// Called once while the Companion starts; it does not fetch anything and does
// not make anything eligible to be fetched.
func SetDownloadDir(dir string) {
	downloadMu.Lock()
	defer downloadMu.Unlock()
	downloadDir = dir
}

// DownloadDir reports the directory set by SetDownloadDir, empty if none.
func DownloadDir() string {
	downloadMu.RLock()
	defer downloadMu.RUnlock()
	return downloadDir
}

// shippedDir is the directory holding the Companion executable. The desktop
// bundle puts cloudflared there, so a fresh install can open a tunnel with
// nothing installed on the machine .
func shippedDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// A Homebrew or /usr/local install is usually a symlink; the shipped
	// binary sits next to the real file, not next to the link.
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe)
}

// lookup resolves a provider binary in the order Fylane trusts them: the copy
// shipped beside this executable, then one the user consented to download,
// then whatever is on PATH, then where the vendor's installer puts it.
//
// Shipped comes first because it is the version this release was built,
// checksummed and signed against, so it is the one whose behaviour is known.
// Downloaded comes second for the same reason one rung weaker — its digest
// was pinned by this build, but it was not signed together with us. PATH is
// last because it is whatever happens to be on the machine.
func lookup(binary string, dirs ...string) (string, bool) {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, tunnelget.ExeName(binary))
		if info, err := os.Stat(candidate); err == nil && runnable(info) {
			return candidate, true
		}
	}
	if path, err := exec.LookPath(binary); err == nil {
		return path, true
	}
	for _, candidate := range installedCopies(binary) {
		if info, err := os.Stat(candidate); err == nil && runnable(info) {
			return candidate, true
		}
	}
	return "", false
}

// applicationsDir is where macOS app bundles live. A variable so a test can
// point it at a directory it owns.
var applicationsDir = "/Applications"

// installedCopies is where each vendor's installer puts the binary when it is
// not on PATH — or not on the PATH this process has. Windows installers add
// their folder to the system PATH, but a process already running keeps the
// PATH it started with, so a Fylane that was open during the install would
// report the tool missing until relaunched. macOS app bundles never touch
// PATH at all: the Tailscale CLI sits inside the bundle.
func installedCopies(binary string) []string {
	switch runtime.GOOS {
	case "windows":
		folder := map[string]string{"tailscale": "Tailscale", "cloudflared": "cloudflared"}[binary]
		if folder == "" {
			return nil
		}
		var paths []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if root := os.Getenv(env); root != "" {
				paths = append(paths, filepath.Join(root, folder, tunnelget.ExeName(binary)))
			}
		}
		return paths
	case "darwin":
		if binary == "tailscale" {
			return []string{filepath.Join(applicationsDir, "Tailscale.app", "Contents", "MacOS", "Tailscale")}
		}
	}
	return nil
}

// runnable reports whether a directory entry is something we could execute.
// Windows carries no execute bit, so there the name is all there is to go on.
func runnable(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

func localPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}

func localHTTP(addr string) string { return "http://" + addr }

// providers is the registry. Order is the order the wizard offers them:
// nothing-to-set-up first, most durable last.
var providers = []Provider{
	{
		Kind:     CloudflareQuick,
		Binary:   "cloudflared",
		Install:  "brew install cloudflared (or download from Cloudflare)",
		Download: "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
		Setup:    SetupNone,
		Stable:   false,
		args: func(o Options) []string {
			return []string{"tunnel", "--no-autoupdate", "--url", localHTTP(o.LocalAddr)}
		},
		parse: regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`),
	},
	{
		Kind:          CloudflareNamed,
		Binary:        "cloudflared",
		Install:       "brew install cloudflared (or download from Cloudflare)",
		Download:      "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
		Setup:         SetupBrowser,
		NeedsHostname: true,
		Tokened:       true,
		Stable:        true,
		args: func(o Options) []string {
			// The global --url overrides the dashboard ingress, so one tunnel
			// points wherever the Companion is listening today.
			args := []string{"tunnel", "--no-autoupdate", "--url", localHTTP(o.LocalAddr), "run"}
			// A dashboard token still works and is not taken away from anyone
			// who set one up; the browser flow just means nobody has to.
			if o.Token != "" {
				return append(args, "--token", o.Token)
			}
			return append(args, CloudflareTunnelName)
		},
		fixedURL:     func(o Options) string { return "https://" + bareHost(o.Hostname) },
		OpensBrowser: true,
		setupArgs:    []string{"tunnel", "login"},
		setupURL:     regexp.MustCompile(`https://dash\.cloudflare\.com/argotunnel\?\S+`),
		authorized:   cloudflareLoggedIn,
		provision:    provisionCloudflareNamed,
		signOut:      cloudflareSignOut,
	},
	{
		Kind:     TailscaleFunnel,
		Binary:   "tailscale",
		Install:  "install Tailscale and run `tailscale up`",
		Download: "https://tailscale.com/download",
		Setup:    SetupBrowser,
		Stable:   true,
		args: func(o Options) []string {
			// Funnel proxies to a local port and stays in the foreground.
			return []string{"funnel", localPort(o.LocalAddr)}
		},
		parse:        regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.ts\.net`),
		OpensBrowser: true,
		// `up` is idempotent: already signed in, it returns at once and prints
		// no URL, which is exactly how this reports "nothing to do".
		setupArgs: []string{"up"},
		setupURL:  regexp.MustCompile(`https://login\.tailscale\.com/\S+`),
	},
	{
		Kind:       Ngrok,
		Binary:     "ngrok",
		Install:    "brew install ngrok, then `ngrok config add-authtoken <token>`",
		Download:   "https://ngrok.com/download",
		Credential: "https://dashboard.ngrok.com/get-started/your-authtoken",
		Setup:      SetupToken,
		NeedsToken: true,
		Tokened:    true,
		Stable:     false,
		args: func(o Options) []string {
			args := []string{"http", o.LocalAddr, "--log", "stdout", "--log-format", "logfmt"}
			if o.Domain != "" {
				args = append(args, "--domain", o.Domain)
			}
			return args
		},
		// The authtoken rides the environment rather than the command line:
		// `ngrok config add-authtoken` is a step the user should not have to
		// take, and an argv is visible in the process table.
		env: func(o Options) []string {
			if o.Token == "" {
				return nil
			}
			return []string{"NGROK_AUTHTOKEN=" + o.Token}
		},
		parse:      regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.ngrok(?:-free)?\.(?:app|dev|io)`),
		authorized: ngrokConfigured,
		signOut:    func() error { return DeleteToken(Ngrok) },
	},
}

// All returns every provider, in wizard order.
func All() []Provider { return append([]Provider(nil), providers...) }

// Lookup returns the provider for kind.
func Lookup(kind Kind) (Provider, bool) {
	for _, p := range providers {
		if p.Kind == kind {
			return p, true
		}
	}
	return Provider{}, false
}
