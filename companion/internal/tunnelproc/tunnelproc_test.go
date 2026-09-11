package tunnelproc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/tunnelget"
)

func TestProviderArgsAndParsing(t *testing.T) {
	opt := Options{LocalAddr: "127.0.0.1:8891", Hostname: "fylane.example.com",
		Domain: "fixed.ngrok.app", Token: "cf-token"}

	cases := []struct {
		kind      Kind
		wantArgs  []string
		line      string
		wantURL   string
		fixedURL  string
		otherLine string
	}{
		{
			kind:     CloudflareQuick,
			wantArgs: []string{"tunnel", "--no-autoupdate", "--url", "http://127.0.0.1:8891"},
			line:     "2026-08-15T10:00:00Z INF |  https://heater-grip-plymouth-wendy.trycloudflare.com   |",
			wantURL:  "https://heater-grip-plymouth-wendy.trycloudflare.com",
			// Cloudflare's banner also prints its own docs links; only the
			// tunnel hostname may be taken for the public address.
			otherLine: "2026-08-15T10:00:00Z INF Thank you for trying Cloudflare Tunnel. See https://developers.cloudflare.com/",
		},
		{
			kind: CloudflareNamed,
			wantArgs: []string{"tunnel", "--no-autoupdate", "--url", "http://127.0.0.1:8891",
				"run", "--token", "cf-token"},
			fixedURL: "https://fylane.example.com",
		},
		{
			kind:      TailscaleFunnel,
			wantArgs:  []string{"funnel", "8891"},
			line:      "https://laptop.tail1234.ts.net/",
			wantURL:   "https://laptop.tail1234.ts.net",
			otherLine: "|-- proxy http://127.0.0.1:8891",
		},
		{
			kind: Ngrok,
			wantArgs: []string{"http", "127.0.0.1:8891", "--log", "stdout", "--log-format", "logfmt",
				"--domain", "fixed.ngrok.app"},
			line:      `t=2026-08-15T10:00:00-0700 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://127.0.0.1:8891 url=https://fixed.ngrok.app`,
			wantURL:   "https://fixed.ngrok.app",
			otherLine: `t=2026-08-15T10:00:00-0700 lvl=info msg="client session established"`,
		},
	}

	for _, c := range cases {
		p, ok := Lookup(c.kind)
		if !ok {
			t.Fatalf("Lookup(%q) missing", c.kind)
		}
		args, err := p.Args(opt)
		if err != nil {
			t.Fatalf("%s Args: %v", c.kind, err)
		}
		if strings.Join(args, " ") != strings.Join(c.wantArgs, " ") {
			t.Errorf("%s args = %v; want %v", c.kind, args, c.wantArgs)
		}
		if got := p.PublicURL(opt); got != c.fixedURL {
			t.Errorf("%s PublicURL = %q; want %q", c.kind, got, c.fixedURL)
		}
		if c.line != "" {
			if got := p.ParseURL(c.line); got != c.wantURL {
				t.Errorf("%s ParseURL = %q; want %q", c.kind, got, c.wantURL)
			}
		}
		if c.otherLine != "" {
			if got := p.ParseURL(c.otherLine); got != "" {
				t.Errorf("%s took %q as the public address from a noise line", c.kind, got)
			}
		}
	}
}

// The shipped copy must win over PATH: it is the build this release was
// checksummed and signed against, and a stray older cloudflared on the user's
// PATH must not be what actually runs.
func TestLookupPrefersTheShippedBinary(t *testing.T) {
	shipped := t.TempDir()
	onPath := t.TempDir()
	write := func(dir, name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	name := tunnelget.ExeName("cloudflared")
	want := write(shipped, name, 0o755)
	write(onPath, name, 0o755)
	t.Setenv("PATH", onPath)

	if got, ok := lookup("cloudflared", shipped); !ok || got != want {
		t.Errorf("lookup = %q, %v; want the shipped %q", got, ok, want)
	}

	// Nothing shipped: fall back to whatever the user installed.
	if got, ok := lookup("cloudflared", t.TempDir()); !ok || got == want {
		t.Errorf("lookup without a shipped copy = %q, %v; want the PATH one", got, ok)
	}

	// A non-executable file with the right name is not a tunnel. Windows has
	// no execute bit, so there the name is the only signal and this case
	// cannot be distinguished.
	if runtime.GOOS != "windows" {
		half := t.TempDir()
		write(half, name, 0o644)
		if got, _ := lookup("cloudflared", half); got == filepath.Join(half, name) {
			t.Error("lookup returned a file it cannot execute")
		}
	}

	// Nothing anywhere is reported as missing, not as an empty path that
	// would later be spawned.
	t.Setenv("PATH", t.TempDir())
	if got, ok := lookup("cloudflared", t.TempDir()); ok {
		t.Errorf("lookup = %q, true; want not found", got)
	}
}

// The vendor installer's folder counts even when PATH does not know it: on
// Windows a Fylane that was running during the install still has the old
// PATH, and on macOS the app bundle never adds the CLI to PATH at all.
func TestLookupFindsTheInstallerLocationOffPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	var want string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("ProgramFiles", root)
		t.Setenv("ProgramFiles(x86)", "")
		want = filepath.Join(root, "Tailscale", "tailscale.exe")
	case "darwin":
		applicationsDir = root
		t.Cleanup(func() { applicationsDir = "/Applications" })
		want = filepath.Join(root, "Tailscale.app", "Contents", "MacOS", "Tailscale")
	default:
		t.Skip("no vendor install location to check on this platform")
	}
	if got, ok := lookup("tailscale", t.TempDir()); ok {
		t.Fatalf("lookup = %q before anything is installed", got)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := lookup("tailscale", t.TempDir()); !ok || got != want {
		t.Errorf("lookup = %q, %v; want the installed %q", got, ok, want)
	}
}

