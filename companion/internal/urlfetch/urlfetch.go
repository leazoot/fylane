// Package urlfetch downloads images/attachments for browser saves with the
// SSRF guard this product mandates: HTTPS only, no private/loopback/link-
// local targets (checked at dial time against the actual resolved IP, so
// DNS rebinding cannot slip through), redirects re-validated per hop, and a
// hard response-size cap.
package urlfetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"syscall"
	"time"
)

// MaxBytes is the hard cap on downloaded content (targets 10 MiB
// images; attachments get headroom).
const MaxBytes = 25 << 20

const maxRedirects = 5

// Result is one guarded download.
type Result struct {
	Data        []byte
	ContentType string
	// SuggestedName is derived from the final URL path; may be empty.
	SuggestedName string
}

// Fetcher performs guarded downloads. The zero value is production-ready;
// checkIP and tlsConfig are injectable only from within the package
// (tests) — there is no exported way to weaken the guard or certificate
// validation.
type Fetcher struct {
	checkIP   func(net.IP) error
	tlsConfig *tls.Config
	Timeout   time.Duration
}

func (f *Fetcher) ipCheck() func(net.IP) error {
	if f.checkIP != nil {
		return f.checkIP
	}
	return RejectNonPublic
}

// RejectNonPublic is the production IP policy: only global unicast,
// non-private addresses may be dialed.
func RejectNonPublic(ip net.IP) error {
	switch {
	case ip.IsLoopback():
		return errors.New("loopback address")
	case ip.IsPrivate():
		return errors.New("private address")
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return errors.New("link-local address")
	case ip.IsMulticast() || ip.IsUnspecified():
		return errors.New("non-unicast address")
	case !ip.IsGlobalUnicast():
		return errors.New("non-global address")
	}
	return nil
}

// Fetch downloads url under the guard.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	check := f.ipCheck()
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
				return fmt.Errorf("refusing to fetch from %s: %w", host, err)
			}
			return nil
		},
	}
	client := &http.Client{
		Timeout: f.timeout(),
		Transport: &http.Transport{
			DialContext:     dialer.DialContext,
			TLSClientConfig: f.tlsConfig,
			// The proxy env is ignored on purpose: a proxy would bypass the
			// dial-time IP policy.
			Proxy: nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-https URL refused")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if req.URL.Scheme != "https" {
		return nil, errors.New("only https downloads are allowed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxBytes {
		return nil, fmt.Errorf("content of %d bytes exceeds the %d MiB limit", resp.ContentLength, MaxBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("content exceeds the %d MiB limit", MaxBytes>>20)
	}

	name := path.Base(resp.Request.URL.Path)
	if name == "/" || name == "." || !strings.Contains(name, ".") {
		name = ""
	}
	return &Result{
		Data:          data,
		ContentType:   resp.Header.Get("Content-Type"),
		SuggestedName: name,
	}, nil
}

func (f *Fetcher) timeout() time.Duration {
	if f.Timeout > 0 {
		return f.Timeout
	}
	return 60 * time.Second
}
