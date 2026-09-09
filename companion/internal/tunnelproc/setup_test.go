package tunnelproc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor polls until cond holds or the deadline passes. The sign-in runs in
// its own goroutine, so the state it publishes arrives after Start returns.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestSetupPublishesTheBrowserURLBeforeItFinishes(t *testing.T) {
	// The whole point of the flow: the URL has to reach the screen while the
	// command is still running, because the command does not return until
	// the user has finished in their browser. A version that only reported
	// on exit would show a blank panel for as long as the sign-in takes.
	// Writing the certificate is what finishing means; a command that only
	// exits is the failure case, covered separately.
	t.Setenv("TUNNEL_ORIGIN_CERT", filepath.Join(t.TempDir(), "cert.pem"))
	fakeBinary(t, "cloudflared", `#!/bin/sh
echo "Please open the following URL and log in:"
echo "https://dash.cloudflare.com/argotunnel?callback=abc123"
sleep 1
printf x > "$TUNNEL_ORIGIN_CERT"
exit 0
`)
	p, _ := Lookup(CloudflareNamed)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the browser URL", func() bool {
		return s.Snapshot().Phase == SetupWaiting
	})
	st := s.Snapshot()
	if st.URL != "https://dash.cloudflare.com/argotunnel?callback=abc123" {
		t.Errorf("URL = %q", st.URL)
	}
	if st.Provider != string(CloudflareNamed) {
		t.Errorf("provider = %q", st.Provider)
	}
	waitFor(t, "the sign-in to finish", func() bool { return s.Snapshot().Phase == SetupReady })
	// Read from disk, not remembered: see TestACheckableSignInIsNotRememberedButRead.
	if !p.Authorized() {
		t.Error("a completed sign-in did not count as authorized")
	}
}

func TestExitZeroIsNotProofOfASignIn(t *testing.T) {
	// Regression, found on the machine: `cloudflared tunnel login` returned 0
	// after the user closed the page, no cert.pem was written, and the panel
	// said "account connected" — with the domain field empty, because there
	// was no zone to read. Where the credential can be read, reading it is
	// the answer; the exit code is not.
	t.Setenv("TUNNEL_ORIGIN_CERT", filepath.Join(t.TempDir(), "cert.pem"))
	fakeBinary(t, "cloudflared", "#!/bin/sh\nexit 0\n")

	p, _ := Lookup(CloudflareNamed)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the sign-in to be judged", func() bool {
		ph := s.Snapshot().Phase
		return ph == SetupReady || ph == SetupFailed
	})
	if ph := s.Snapshot().Phase; ph != SetupFailed {
		t.Errorf("phase = %q; want failed — no certificate was written", ph)
	}
	if s.Authorized(CloudflareNamed) {
		t.Error("a sign-in that left no credential was remembered as done")
	}
}

func TestACheckableSignInIsNotRememberedButRead(t *testing.T) {
	// The credential on disk is the truth. Remembering "this run signed in"
	// for a provider we can read would outlive a later sign-out and keep the
	// panel claiming an account that is gone.
	cert := filepath.Join(t.TempDir(), "cert.pem")
	t.Setenv("TUNNEL_ORIGIN_CERT", cert)
	fakeBinary(t, "cloudflared", "#!/bin/sh\nprintf x > \"$TUNNEL_ORIGIN_CERT\"\nexit 0\n")

	p, _ := Lookup(CloudflareNamed)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the sign-in", func() bool { return s.Snapshot().Phase == SetupReady })
	if !p.Authorized() {
		t.Error("the certificate is there but the provider does not see it")
	}
	if s.Authorized(CloudflareNamed) {
		t.Error("a readable sign-in was also remembered; the file is the only truth")
	}
}

func TestSetupReportsWhyItFailed(t *testing.T) {
	fakeBinary(t, "cloudflared", `#!/bin/sh
echo "failed to get zone: no account" >&2
exit 1
`)
	p, _ := Lookup(CloudflareNamed)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the failure", func() bool { return s.Snapshot().Phase == SetupFailed })
	if d := s.Snapshot().Detail; d != "failed to get zone: no account" {
		t.Errorf("detail = %q; want the program's own last line", d)
	}
	if s.Authorized(CloudflareNamed) {
		t.Error("a failed sign-in counted as authorized")
	}
}

func TestOnlyOneSignInAtATime(t *testing.T) {
	fakeBinary(t, "cloudflared", "#!/bin/sh\nsleep 2\n")
	p, _ := Lookup(CloudflareNamed)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Start(context.Background(), p); err == nil {
		t.Error("a second sign-in started on top of the first")
	}
	s.Cancel()
	if ph := s.Snapshot().Phase; ph != SetupIdle {
		t.Errorf("phase after cancel = %q; want idle", ph)
	}
}

func TestCancelDoesNotLeaveAFailureOnScreen(t *testing.T) {
	// Cancelling is the user's own decision. Reporting it back to them as a
	// failure would be the app blaming them for pressing its own button.
	fakeBinary(t, "tailscale", "#!/bin/sh\nsleep 2\n")
	p, _ := Lookup(TailscaleFunnel)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Cancel()
	time.Sleep(200 * time.Millisecond)
	if ph := s.Snapshot().Phase; ph != SetupIdle {
		t.Errorf("phase = %q; want idle after a cancel", ph)
	}
}

func TestProvidersWithoutASignInSaySo(t *testing.T) {
	quick, _ := Lookup(CloudflareQuick)
	if _, ok := quick.SetupArgs(); ok {
		t.Error("the quick tunnel claims a sign-in it does not have")
	}
	if quick.Setup != SetupNone {
		t.Errorf("quick tunnel setup = %q; want none", quick.Setup)
	}
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), quick); err == nil {
		t.Error("started a sign-in for a provider that has none")
	}
}

