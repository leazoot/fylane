// Package tunnelget downloads a pinned tunnel binary onto a machine whose
// Fylane package did not carry one.
//
// This package exists because of a revision, not because of a preference.
// Fylane shipped cloudflared inside the desktop package and drew the line at
// "the runtime never downloads an executable"; that line moved after
// the user reaffirmed the choice with the counter-argument in hand. So the
// rules this package follows are narrow on purpose:
//
//   - It downloads only what pins.txt names. There is no argument anywhere
//     that carries a URL, a version, or a digest — a caller can ask for
//     "cloudflared on this platform" and nothing else. This is not a general
//     downloader and must never grow into one.
//   - Nothing is fetched without the user having said yes to this download.
//     Consent is the caller's job because only the caller has a way to ask;
//     what this package guarantees is that it never runs on its own.
//   - The digest is the integrity control. A download whose SHA-256 does not
//     match the pin is deleted and reported. It is never kept, never used
//     "just this once", and a mismatch is never a warning.
//
// The transport guard is shared with urlfetch rather than
// re-derived: urlfetch.RejectNonPublic is the same dial-time IP policy, so a
// hostname that resolves into a private range cannot be reached from here
// either. urlfetch.Fetcher itself is not reused — it buffers the whole
// response in memory under a 25 MiB cap, and the only ways to fit a 40 MB
// binary through it would be to raise that cap or add an exported way to
// relax the guard, both of which weaken it for the callers it was written
// for.
package tunnelget

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/leazoot/fylane/companion/internal/urlfetch"
)

// MaxBytes caps a download. Larger than urlfetch's cap because these are
// program binaries rather than images: the pinned cloudflared builds run to
// about 40 MB, and the cap is a runaway guard, not a size policy.
const MaxBytes = 96 << 20

const maxRedirects = 5

// DefaultTimeout bounds the whole transfer. A tunnel binary over a slow link
// is a legitimate several-minute download, so this is far longer than the
// timeouts on anything a platform can trigger.
const DefaultTimeout = 10 * time.Minute

// Dir is where downloaded binaries live for a given data directory. Inside
// the data directory rather than anywhere on PATH: a program Fylane fetched
// is Fylane's to keep track of, and putting it somewhere the rest of the
// system executes from would be a much larger thing than what was agreed.
func Dir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "tools")
}

// ExeName is the file name a program has on this platform. Both the
// downloader and the process manager resolve binaries by this name, so it has
// exactly one definition.
func ExeName(binary string) string {
	if runtime.GOOS == "windows" {
		return binary + ".exe"
	}
	return binary
}

// A Getter performs the download. The zero value is what production uses;
// the unexported fields are test seams and there is no exported way to point
// this at another host or to soften the IP policy.
type Getter struct {
	Timeout time.Duration

	checkIP   func(net.IP) error
	endpoint  string
	tlsConfig *tls.Config
}