// Three places, in this order: what shipped with this release, what the user
// consented to download, and whatever is on PATH. The order is the
// point — shipped was checksummed and signed together with us, downloaded was
// only checksummed, and PATH is whatever happens to be on the machine.
func TestLookupPrefersShippedOverDownloadedOverPath(t *testing.T) {
	name := tunnelget.ExeName("cloudflared")
	dirs := map[string]string{}
	for _, which := range []string{"shipped", "downloaded", "path"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		dirs[which] = dir
	}
	t.Setenv("PATH", dirs["path"])

	for _, tc := range []struct {
		name    string
		shipped string
		down    string
		want    string
	}{
		{"all three", dirs["shipped"], dirs["downloaded"], dirs["shipped"]},
		{"no shipped copy", "", dirs["downloaded"], dirs["downloaded"]},
		{"neither", "", "", dirs["path"]},
		{"shipped dir without the binary", t.TempDir(), dirs["downloaded"], dirs["downloaded"]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lookup("cloudflared", tc.shipped, tc.down)
			if !ok || got != filepath.Join(tc.want, name) {
				t.Errorf("lookup = %q, %v; want the copy in %q", got, ok, tc.want)
			}
		})
	}
}

// The order above is asserted on lookup, but what production calls is
// Available, and the order it passes its directories in is a separate fact.
// This is the one that would go wrong silently: a downloaded binary shadowing
// the one this release was signed with, and everything still working.
func TestAvailableAsksTheShippedDirectoryFirst(t *testing.T) {
	t.Cleanup(func() { SetDownloadDir("") })
	name := tunnelget.ExeName("cloudflared")

	// The shipped location is wherever this executable is, so for the test
	// that is the test binary's own directory. It is a temporary build
	// directory the toolchain removes, but the file is cleaned up regardless.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	shipped := filepath.Join(filepath.Dir(exe), name)
	if err := os.WriteFile(shipped, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skipf("cannot write beside the test binary: %v", err)
	}
	t.Cleanup(func() { os.Remove(shipped) })

	downloads := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloads, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	SetDownloadDir(downloads)
	t.Setenv("PATH", t.TempDir())

	p, ok := Lookup(CloudflareQuick)
	if !ok {
		t.Fatal("the quick tunnel provider is missing from this build")
	}
	got, ok := p.Available()
	if !ok || got != shipped {
		t.Errorf("Available = %q, %v; want the shipped copy %q", got, ok, shipped)
	}

	// With nothing shipped, the downloaded copy is what runs.
	os.Remove(shipped)
	if got, ok := p.Available(); !ok || got != filepath.Join(downloads, name) {
		t.Errorf("Available = %q, %v; want the downloaded copy", got, ok)
	}
}