func TestNgrokTakesItsTokenFromTheEnvironmentNotTheCommandLine(t *testing.T) {
	// An argv is readable by anything on the machine; `ngrok config
	// add-authtoken` is a terminal step the user should never have to take.
	p, _ := Lookup(Ngrok)
	args, err := p.Args(Options{LocalAddr: "127.0.0.1:8891", Token: "secret-token"})
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	for _, a := range args {
		if a == "secret-token" {
			t.Fatal("the authtoken is on the command line")
		}
	}
	env := p.Env(Options{Token: "secret-token"})
	if len(env) != 1 || env[0] != "NGROK_AUTHTOKEN=secret-token" {
		t.Errorf("Env = %v", env)
	}
	if got := p.Env(Options{}); len(got) != 0 {
		t.Errorf("Env with no token = %v; want nothing set", got)
	}
}

func TestNamedTunnelRunsByNameOnceSignedIn(t *testing.T) {
	// Signed in, there is no token to put on the command line — the tunnel is
	// named, and cloudflared reads the credentials the login wrote.
	p, _ := Lookup(CloudflareNamed)
	cert := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNEL_ORIGIN_CERT", cert)

	args, err := p.Args(Options{LocalAddr: "127.0.0.1:8891", Hostname: "fylane.example.com"})
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	if args[len(args)-1] != CloudflareTunnelName {
		t.Errorf("args = %v; want it to end with the tunnel name", args)
	}
	// A user who already had a dashboard token keeps working exactly as before.
	args, err = p.Args(Options{LocalAddr: "127.0.0.1:8891", Hostname: "h.example", Token: "tok"})
	if err != nil {
		t.Fatalf("Args with token: %v", err)
	}
	if args[len(args)-2] != "--token" || args[len(args)-1] != "tok" {
		t.Errorf("args = %v; want the token path preserved", args)
	}
}

func TestSignOutRemovesTheCredentialAndTheMemoryOfIt(t *testing.T) {
	// Signing out also clears the stored token, and the real keychain is the
	// developer's own — a test must not reach into it.
	keyring.MockInit()

	// Two halves, and missing either one makes the button a lie: the file has
	// to go, and this process has to stop vouching for the sign-in it ran.
	cert := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNEL_ORIGIN_CERT", cert)

	p, _ := Lookup(CloudflareNamed)
	if !p.Authorized() {
		t.Fatal("setup: the certificate is there but the provider says otherwise")
	}
	if !p.CanSignOut() {
		t.Fatal("the named tunnel cannot be signed out, but its credential is ours to remove")
	}
	if err := p.SignOut(); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if _, err := os.Stat(cert); !os.IsNotExist(err) {
		t.Error("the certificate survived the sign-out")
	}
	if p.Authorized() {
		t.Error("still reported as signed in after signing out")
	}
	// Signing out twice is what happens when the user presses it again after
	// a failure; it must not turn into an error about a missing file.
	if err := p.SignOut(); err != nil {
		t.Errorf("second SignOut: %v", err)
	}
}

func TestSigningOutClearsWhatThisRunRemembers(t *testing.T) {
	fakeBinary(t, "tailscale", "#!/bin/sh\nexit 0\n")
	p, _ := Lookup(TailscaleFunnel)
	s := NewSetups(quietLog())
	if err := s.Start(context.Background(), p); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the sign-in", func() bool { return s.Authorized(TailscaleFunnel) })
	s.Forget(TailscaleFunnel)
	if s.Authorized(TailscaleFunnel) {
		t.Error("the run still vouches for a sign-in that was undone")
	}
	if ph := s.Snapshot().Phase; ph != SetupIdle {
		t.Errorf("phase = %q; want the finished sign-in cleared off the panel", ph)
	}
}

func TestWhatIsNotOursToRemoveIsNotOfferedAsAButton(t *testing.T) {
	// Tailscale signs the whole machine in through its own daemon. Offering
	// "disconnect" here would take the tailnet away from everything else on
	// the machine, from a button in a window about one folder.
	ts, _ := Lookup(TailscaleFunnel)
	if ts.CanSignOut() {
		t.Error("tailscale offers a sign-out that is not Fylane's to perform")
	}
	if err := ts.SignOut(); err == nil {
		t.Error("SignOut succeeded for a provider that cannot be signed out")
	}
	quick, _ := Lookup(CloudflareQuick)
	if quick.CanSignOut() {
		t.Error("the quick tunnel offers a sign-out; it has no account at all")
	}
}

func TestAuthorizedIsNotAGuess(t *testing.T) {
	t.Setenv("TUNNEL_ORIGIN_CERT", filepath.Join(t.TempDir(), "cert.pem"))
	named, _ := Lookup(CloudflareNamed)
	if !named.Checkable() {
		t.Error("the named tunnel's sign-in state is readable and should say so")
	}
	if named.Authorized() {
		t.Error("reported signed in with no certificate on disk")
	}
	// Tailscale keeps its state in a daemon this process cannot read. It must
	// answer "cannot tell", not "no" — a false no would refuse to start a
	// tunnel that would have worked.
	ts, _ := Lookup(TailscaleFunnel)
	if ts.Checkable() {
		t.Error("tailscale claims a readable sign-in state")
	}
	if !ts.Authorized() {
		t.Error("an unreadable sign-in state was reported as not signed in")
	}
}
