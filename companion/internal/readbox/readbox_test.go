package readbox

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// On Linux the boundary is applied by re-executing this very binary as a shim
// (box_linux.go), and under `go test` this binary is the test binary. Without
// this interception the child would run the whole package again, which would
// wrap another command, which would run the package again — a test that
// never finishes rather than one that fails. This mirrors what the Core's
// own main does before dispatching anything.
func TestMain(m *testing.M) {
	if IsShim(os.Args) {
		if err := RunShim(os.Args); err != nil {
			os.Stderr.WriteString("read boundary shim: " + err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestOffIsNotAbsentAndNeitherIsAFailure(t *testing.T) {
	// Three states, three meanings. Off is the user's choice and says so;
	// Absent is the platform's answer; a failure is neither and never
	// produces a running unbounded child.
	//
	// The platform is fixed here rather than probed, because the three states
	// are a derivation and this asserts the derivation: on a machine that
	// cannot enforce a boundary there is no user choice that produces Off,
	// and that is the case immediately below.
	off := &Box{platform: Enforced, why: "subprocess reads are bounded"}
	off.SetEnabled(false)
	if off.State() != Off || off.Enforcing() {
		t.Fatalf("state = %s, want off", off.State())
	}
	if !strings.Contains(off.Why(), "turned off") {
		t.Fatalf("why = %q, want it to say the user turned it off", off.Why())
	}

	// Turning it back on takes effect here, not at the next restart. A switch
	// that needs a restart to mean anything is the quiet failure this package
	// spends its whole design avoiding.
	off.SetEnabled(true)
	if off.State() != Enforced {
		t.Fatalf("state after turning it back on = %s, want enforced", off.State())
	}
	off.SetEnabled(false)

	// A machine that cannot enforce one reports absence whatever the user
	// chose. Reporting Off there would put the blame on a person for
	// something the platform decided — and the settings page draws those two
	// differently on purpose.
	cannot := &Box{platform: Absent, why: "this platform has no boundary"}
	for _, chose := range []bool{true, false} {
		cannot.SetEnabled(chose)
		if cannot.State() != Absent {
			t.Fatalf("state on a machine with no boundary = %s (setting %v), want absent", cannot.State(), chose)
		}
		if strings.Contains(cannot.Why(), "turned off") {
			t.Fatalf("why = %q, want the platform's reason, not the user's", cannot.Why())
		}
	}
	// Off still wraps nothing rather than failing: turning the boundary off
	// is not a bypass flag, it is the absence of this one layer.
	cmd := osexec.Command("/bin/echo", "hi")
	if err := off.Wrap(cmd, Policy{ReadWrite: []string{t.TempDir()}}); err != nil {
		t.Fatalf("Wrap while off: %v", err)
	}
	if cmd.Path != "/bin/echo" {
		t.Fatalf("an off boundary rewrote the command to %q", cmd.Path)
	}

	// A nil Box is what a caller that was never given one holds. It must
	// behave like an absence, not panic and not silently claim enforcement.
	var none *Box
	if none.Enforcing() || none.State() != Absent {
		t.Fatalf("nil box state = %s", none.State())
	}
	if err := none.Wrap(cmd, Policy{}); err != nil {
		t.Fatalf("nil box Wrap: %v", err)
	}
}

func TestTheWorkspaceIsReadableAndTheCredentialStoresAreNot(t *testing.T) {
	// The allowed set is the line this whole package draws. It is asserted
	// against names rather than behaviour so it holds on every platform,
	// including the ones that cannot enforce anything.
	root := t.TempDir()
	p := WorkspacePolicy(root, true)
	if len(p.ReadWrite) != 1 || p.ReadWrite[0] != root {
		t.Fatalf("read-write = %v, want just the workspace", p.ReadWrite)
	}
	joined := strings.Join(p.allPaths(), "\n")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	// Configuration a toolchain cannot start without: allowed.
	if !strings.Contains(joined, filepath.Join(home, ".gitconfig")) {
		t.Fatalf("git configuration is not readable, so git cannot run:\n%s", joined)
	}
	// Credential stores: never, whatever a build says it needs. A build that
	// wants a private registry token fails loudly, which is the direction
	// this boundary is supposed to fail in.
	for _, secret := range []string{".ssh", ".aws", ".npmrc", ".netrc", ".docker", ".kube", ".gnupg"} {
		if strings.Contains(joined, filepath.Join(home, secret)) {
			t.Fatalf("%s is inside the read boundary:\n%s", secret, joined)
		}
	}
	// And the home directory itself is not granted wholesale, which would
	// make every entry above meaningless.
	for _, path := range p.allPaths() {
		if path == home {
			t.Fatal("the whole home directory is readable")
		}
	}
}

func TestAPolicyThatAllowsNothingIsRejected(t *testing.T) {
	// An empty policy would be a boundary around everything, which no caller
	// means and which would look like a broken machine rather than a refusal.
	if _, err := decodePolicy(`{"read_write":[],"read_only":[]}`); err == nil {
		t.Fatal("an empty policy was accepted")
	}
	if _, err := decodePolicy(`not json`); err == nil {
		t.Fatal("unreadable JSON was accepted")
	}
}

func TestNormalArgumentsAreNotTheShim(t *testing.T) {
	for _, args := range [][]string{
		{"fylane-companion"},
		{"fylane-companion", "serve"},
		{"fylane-companion", "serve", "-relay", "wss://example"},
	} {
		if IsShim(args) {
			t.Fatalf("%v was taken for the shim", args)
		}
	}
}

// The rest of this file is the real thing: a real process, a real boundary,
// and a file it must not be able to read.
func TestAWrappedCommandCannotReadOutsideTheWorkspace(t *testing.T) {
	box := New(true)
	if !box.Enforcing() {
		t.Skipf("no read boundary on this machine: %s", box.Why())
	}
	if runtime.GOOS == "windows" {
		t.Skip("no boundary and no /bin/cat")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("workspace content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The home directory is the F21/F22 residual in one place: other repos,
	// documents, keys, browser profiles. Nothing is written there — listing
	// it is the whole test.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}

	run := func(prog string, args ...string) (string, error) {
		cmd := osexec.Command(prog, args...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
		if err := box.Wrap(cmd, WorkspacePolicy(root, true)); err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	got, err := run("/bin/cat", inside)
	if err != nil || !strings.Contains(got, "workspace content") {
		t.Fatalf("reading inside the workspace failed: %v %q", err, got)
	}

	got, _ = run("/bin/ls", home)
	if !strings.Contains(strings.ToLower(got), "not permitted") &&
		!strings.Contains(strings.ToLower(got), "denied") {
		// And the refusal has to look like a refusal. "No such file" would
		// send whoever reads it looking for something that is right there.
		t.Fatalf("the home directory was readable, or the denial did not say it was one: %q", got)
	}
}

func TestTheBoundaryDoesNotStopTheToolchainItNeedsToRun(t *testing.T) {
	// The allowed set is a name list, and a name list falls behind. This is
	// the test that says which direction it falls in: a missing entry has to
	// show up here as a broken build, not out there as a quiet leak.
	box := New(true)
	if !box.Enforcing() {
		t.Skipf("no read boundary on this machine: %s", box.Why())
	}
	goBin, err := osexec.LookPath("go")
	if err != nil {
		t.Skip("no Go toolchain on this machine")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	for name, body := range map[string]string{
		"go.mod":  "module boundary.test\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := osexec.Command(goBin, "build", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if err := box.Wrap(cmd, WorkspacePolicy(root, true)); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build inside the boundary failed: %v\n%s", err, out)
	}
}

func TestReachSaysWhatAWorkspaceActuallyGets(t *testing.T) {
	// Four words because four things can be true, and the two that are easy
	// to merge are the ones that matter: "asked for no network and got it"
	// and "asked for no network and this machine cannot deliver" would look
	// identical behind a boolean.
	allowed := Policy{Network: true}
	denied := Policy{}
	for _, c := range []struct {
		net  NetState
		want [2]Reach // allowing, denying
	}{
		{NetEnforced, [2]Reach{ReachAllowed, ReachDenied}},
		{NetPartial, [2]Reach{ReachAllowed, ReachPartial}},
		{NetAbsent, [2]Reach{ReachAllowed, ReachUnbounded}},
	} {
		b := &Box{platform: Enforced, net: c.net}
		if got := b.Reach(allowed); got != c.want[0] {
			t.Errorf("net %s, workspace allows: reach = %s, want %s", c.net, got, c.want[0])
		}
		if got := b.Reach(denied); got != c.want[1] {
			t.Errorf("net %s, workspace denies: reach = %s, want %s", c.net, got, c.want[1])
		}
	}

	// A Companion with no boundary at all reports the honest word, not the
	// workspace's wish.
	var none *Box
	if got := none.Reach(denied); got != ReachUnbounded {
		t.Errorf("nil box reach = %s, want unbounded", got)
	}
}

func TestTheTwoBoundariesAreSeparatePromises(t *testing.T) {
	// Turning the read boundary off in settings must not quietly drop a
	// workspace's "no network", and a workspace that allows the network must
	// still have its reads bounded. One switch answering for both is the
	// merge this test exists to prevent.
	b := &Box{platform: Enforced, net: NetEnforced, why: "bounded"}

	b.SetEnabled(false)
	if reads, network := b.bounds(Policy{}); reads || !network {
		t.Errorf("read boundary off, network denied: reads=%v network=%v", reads, network)
	}
	b.SetEnabled(true)
	if reads, network := b.bounds(Policy{Network: true}); !reads || network {
		t.Errorf("read boundary on, network allowed: reads=%v network=%v", reads, network)
	}
	// Neither wanted is the only case that wraps nothing at all.
	b.SetEnabled(false)
	if reads, network := b.bounds(Policy{Network: true}); reads || network {
		t.Errorf("nothing asked for: reads=%v network=%v", reads, network)
	}

	// And a machine that cannot deny the network never claims to.
	cannot := &Box{platform: Enforced, net: NetAbsent}
	if _, network := cannot.bounds(Policy{}); network {
		t.Error("a machine with no outbound boundary reported one in force")
	}
}

func TestAPolicySurvivesTheTripThroughTheEnvironment(t *testing.T) {
	// The Linux shim reads the policy back out of an environment variable.
	// A field that does not survive that trip is a boundary the child never
	// gets, and the network flag is the one whose loss is silent: the reads
	// would still be bounded and nothing would look wrong.
	for _, network := range []bool{true, false} {
		encoded, err := Policy{ReadWrite: []string{"/tmp/work"}, Network: network}.encode()
		if err != nil {
			t.Fatal(err)
		}
		back, err := decodePolicy(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if back.Network != network {
			t.Errorf("network %v did not survive encoding", network)
		}
	}
}
