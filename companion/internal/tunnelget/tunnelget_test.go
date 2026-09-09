package tunnelget

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// allowLoopback is the only way a test reaches a local server. Production has
// no equivalent: Getter.checkIP is unexported, so nothing outside this package
// can install it.
func allowLoopback(net.IP) error { return nil }

func serve(t *testing.T, h http.Handler) (*httptest.Server, *Getter) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, &Getter{checkIP: allowLoopback, endpoint: srv.URL}
}

func rawPin(body []byte) Pin {
	return Pin{
		Binary: "cloudflared", Version: "test", Asset: "cloudflared-test",
		SHA256: digest(body),
		URL:    "https://example.invalid/releases/download/test/cloudflared-test",
	}
}

func tgz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// The pin table

// Every target this project ships for must have a pin, and every digest must
// be a digest. A truncated or mistyped checksum would otherwise surface as a
// checksum failure at download time, which reads like tampering and would
// send whoever hits it looking in the wrong place.
func TestEveryPinnedTargetParses(t *testing.T) {
	pins, err := parsePins(pinsFile)
	if err != nil {
		t.Fatalf("pins.txt does not parse: %v", err)
	}
	want := []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64"}
	for _, target := range want {
		parts := strings.SplitN(target, "/", 2)
		p, err := PinFor("cloudflared", parts[0], parts[1])
		if err != nil {
			t.Errorf("PinFor(%s): %v", target, err)
			continue
		}
		if !hex64.MatchString(p.SHA256) {
			t.Errorf("%s: %q is not a sha-256", target, p.SHA256)
		}
		if p.Version == "" || !strings.HasPrefix(p.URL, "https://") {
			t.Errorf("%s: version %q url %q", target, p.Version, p.URL)
		}
		if !strings.Contains(p.URL, p.Version) || !strings.HasSuffix(p.URL, p.Asset) {
			t.Errorf("%s: url %q does not name version %q and asset %q", target, p.URL, p.Version, p.Asset)
		}
	}
	if len(pins) != len(want) {
		t.Errorf("pins.txt has %d entries, want %d", len(pins), len(want))
	}
}

// An unpinned target must be an error and never a fallback to some other
// build: an unpinned platform means nobody knows what would be running.
func TestAnUnpinnedTargetIsRefused(t *testing.T) {
	if _, err := PinFor("cloudflared", "plan9", "mips"); err == nil {
		t.Fatal("PinFor returned a pin for an unpinned target")
	}
	if Pinned("cloudflared", "plan9", "mips") {
		t.Error("Pinned said yes for an unpinned target")
	}
	if !Pinned("cloudflared", "linux", "amd64") {
		t.Error("Pinned said no for a target that is in the table")
	}
}

// The build script and this package must read the same digests. The way that
// stops being true is somebody copying a checksum back into the script, so
// what is asserted is the absence of a second copy.
func TestTheBuildScriptKeepsNoSecondCopyOfTheDigests(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "fetch-cloudflared.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(script, []byte("companion/internal/tunnelget/pins.txt")) {
		t.Error("fetch-cloudflared.sh does not read pins.txt")
	}
	if loose := regexp.MustCompile(`[0-9a-f]{64}`).FindAll(script, -1); len(loose) > 0 {
		t.Errorf("fetch-cloudflared.sh carries its own copy of %d digest(s); pins.txt is the only place they belong", len(loose))
	}
}

// ---------------------------------------------------------------------------
// Downloading