// Fetch downloads the pinned asset, verifies it, and installs the binary into
// dir. It returns the installed path.
//
// The caller must already have the user's consent for this specific download;
// see the package comment.
func (g *Getter) Fetch(ctx context.Context, pin Pin, dir string) (string, error) {
	if pin.Binary == "" || pin.URL == "" || pin.SHA256 == "" {
		return "", errors.New("incomplete pin")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot prepare the download directory: %w", err)
	}

	// A partial download must never be mistaken for a finished one, so the
	// bytes land under a temporary name and only a verified file is renamed
	// into place.
	tmp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return "", fmt.Errorf("cannot open a temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	sum, err := g.download(ctx, pin, tmp)
	if err != nil {
		return "", err
	}
	if sum != pin.SHA256 {
		return "", fmt.Errorf("%s failed its checksum: expected %s, got %s — the download was discarded",
			pin.Asset, pin.SHA256, sum)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("cannot finish writing the download: %w", err)
	}

	// Only now, with the bytes proven, is anything unpacked or made
	// executable. Extracting first would mean running the archive reader over
	// content nobody has vouched for.
	staged := tmpName
	if pin.Archive {
		staged, err = extract(tmpName, pin.Binary, dir)
		if err != nil {
			return "", err
		}
		defer os.Remove(staged)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return "", fmt.Errorf("cannot make %s executable: %w", pin.Binary, err)
	}
	// macOS refuses to run a file another program marked as downloaded. Our
	// own writes are not marked, so this is normally a no-op; it is here so
	// that a file which picked the mark up some other way still runs.
	if err := clearQuarantine(staged); err != nil {
		return "", err
	}

	dest := filepath.Join(dir, ExeName(pin.Binary))
	if err := os.Rename(staged, dest); err != nil {
		return "", fmt.Errorf("cannot install %s: %w", pin.Binary, err)
	}
	return dest, nil
}

// download streams the asset into w, returning the hex SHA-256 of what it
// wrote. The hash is computed on the way past rather than by re-reading the
// file: the bytes that were hashed are then provably the bytes on disk.
func (g *Getter) download(ctx context.Context, pin Pin, w io.Writer) (string, error) {
	rawURL := pin.URL
	if g.endpoint != "" {
		u, err := url.Parse(pin.URL)
		if err != nil {
			return "", err
		}
		rawURL = g.endpoint + u.Path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid download url: %w", err)
	}
	if req.URL.Scheme != "https" && g.endpoint == "" {
		return "", errors.New("only https downloads are allowed")
	}

	resp, err := g.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %d for %s", req.URL.Host, resp.StatusCode, pin.Asset)
	}
	if resp.ContentLength > MaxBytes {
		return "", fmt.Errorf("%s is %d bytes, over the %d MiB limit", pin.Asset, resp.ContentLength, MaxBytes>>20)
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("the download did not finish: %w", err)
	}
	if n > MaxBytes {
		return "", fmt.Errorf("%s exceeds the %d MiB limit", pin.Asset, MaxBytes>>20)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (g *Getter) client() *http.Client {
	check := g.checkIP
	if check == nil {
		check = urlfetch.RejectNonPublic
	}
	dialer := &net.Dialer{
		Timeout: 15 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("cannot verify dial target %q", host)
			}
			if err := check(ip); err != nil {
				return fmt.Errorf("refusing to download from %s: %w", host, err)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: g.timeout(),
		Transport: &http.Transport{
			DialContext:     dialer.DialContext,
			TLSClientConfig: g.tlsConfig,
			// The proxy environment is ignored on purpose: a proxy would
			// dial on our behalf and the IP policy above would never see the
			// real target.
			Proxy: nil,
		},
		// Release downloads redirect to the publisher's asset host, so a
		// same-host rule would break every real download. What is enforced
		// instead: the first request goes to the pinned address, every hop
		// stays on https and on a public address, and the bytes are accepted
		// only against the pinned digest. The digest is what makes the hop
		// host uninteresting — no redirect can produce different bytes and
		// still pass.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing a redirect to %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

func (g *Getter) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return DefaultTimeout
}

// extract pulls the one expected binary out of a gzipped tar and writes it
// beside the archive. Everything else in the archive is ignored: the entry
// names come from outside, so they choose nothing here — not the destination
// path, not the file mode, not whether an entry is written at all.
func extract(archivePath, binary, dir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("the download is not a gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("cannot read the archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binary {
			continue
		}
		out, err := os.CreateTemp(dir, ".unpacked-*")
		if err != nil {
			return "", err
		}
		n, err := io.Copy(out, io.LimitReader(tr, MaxBytes+1))
		if err == nil && n > MaxBytes {
			err = fmt.Errorf("%s in the archive exceeds the %d MiB limit", binary, MaxBytes>>20)
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
		return out.Name(), nil
	}
	return "", fmt.Errorf("the archive does not contain %s", binary)
}
