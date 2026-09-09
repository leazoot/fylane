package app

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/directsrv"
	"github.com/leazoot/fylane/companion/internal/tunnelproc"
)

// The connect screen's back end: which tunnel publishes this machine, what it
// published, and switching to another one without restarting the daemon.

// startTunnel builds the manager for the configured provider and starts it.
// Whatever address it announces becomes the issuer, so a quick tunnel that
// renames the machine on every restart needs no user action.
func (a *App) startTunnel(ctx context.Context, direct *directsrv.Server) (*tunnelproc.Manager, error) {
	p, ok := tunnelproc.Lookup(tunnelproc.Kind(a.cfg.TunnelProvider))
	if !ok {
		return nil, fmt.Errorf("unknown tunnel provider %q", a.cfg.TunnelProvider)
	}
	// Only ask the keychain when the provider actually needs a credential;
	// three of the four do not, and a pointless keychain read is a pointless
	// way to fail.
	var token string
	if p.NeedsToken {
		var err error
		if token, err = tunnelproc.LoadToken(p.Kind); err != nil {
			return nil, err
		}
	}
	opt := tunnelproc.Options{
		LocalAddr: a.cfg.DirectAddr,
		Hostname:  a.cfg.TunnelHostname,
		Domain:    a.cfg.TunnelDomain,
		Token:     token,
	}
	m := tunnelproc.New(p, opt, a.log, func(u string) {
		direct.SetPublicURL(u)
		// Only a provider with a durable address is worth remembering: a
		// stored quick-tunnel hostname would be a dead link on next start.
		if p.Stable {
			if err := SavePublicURL(a.cfg.DataDir, u); err != nil {
				a.log.Warn("recording the public URL", "error", err)
			}
		}
		// Announced after the address is in place, never before: a listener
		// told where to connect while the server still answers the old
		// address would be told a lie.
		if a.Ready != nil {
			a.Ready(direct.ConnectorURL(), direct.PairingCode)
		}
	})
	if err := m.Start(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// connectControl implements ctlapi.ConnectControl over the running direct
// server and tunnel manager. direct is nil in relay mode: the screen still
// asks how this machine is reached, and the answer is the relay's address.
type connectControl struct {
	app       *App
	direct    *directsrv.Server
	connector func() string
	ctx       context.Context
	setups    *tunnelproc.Setups
	downloads *tunnelproc.Downloads

	mu     sync.Mutex
	tunnel *tunnelproc.Manager
}

// newConnectControl wires the connect screen's backing object. It exists so
// the runners are never half-set: a nil Setups or Downloads panics on the
// first snapshot, and the screen reads that snapshot constantly.
func newConnectControl(a *App, direct *directsrv.Server, tun *tunnelproc.Manager,
	ctx context.Context, connector func() string) *connectControl {
	return &connectControl{
		app:       a,
		direct:    direct,
		tunnel:    tun,
		ctx:       ctx,
		connector: connector,
		setups:    tunnelproc.NewSetups(a.log),
		downloads: tunnelproc.NewDownloads(a.log),
	}
}

// StartSetup runs the provider's own browser sign-in, so the credential is
// issued to the provider rather than typed into Fylane.
func (c *connectControl) StartSetup(ctx context.Context, provider string) error {
	p, ok := tunnelproc.Lookup(tunnelproc.Kind(provider))
	if !ok {
		return fmt.Errorf("unknown tunnel provider %q", provider)
	}
	return c.setups.Start(ctx, p)
}

// CancelSetup abandons a sign-in in progress.
func (c *connectControl) CancelSetup() { c.setups.Cancel() }

// StartDownload fetches the pinned build of a provider's program. The
// consent happened before this call: it is a button on a panel that showed the
// program, the source and the digest first.
func (c *connectControl) StartDownload(ctx context.Context, provider string) error {
	p, ok := tunnelproc.Lookup(tunnelproc.Kind(provider))
	if !ok {
		return fmt.Errorf("unknown tunnel provider %q", provider)
	}
	return c.downloads.Start(ctx, p)
}

// CancelDownload abandons a download in progress.
func (c *connectControl) CancelDownload() { c.downloads.Cancel() }

// SignOut forgets a provider's credential on this machine. The tunnel running
// on it is stopped first: leaving it up would publish this machine on an
// authorization the user just took away.
func (c *connectControl) SignOut(provider string) error {
	p, ok := tunnelproc.Lookup(tunnelproc.Kind(provider))
	if !ok {
		return fmt.Errorf("unknown tunnel provider %q", provider)
	}
	if m := c.current(); m != nil && m.Provider().Kind == p.Kind {
		if err := c.StopTunnel(); err != nil {
			return err
		}
	}
	if err := p.SignOut(); err != nil {
		return err
	}
	c.setups.Forget(p.Kind)
	return nil
}

func (c *connectControl) current() *tunnelproc.Manager {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tunnel
}

func (c *connectControl) ConnectSnapshot() ctlapi.ConnectDoc {
	// The list of ways in is the same in both modes — that is the point of
	// the screen. What differs is which one is live and what can be started
	// without a restart.
	doc := ctlapi.ConnectDoc{
		Mode:       ModeRelay,
		RelayURL:   StoredRelayURL(c.app.cfg.DataDir),
		Restarting: c.app.Restarting(),
		Providers:  []ctlapi.ConnectProvider{},
		Platform:   runtime.GOOS + "/" + runtime.GOARCH,
	}
	for _, p := range tunnelproc.All() {
		_, installed := p.Available()
		// Only when the program is missing. An offer next to an installed
		// program is an invitation to re-download something already working.
		var offer *ctlapi.ConnectOffer
		if !installed {
			if o, pinned := tunnelproc.OfferFor(p); pinned {
				offer = &ctlapi.ConnectOffer{
					Binary:  o.Binary,
					Version: o.Version,
					Source:  o.Source,
					SHA256:  o.SHA256,
				}
			}
		}
		// Two ways to be authorized: the credential is readable on disk, or
		// this run signed in and the provider keeps its state somewhere we
		// cannot see.
		authorized := p.Checkable() && p.Authorized()
		if c.setups.Authorized(p.Kind) {
			authorized = true
		}
		doc.Providers = append(doc.Providers, ctlapi.ConnectProvider{
			Kind:          string(p.Kind),
			Binary:        p.Binary,
			Install:       p.Install,
			Installed:     installed,
			NeedsToken:    p.NeedsToken,
			NeedsHostname: p.NeedsHostname,
			Stable:        p.Stable,
			Setup:         string(p.Setup),
			Download:      p.Download,
			Offer:         offer,
			Credential:    p.Credential,
			Authorized:    authorized,
			Checkable:     p.Checkable() || c.setups.Authorized(p.Kind),
			CanSignOut:    p.CanSignOut(),
			// The sign-in already picked a domain; asking for it again is
			// asking the user to repeat themselves.
			SuggestedHostname: suggestHostname(p, authorized),
			OpensBrowser:      p.OpensBrowser,
		})
	}
	if st := c.setups.Snapshot(); st.Phase != tunnelproc.SetupIdle {
		doc.Setup = &ctlapi.ConnectSetup{
			Provider: st.Provider,
			Phase:    string(st.Phase),
			URL:      st.URL,
			Detail:   st.Detail,
		}
	}
	if dl := c.downloads.Snapshot(); dl.Phase != tunnelproc.DownloadIdle {
		// A finished download is dropped once the program it installed is
		// found, so a row that now says "installed" does not keep a stale
		// outcome underneath it.
		if dl.Phase == tunnelproc.DownloadReady && installedByKind(doc.Providers, dl.Provider) {
			c.downloads.Forget(tunnelproc.Kind(dl.Provider))
		} else {
			doc.Download = &ctlapi.ConnectDownload{
				Provider: dl.Provider,
				Phase:    string(dl.Phase),
				Detail:   dl.Detail,
			}
		}
	}
	if c.direct == nil {
		// No tunnel-state claim: whether the relay tunnel is up is the lane's
		// story, told on Sources. This screen answers where platforms connect.
		if c.connector != nil {
			doc.ConnectorURL = c.connector()
		}
		return doc
	}

	doc.Mode = ModeDirect
	doc.State = string(tunnelproc.Stopped)
	doc.PublicURL = c.direct.PublicURL()
	doc.ConnectorURL = c.direct.ConnectorURL()
	if m := c.current(); m != nil {
		state, detail, _ := m.Status()
		doc.Provider = string(m.Provider().Kind)
		doc.State, doc.Detail = string(state), detail
	} else if doc.PublicURL != "" {
		// Somebody else is publishing this machine — a tunnel the user runs
		// themselves. Saying "stopped" next to a working address would be a
		// lie about whose job it is.
		doc.State = string(tunnelproc.Running)
		doc.Detail = "published by a tunnel Fylane does not manage"
	}
	return doc
}

// installedByKind reports whether the snapshot already sees this provider's
// program on the machine.
func installedByKind(providers []ctlapi.ConnectProvider, kind string) bool {
	for _, p := range providers {
		if p.Kind == kind {
			return p.Installed
		}
	}
	return false
}

// suggestHostname offers an address under the domain the sign-in chose. Only
// for a provider that needs one and is actually signed in: a suggestion built
// on no account would be a guess presented as an answer.
func suggestHostname(p tunnelproc.Provider, authorized bool) string {
	if !p.NeedsHostname || !authorized || p.Kind != tunnelproc.CloudflareNamed {
		return ""
	}
	return tunnelproc.SuggestedHostname()
}

// ApplyTunnel picks how this machine is reached. Switching between the relay
// and this machine's own surface stores the choice and restarts; changing the
// tunnel within direct mode is a live swap.
func (c *connectControl) ApplyTunnel(ctx context.Context, req ctlapi.TunnelRequest) error {
	if req.Mode == ModeRelay {
		if err := SwitchToRelay(c.app.cfg.DataDir); err != nil {
			return err
		}
		c.app.requestRestart()
		return nil
	}
	p, ok := tunnelproc.Lookup(tunnelproc.Kind(req.Provider))
	if !ok {
		return fmt.Errorf("unknown tunnel provider %q", req.Provider)
	}
	// The credential goes to the keychain before anything is committed: a
	// stored choice whose token never arrived would fail on the next start
	// with nothing on screen to explain it.
	if req.Token != "" {
		if err := tunnelproc.SaveToken(p.Kind, req.Token); err != nil {
			return err
		}
	}
	token := req.Token
	if token == "" && p.Tokened {
		token, _ = tunnelproc.LoadToken(p.Kind)
	}
	opt := tunnelproc.Options{
		LocalAddr: c.app.cfg.DirectAddr, Hostname: req.Hostname, Domain: req.Domain, Token: token,
	}
	if opt.LocalAddr == "" {
		opt.LocalAddr = DefaultDirectAddr
	}
	if err := p.Validate(opt); err != nil {
		return err
	}
	if _, installed := p.Available(); !installed {
		return fmt.Errorf("%s is not installed: %s", p.Binary, p.Install)
	}
	// The account-side work (create the tunnel, point DNS at it) happens
	// before anything is committed. Doing it after the restart would fail
	// where nothing is left on screen to explain it.
	if err := p.Provision(ctx, opt); err != nil {
		return err
	}
	if c.direct == nil {
		// Coming from the relay: what is missing has to be caught now. After
		// the restart there is no relay to fall back to, and a tunnel that
		// cannot start would leave the machine unreachable with nothing on
		// screen to explain it. (A live swap inside direct mode reports the
		// same errors from the start attempt, with the old tunnel still up.)
		if err := SwitchToDirect(c.app.cfg.DataDir, c.app.cfg.DirectAddr, req.Provider, req.Hostname, req.Domain); err != nil {
			return err
		}
		c.app.requestRestart()
		return nil
	}
	if err := SaveTunnel(c.app.cfg.DataDir, req.Provider, req.Hostname, req.Domain); err != nil {
		return err
	}
	c.stop()

	c.app.cfg.TunnelProvider = req.Provider
	c.app.cfg.TunnelHostname = req.Hostname
	c.app.cfg.TunnelDomain = req.Domain
	m, err := c.app.startTunnel(c.ctx, c.direct)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.tunnel = m
	c.mu.Unlock()
	return nil
}

// StopTunnel stops the managed tunnel and forgets the choice, so a restart
// does not bring it back behind the user's back.
func (c *connectControl) StopTunnel() error {
	if c.direct == nil {
		return ctlapi.ErrNotDirect
	}
	c.stop()
	c.app.cfg.TunnelProvider = ""
	return SaveTunnel(c.app.cfg.DataDir, "", "", "")
}

func (c *connectControl) stop() {
	c.mu.Lock()
	m := c.tunnel
	c.tunnel = nil
	c.mu.Unlock()
	if m != nil {
		m.Stop()
	}
	c.direct.SetPublicURL("")
}