func TestFetchInstallsAVerifiedBinary(t *testing.T) {
	body := []byte("#!/bin/sh\necho tunnel\n")
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	_ = srv
	dir := filepath.Join(t.TempDir(), "tools")

	got, err := g.Fetch(context.Background(), rawPin(body), dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if want := filepath.Join(dir, ExeName("cloudflared")); got != want {
		t.Errorf("installed at %q, want %q", got, want)
	}
	on, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(on, body) {
		t.Error("the installed file is not what the server sent")
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed mode %v is not executable", info.Mode().Perm())
	}
}

// The digest is the whole integrity control, so a mismatch has to end the
// operation — and it has to leave nothing behind. A rejected download still
// sitting in the directory would be found by the next lookup and run.
func TestFetchDiscardsADownloadThatFailsItsChecksum(t *testing.T) {
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not the program you pinned"))
	}))
	_ = srv
	dir := filepath.Join(t.TempDir(), "tools")

	_, err := g.Fetch(context.Background(), rawPin([]byte("the real program")), dir)
	if err == nil {
		t.Fatal("Fetch accepted a body that does not match the pin")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error %q does not say the checksum failed", err)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("a rejected download left %d file(s) behind: %v", len(left), left)
	}
}

func TestFetchExtractsTheBinaryFromAnArchive(t *testing.T) {
	body := tgz(t, map[string]string{"cloudflared": "the program"})
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	_ = srv
	dir := filepath.Join(t.TempDir(), "tools")

	pin := rawPin(body)
	pin.Archive = true
	got, err := g.Fetch(context.Background(), pin, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	on, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(on) != "the program" {
		t.Errorf("installed %q, want the archived program", on)
	}
}

// Entry names inside the archive come from outside, so they must choose
// nothing: not the destination, not whether an entry is written at all. Only
// the one expected name is taken, and it is written to a path this package
// picked.
func TestArchiveEntryNamesChooseNothing(t *testing.T) {
	// The one entry whose base name matches also carries a path that climbs
	// out of the destination, into a directory that does not exist. Using
	// that name for anything — even for a file that is moved away afterwards
	// — leaves "escaped" behind, which is how the test sees it.
	body := tgz(t, map[string]string{
		"../../../escaped/cloudflared": "the program",
		"cloudflared.bak":              "not the program",
		"subdir/../passwd":             "should never be written",
	})
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	_ = srv
	// Deep enough that the climbing entry lands inside root, where the walk
	// below can see it. An escape the test cannot observe is not a test.
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b", "tools")

	pin := rawPin(body)
	pin.Archive = true
	got, err := g.Fetch(context.Background(), pin, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	on, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(on) != "the program" {
		t.Errorf("installed %q; the entry matched by base name is the one that counts", on)
	}
	// Nothing anywhere but the one installed file, and no directory the
	// archive asked for. Directories count: a file written outside and then
	// moved into place still leaves its parent behind.
	var stray []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		switch {
		case err != nil || info == nil:
		case info.IsDir() && strings.Contains(path, "escaped"):
			stray = append(stray, path)
		case !info.IsDir() && path != got:
			stray = append(stray, path)
		}
		return nil
	})
	if len(stray) != 0 {
		t.Errorf("the archive chose where to write: %v", stray)
	}
}

func TestFetchRefusesAnArchiveWithoutTheBinary(t *testing.T) {
	body := tgz(t, map[string]string{"readme.txt": "nothing useful here"})
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	_ = srv
	pin := rawPin(body)
	pin.Archive = true

	if _, err := g.Fetch(context.Background(), pin, filepath.Join(t.TempDir(), "tools")); err == nil {
		t.Fatal("Fetch accepted an archive with no binary in it")
	}
}

// ---------------------------------------------------------------------------
// The transport guard

// The production IP policy is on unless a test replaces it, and this is the
// test that proves it: a loopback server is unreachable with the zero value.
func TestTheDefaultGetterWillNotDialALocalAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("should never be delivered"))
	}))
	defer srv.Close()

	g := &Getter{endpoint: srv.URL} // no checkIP: production policy
	_, err := g.Fetch(context.Background(), rawPin([]byte("x")), filepath.Join(t.TempDir(), "tools"))
	if err == nil {
		t.Fatal("the default getter dialled a loopback address")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error %q does not name the reason", err)
	}
}