// The download directory is consulted only because something told this
// package where it is. Knowing the location is not permission to fill it: the
// resolver never fetches, so an unset directory is simply one fewer place to
// look.
func TestTheDownloadDirectoryIsOnlyEverRead(t *testing.T) {
	t.Cleanup(func() { SetDownloadDir("") })

	if DownloadDir() != "" {
		t.Fatalf("DownloadDir starts at %q, want empty", DownloadDir())
	}
	dir := filepath.Join(t.TempDir(), "tools")
	SetDownloadDir(dir)
	if DownloadDir() != dir {
		t.Errorf("DownloadDir = %q, want %q", DownloadDir(), dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("setting the download directory created it: %v", err)
	}

	// A provider is still simply missing when the directory is empty.
	t.Setenv("PATH", t.TempDir())
	if got, ok := lookup("cloudflared", "", DownloadDir()); ok {
		t.Errorf("lookup = %q, true; want not found", got)
	}
}

func TestProviderValidation(t *testing.T) {
	// Whether a named tunnel is usable now depends on a file in the user's
	// home directory, so the test says where that file is. Without this it
	// passes or fails according to whether the developer happens to have run
	// `cloudflared tunnel login` — which is not a property of the code.
	t.Setenv("TUNNEL_ORIGIN_CERT", filepath.Join(t.TempDir(), "cert.pem"))

	named, _ := Lookup(CloudflareNamed)
	if err := named.Validate(Options{LocalAddr: "127.0.0.1:8891", Hostname: "h.example"}); err == nil {
		t.Error("named tunnel accepted with neither a sign-in nor a token")
	}
	if err := named.Validate(Options{LocalAddr: "127.0.0.1:8891", Token: "t"}); err == nil {
		t.Error("named tunnel accepted without a hostname")
	}
	// A token is one way in; the browser sign-in is the other. Either alone
	// is enough, which is the whole point of not making people find a token.
	if err := named.Validate(Options{LocalAddr: "127.0.0.1:8891", Hostname: "h.example", Token: "t"}); err != nil {
		t.Errorf("named tunnel rejected with a token: %v", err)
	}
	cert := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNEL_ORIGIN_CERT", cert)
	if err := named.Validate(Options{LocalAddr: "127.0.0.1:8891", Hostname: "h.example"}); err != nil {
		t.Errorf("named tunnel rejected after signing in: %v", err)
	}
	quick, _ := Lookup(CloudflareQuick)
	if err := quick.Validate(Options{LocalAddr: "not-an-address"}); err == nil {
		t.Error("malformed local address accepted")
	}
	if _, ok := Lookup("wireguard"); ok {
		t.Error("Lookup invented a provider")
	}
	if len(All()) != 4 {
		t.Errorf("All() = %d providers; want 4", len(All()))
	}
}

// fakeBinary puts a script named after the provider's binary on PATH.
func fakeBinary(t *testing.T, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tunnel binary is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The manager reads the address out of the process output and reports it.
func TestManagerPublishesTheAddress(t *testing.T) {
	fakeBinary(t, "cloudflared", `
echo "INF |  https://fake-tunnel-abc.trycloudflare.com  |" >&2
while true; do sleep 1; done
`)
	p, _ := Lookup(CloudflareQuick)
	urls := make(chan string, 4)
	m := New(p, Options{LocalAddr: "127.0.0.1:8891"}, testLogger(), func(u string) { urls <- u })
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	select {
	case got := <-urls:
		if got != "https://fake-tunnel-abc.trycloudflare.com" {
			t.Fatalf("published %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no address published")
	}
	if state, _, url := m.Status(); state != Running || url == "" {
		t.Fatalf("Status = %s, %q", state, url)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Error("Start while running spawned a second process")
	}

	m.Stop()
	if state, _, url := m.Status(); state != Stopped || url != "" {
		t.Fatalf("after Stop: state = %s, url = %q", state, url)
	}
	// Stopping clears the address for everyone watching, so the desktop stops
	// showing a connector URL that no longer resolves.
	for {
		select {
		case u := <-urls:
			if u == "" {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not report the address going away")
		}
	}
}

// A tunnel that dies is restarted, and a quick tunnel's new address replaces
// the old one.
func TestManagerRestartsAndFollowsANewAddress(t *testing.T) {
	dir := fakeBinary(t, "cloudflared", `
n=$(cat "$FAKE_RUNS" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$FAKE_RUNS"
echo "INF |  https://run-$n.trycloudflare.com  |" >&2
sleep 0.2
`)
	runs := filepath.Join(dir, "runs")
	t.Setenv("FAKE_RUNS", runs)

	p, _ := Lookup(CloudflareQuick)
	urls := make(chan string, 8)
	m := New(p, Options{LocalAddr: "127.0.0.1:8891"}, testLogger(), func(u string) { urls <- u })
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	seen := map[string]bool{}
	deadline := time.After(20 * time.Second)
	for len(seen) < 2 {
		select {
		case u := <-urls:
			if u != "" {
				seen[u] = true
			}
		case <-deadline:
			t.Fatalf("only saw %v after a restart", seen)
		}
	}
	if !seen["https://run-1.trycloudflare.com"] || !seen["https://run-2.trycloudflare.com"] {
		t.Fatalf("addresses = %v; want both runs", seen)
	}
}

func TestManagerRefusesAMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	p, _ := Lookup(Ngrok)
	m := New(p, Options{LocalAddr: "127.0.0.1:8891"}, testLogger(), nil)
	err := m.Start(context.Background())
	if err == nil {
		m.Stop()
		t.Fatal("Start succeeded without the binary installed")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v; want an install hint", err)
	}
}
