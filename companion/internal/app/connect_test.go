package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/directsrv"
)

// fakeTunnelBinary puts a stand-in cloudflared on PATH that announces an
// address and stays up.
func fakeTunnelBinary(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tunnel binary is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"INF |  https://switched-tunnel.trycloudflare.com  |\" >&2\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(filepath.Join(dir, "cloudflared"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Switching tunnels from the connect screen starts the process, publishes the
// address it announces, and remembers the choice for the next start.
func TestConnectControlSwitchesTunnels(t *testing.T) {
	fakeTunnelBinary(t)
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	direct, err := directsrv.Open(ctx, dataDir, "test-host", http.NotFoundHandler(), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()

	a := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:8787", DirectAddr: "127.0.0.1:8891"}, testLogger())
	c := newConnectControl(a, direct, nil, ctx, nil)

	if doc := c.ConnectSnapshot(); doc.Mode != "direct" || doc.State != "stopped" || len(doc.Providers) != 4 {
		t.Fatalf("initial snapshot = %+v", doc)
	}
	if err := c.ApplyTunnel(ctx, ctlapi.TunnelRequest{Provider: "cloudflare-quick"}); err != nil {
		t.Fatalf("ApplyTunnel: %v", err)
	}
	defer c.stop()

	deadline := time.Now().Add(10 * time.Second)
	for direct.PublicURL() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := direct.PublicURL(); got != "https://switched-tunnel.trycloudflare.com" {
		t.Fatalf("public URL = %q", got)
	}
	doc := c.ConnectSnapshot()
	if doc.Provider != "cloudflare-quick" || doc.State != "running" ||
		doc.ConnectorURL != "https://switched-tunnel.trycloudflare.com/mcp" {
		t.Fatalf("snapshot = %+v", doc)
	}

	stored := readConfig(t, dataDir)
	if stored["tunnel_provider"] != "cloudflare-quick" {
		t.Errorf("tunnel_provider not persisted: %v", stored)
	}
	// A quick tunnel's address is different on every start, so remembering it
	// would only produce a dead link.
	if _, ok := stored["public_url"]; ok {
		t.Errorf("a throwaway address was remembered: %v", stored)
	}

	if err := c.StopTunnel(); err != nil {
		t.Fatalf("StopTunnel: %v", err)
	}
	if direct.PublicURL() != "" || direct.ConnectorURL() != "" {
		t.Fatal("stopping the tunnel left an address behind")
	}
	if stored := readConfig(t, dataDir); stored["tunnel_provider"] != nil {
		t.Errorf("stopping did not forget the provider: %v", stored)
	}
}

func TestConnectControlRejectsIncompleteProviders(t *testing.T) {
	fakeTunnelBinary(t)
	dataDir := t.TempDir()
	ctx := context.Background()
	direct, err := directsrv.Open(ctx, dataDir, "test-host", http.NotFoundHandler(), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	a := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:8787", DirectAddr: "127.0.0.1:8891"}, testLogger())
	c := newConnectControl(a, direct, nil, ctx, nil)

	if err := c.ApplyTunnel(ctx, ctlapi.TunnelRequest{Provider: "carrier-pigeon"}); err == nil {
		t.Error("unknown provider accepted")
	}
	// A named tunnel needs an account and somewhere to answer. Which of the
	// two is missing depends on a file in the user's home directory, so the
	// test says where that file is — otherwise it passes or fails according
	// to whether the developer has run `cloudflared tunnel login`.
	cert := filepath.Join(t.TempDir(), "cert.pem")
	t.Setenv("TUNNEL_ORIGIN_CERT", cert)

	err = c.ApplyTunnel(ctx, ctlapi.TunnelRequest{Provider: "cloudflare-named"})
	if err == nil {
		c.stop()
		t.Fatal("named tunnel accepted with no account behind it")
	}
	if !strings.Contains(err.Error(), "account") {
		t.Errorf("error = %v; want it to say the account is missing", err)
	}

	// Signed in, what is left missing is the hostname.
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = c.ApplyTunnel(ctx, ctlapi.TunnelRequest{Provider: "cloudflare-named"})
	if err == nil {
		c.stop()
		t.Fatal("named tunnel accepted with nowhere to answer")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("error = %v; want it to say the hostname is missing", err)
	}
}

// Switching between the relay and this machine's own surface is stored and
// then restarts: the listeners, the authorization server and the pairing
// origin all change at once, and no half-applied state is worth the risk.
func TestConnectControlSwitchesModes(t *testing.T) {
	fakeTunnelBinary(t)
	dataDir := t.TempDir()
	ctx := context.Background()
	if err := SaveRelayURL(dataDir, "wss://relay.example/tunnel"); err != nil {
		t.Fatal(err)
	}

	// Relay mode: no direct server, and the screen still lists every way in.
	a := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:8787", RelayURL: "wss://relay.example/tunnel"}, testLogger())
	c := newConnectControl(a, nil, nil, ctx, func() string { return "https://relay.example/mcp" })
	doc := c.ConnectSnapshot()
	if doc.Mode != ModeRelay || len(doc.Providers) != 4 || doc.RelayURL != "wss://relay.example/tunnel" {
		t.Fatalf("relay snapshot = %+v", doc)
	}
	if doc.ConnectorURL != "https://relay.example/mcp" || doc.Restarting {
		t.Fatalf("relay snapshot = %+v", doc)
	}

	if err := c.ApplyTunnel(ctx, ctlapi.TunnelRequest{Provider: "cloudflare-quick"}); err != nil {
		t.Fatalf("switch to direct: %v", err)
	}
	if !a.Restarting() && !waitFor(func() bool { return a.Restarting() }) {
		t.Fatal("switching modes did not ask for a restart")
	}
	stored := readConfig(t, dataDir)
	if stored["mode"] != ModeDirect || stored["direct_addr"] != DefaultDirectAddr ||
		stored["tunnel_provider"] != "cloudflare-quick" {
		t.Fatalf("stored = %v", stored)
	}
	// The relay stays on file: switching back must not mean pairing again.
	if stored["relay_url"] != "wss://relay.example/tunnel" {
		t.Fatalf("the relay was forgotten: %v", stored)
	}
	// A resolved config now comes up in direct mode, with the relay ignored.
	cfg, err := ParseArgs([]string{"-data-dir", dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DirectAddr != DefaultDirectAddr || cfg.RelayURL != "" || cfg.TunnelProvider != "cloudflare-quick" {
		t.Fatalf("resolved config = %+v", cfg)
	}

	// And back again.
	b := New(cfg, testLogger())
	back := newConnectControl(b, nil, nil, ctx, nil)
	if err := back.ApplyTunnel(ctx, ctlapi.TunnelRequest{Mode: ModeRelay}); err != nil {
		t.Fatalf("switch to relay: %v", err)
	}
	if stored := readConfig(t, dataDir); stored["mode"] != ModeRelay {
		t.Fatalf("stored = %v", stored)
	}
	cfg, err = ParseArgs([]string{"-data-dir", dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelayURL != "wss://relay.example/tunnel" || cfg.DirectAddr != "" {
		t.Fatalf("resolved config = %+v", cfg)
	}
	// The tunnel choice is direct mode's; carrying it into relay mode used to
	// fail the "-tunnel needs direct mode" check on startup.
	if cfg.TunnelProvider != "" {
		t.Fatalf("relay mode resolved a tunnel provider: %+v", cfg)
	}
}

// Going direct is one-way until the switch completes, so what the tunnel
// needs has to be complete before the relay is given up.
func TestSwitchToDirectRefusesAnIncompleteTunnel(t *testing.T) {
	dataDir := t.TempDir()
	if err := SaveRelayURL(dataDir, "wss://relay.example/tunnel"); err != nil {
		t.Fatal(err)
	}
	a := New(&Config{DataDir: dataDir, RelayURL: "wss://relay.example/tunnel"}, testLogger())
	c := newConnectControl(a, nil, nil, context.Background(), nil)

	err := c.ApplyTunnel(context.Background(), ctlapi.TunnelRequest{Provider: "cloudflare-named"})
	if err == nil {
		t.Fatal("switched to a named tunnel with no hostname or token")
	}
	if a.Restarting() {
		t.Fatal("a refused switch still restarted the daemon")
	}
	if stored := readConfig(t, dataDir); stored["mode"] != ModeRelay {
		t.Fatalf("a refused switch changed the stored mode: %v", stored)
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func readConfig(t *testing.T, dataDir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The offer is what the connect screen asks with, so the snapshot has to tell
// the three states apart: program present (nothing to offer), program missing
// but pinned (offer it), program missing and unpinned (say so instead).
func TestSnapshotOffersOnlyWhatIsMissingAndPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tunnel binary is a shell script")
	}
	// An empty PATH so a cloudflared the developer happens to have does not
	// decide which branch this test takes.
	t.Setenv("PATH", t.TempDir())
	a := New(&Config{DataDir: t.TempDir(), Addr: "127.0.0.1:8787"}, testLogger())
	c := newConnectControl(a, nil, nil, context.Background(), nil)

	byKind := map[string]ctlapi.ConnectProvider{}
	for _, p := range c.ConnectSnapshot().Providers {
		byKind[p.Kind] = p
	}

	quick := byKind["cloudflare-quick"]
	if quick.Installed {
		t.Fatal("cloudflared was found despite an empty PATH")
	}
	if quick.Offer == nil {
		t.Fatal("cloudflared is missing and pinned, but no offer was made")
	}
	if quick.Offer.Binary != "cloudflared" || quick.Offer.SHA256 == "" || quick.Offer.Source == "" {
		t.Fatalf("offer = %+v", quick.Offer)
	}
	// The vendor's own page stays alongside it. fetching became possible;
	// it did not make installing it yourself the lesser path.
	if quick.Download == "" {
		t.Fatal("the offer replaced the vendor's download page")
	}

	// No pin for this program: an offer that cannot be honoured is worse than
	// no offer, and the screen has something else to say.
	if ngrok := byKind["ngrok"]; ngrok.Offer != nil {
		t.Fatalf("ngrok is unpinned but was offered: %+v", ngrok.Offer)
	}

	// Present on the machine: nothing to offer. Re-downloading a working
	// program is a download nobody needed.
	fakeTunnelBinary(t)
	for _, p := range c.ConnectSnapshot().Providers {
		if p.Kind == "cloudflare-quick" {
			if !p.Installed {
				t.Fatal("the fake cloudflared was not found")
			}
			if p.Offer != nil {
				t.Fatalf("an installed program was still offered: %+v", p.Offer)
			}
		}
	}
}