func TestFetchRefusesANonHTTPSPin(t *testing.T) {
	pin := rawPin([]byte("x"))
	pin.URL = "http://example.invalid/cloudflared"

	g := &Getter{checkIP: allowLoopback}
	_, err := g.Fetch(context.Background(), pin, filepath.Join(t.TempDir(), "tools"))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("Fetch over http: %v; want an https-only refusal", err)
	}
}

// A release download does redirect — to the publisher's asset host — so the
// rule cannot be "same host". What it is instead: every hop stays on https.
// A redirect that drops to plain http is refused even though the pinned
// digest would still have caught altered bytes.
func TestARedirectMayNotDropToPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("delivered over http"))
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/asset", http.StatusFound)
	}))
	defer secure.Close()

	pool := x509.NewCertPool()
	pool.AddCert(secure.Certificate())
	g := &Getter{checkIP: allowLoopback, endpoint: secure.URL, tlsConfig: &tls.Config{RootCAs: pool}}

	_, err := g.Fetch(context.Background(), rawPin([]byte("x")), filepath.Join(t.TempDir(), "tools"))
	if err == nil {
		t.Fatal("Fetch followed a redirect off https")
	}
	if !strings.Contains(err.Error(), "http") {
		t.Errorf("error %q does not name the refused scheme", err)
	}
}

// A redirect chain that never ends is a way to hold the download open; the
// hop limit is what stops it.
//
// The chain has to stay on https, or the scheme rule stops it at the first
// hop and this says nothing about the limit. And asserting only that it
// errors would pass on the http package's own default of ten, which is not
// the limit this package chose — so what is counted is how many times the
// server was actually asked.
func TestARedirectChainIsBounded(t *testing.T) {
	var mu sync.Mutex
	hops := 0
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hops++
		mu.Unlock()
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	g := &Getter{checkIP: allowLoopback, endpoint: srv.URL, tlsConfig: &tls.Config{RootCAs: pool}}

	_, err := g.Fetch(context.Background(), rawPin([]byte("x")), filepath.Join(t.TempDir(), "tools"))
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("Fetch on an endless redirect: %v; want a hop-limit refusal", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hops > maxRedirects {
		t.Errorf("the server was asked %d times; the limit is %d", hops, maxRedirects)
	}
}

// The cap has to be enforced before the body is read, or announcing a huge
// size would be enough to make Fylane read it.
func TestAnOversizedAssetIsRefusedBeforeItIsRead(t *testing.T) {
	served := false
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.Header().Set("Content-Length", fmt.Sprint(MaxBytes+1))
		w.WriteHeader(http.StatusOK)
		w.Write(bytes.Repeat([]byte("x"), 16))
	}))
	_ = srv

	_, err := g.Fetch(context.Background(), rawPin([]byte("x")), filepath.Join(t.TempDir(), "tools"))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("Fetch of an oversized asset: %v; want a size refusal", err)
	}
	if !served {
		t.Error("the test server was never reached, so the cap was not what refused it")
	}
}

func TestAServerErrorIsNotAnInstall(t *testing.T) {
	srv, g := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	_ = srv
	dir := filepath.Join(t.TempDir(), "tools")

	if _, err := g.Fetch(context.Background(), rawPin([]byte("x")), dir); err == nil {
		t.Fatal("Fetch treated a 404 as a download")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("a failed download left %v behind", left)
	}
}

// ---------------------------------------------------------------------------
// Placement

func TestDownloadsGoInTheDataDirectoryAndNowhereElse(t *testing.T) {
	if got, want := Dir("/home/someone/.config/fylane"), filepath.Join("/home/someone/.config/fylane", "tools"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	// No data directory means no download location, not the working directory.
	if got := Dir(""); got != "" {
		t.Errorf("Dir(\"\") = %q, want empty", got)
	}
}

// clearQuarantine runs on every install, so a file that never carried the
// mark must not be an error. On macOS that is the ENOATTR path.
func TestClearingAnAbsentQuarantineMarkIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := clearQuarantine(path); err != nil {
		t.Errorf("clearQuarantine on an unmarked file: %v", err)
	}
}
