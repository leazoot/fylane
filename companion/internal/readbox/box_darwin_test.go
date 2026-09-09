//go:build darwin

package readbox

// The macOS half of the boundary, asserted against the profile this platform
// actually builds and against a real process running under it. It lives in
// its own file because profileFor exists only here — a test that names it
// from the shared file fails `GOOS=linux go vet` rather than the platform it
// was written for.

import (
	"os"
	osexec "os/exec"
	"strings"
	"testing"
)

func TestADeniedWorkspaceReallyCannotOpenASocket(t *testing.T) {
	// A real process, because everything above this asserts a profile string
	// and a profile string that is never applied is a boundary nobody has.
	//
	// dig is the probe rather than curl: it needs no network to fail the way
	// this test reads for — a denied UDP bind fails locally, so the assertion
	// holds on a machine with no connectivity at all. It is also the case
	// Landlock cannot cover, which is why macOS reports NetEnforced and
	// Linux reports NetPartial.
	box := New(true)
	if box.Network() != NetEnforced {
		t.Skipf("this machine reports %s, so there is nothing to assert", box.Network())
	}
	if _, err := os.Stat("/usr/bin/dig"); err != nil {
		t.Skip("no dig on this machine")
	}
	root := t.TempDir()

	denied := osexec.Command("/usr/bin/dig", "+time=1", "+tries=1", "example.com")
	denied.Env = []string{}
	if err := box.Wrap(denied, WorkspacePolicy(root, false)); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	out, _ := denied.CombinedOutput()
	if !strings.Contains(string(out), "not permitted") {
		t.Fatalf("a denied workspace still opened a socket; dig said: %s", out)
	}

	// And the same command in a workspace that allows the network is not
	// stopped by this boundary. Its own success needs connectivity, which a
	// test may not have, so only the refusal is asserted against.
	allowed := osexec.Command("/usr/bin/dig", "+time=1", "+tries=1", "example.com")
	allowed.Env = []string{}
	if err := box.Wrap(allowed, WorkspacePolicy(root, true)); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	out, _ = allowed.CombinedOutput()
	if strings.Contains(string(out), "not permitted") {
		t.Fatalf("a workspace that allows the network was denied one: %s", out)
	}
}

func TestTheProfileDeniesOnlyWhatWasAskedFor(t *testing.T) {
	root := t.TempDir()
	reads, err := profileFor(WorkspacePolicy(root, true), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reads, "network") {
		t.Error("a reads-only profile denied the network")
	}
	if !strings.Contains(reads, "file-read-data") {
		t.Error("a reads-only profile does not bound reads")
	}

	// Reads off, network denied: the profile must not claim a read boundary
	// the user switched off in settings.
	net, err := profileFor(WorkspacePolicy(root, false), false, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(net, "file-read-data") {
		t.Error("a network-only profile bounded reads as well")
	}
	if !strings.Contains(net, "(deny network*)") {
		t.Errorf("a network-only profile denies nothing: %s", net)
	}
	// A network-only profile needs no readable paths, and demanding some
	// would make the boundary fail exactly where reads are turned off.
	if _, err := profileFor(Policy{}, false, true); err != nil {
		t.Errorf("a network-only profile with no paths: %v", err)
	}
	if _, err := profileFor(Policy{}, true, false); err == nil {
		t.Error("a read boundary over nothing was accepted")
	}
}
