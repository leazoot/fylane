package urlfetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loopbackOnly lets tests reach httptest servers while still exercising the
// dial-time control path; the production policy is tested separately.
func loopbackOnly(ip net.IP) error {
	if !ip.IsLoopback() {
		return fmt.Errorf("test policy: only loopback")
	}
	return nil
}

// testFetcher trusts the test server's certificate; the guard logic itself
// stays production-strict apart from the loopback allowance.
func testFetcher(t *testing.T, srv *httptest.Server) *Fetcher {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &Fetcher{checkIP: loopbackOnly, tlsConfig: &tls.Config{RootCAs: pool}}
}

func newGuardedServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/img/pic.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNGDATA"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, MaxBytes+1))
	})
	mux.HandleFunc("/declared-big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(MaxBytes+1))
	})
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/img/pic.png", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	})
	mux.HandleFunc("/leave-https", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/x", http.StatusFound)
	})
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRejectNonPublic(t *testing.T) {
	bad := []string{"127.0.0.1", "::1", "10.0.0.8", "172.16.3.4", "192.168.1.1", "169.254.169.254", "fe80::1", "224.0.0.1", "0.0.0.0", "fc00::1"}
	for _, s := range bad {
		if err := RejectNonPublic(net.ParseIP(s)); err == nil {
			t.Errorf("%s must be rejected", s)
		}
	}
	good := []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, s := range good {
		if err := RejectNonPublic(net.ParseIP(s)); err != nil {
			t.Errorf("%s must be allowed: %v", s, err)
		}
	}
}

func TestFetchHappyPathAndRedirect(t *testing.T) {
	srv := newGuardedServer(t)
	f := testFetcher(t, srv)

	res, err := f.Fetch(context.Background(), srv.URL+"/img/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "PNGDATA" || res.ContentType != "image/png" || res.SuggestedName != "pic.png" {
		t.Fatalf("result = %+v", res)
	}

	res, err = f.Fetch(context.Background(), srv.URL+"/hop")
	if err != nil || string(res.Data) != "PNGDATA" {
		t.Fatalf("redirected fetch = %+v, %v", res, err)
	}
}

func TestFetchEnforcesLimits(t *testing.T) {
	srv := newGuardedServer(t)
	f := testFetcher(t, srv)

	if _, err := f.Fetch(context.Background(), srv.URL+"/big"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized download must fail, got %v", err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("redirect loop must fail, got %v", err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/leave-https"); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("https downgrade redirect must fail, got %v", err)
	}
	// A declared-oversize Content-Length is refused before the body is read.
	if _, err := f.Fetch(context.Background(), srv.URL+"/declared-big"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("declared oversize must fail, got %v", err)
	}
}

func TestRedirectHopRevalidatedAtDial(t *testing.T) {
	// Target on IPv6 loopback, entry on IPv4 loopback, policy allowing IPv4
	// only: structurally the redirect-into-private-range attack. The per-hop
	// dial check must refuse the second connection.
	l6, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("SECRET"))
	}))
	target.Listener.Close()
	target.Listener = l6
	target.StartTLS()
	t.Cleanup(target.Close)
	targetURL := "https://" + l6.Addr().String() + "/loot"

	var entry *httptest.Server
	entry = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	t.Cleanup(entry.Close)

	pool := x509.NewCertPool()
	pool.AddCert(entry.Certificate())
	pool.AddCert(target.Certificate())
	ipv4Only := func(ip net.IP) error {
		if ip.To4() != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("refusing to fetch %s", ip)
	}
	f := &Fetcher{checkIP: ipv4Only, tlsConfig: &tls.Config{RootCAs: pool}}

	res, err := f.Fetch(context.Background(), entry.URL)
	if err == nil || strings.Contains(fmt.Sprint(res), "SECRET") {
		t.Fatalf("redirect to a policy-refused host must fail at dial time, got %v, %v", res, err)
	}
}

func TestProxyEnvironmentIgnored(t *testing.T) {
	// The dial-time IP policy only holds when connections go direct; a
	// proxy honoring HTTPS_PROXY would tunnel around it.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	srv := newGuardedServer(t)
	f := testFetcher(t, srv)
	if _, err := f.Fetch(context.Background(), srv.URL+"/img/pic.png"); err != nil {
		t.Fatalf("fetch honored the proxy environment: %v", err)
	}
}

func TestFetchRefusesHTTPAndPrivateTargets(t *testing.T) {
	f := &Fetcher{}
	if _, err := f.Fetch(context.Background(), "http://example.com/a.png"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http must be refused, got %v", err)
	}

	// An https URL pointing at a loopback listener: the production policy
	// must refuse at dial time, before any TLS or protocol work happens.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	u := "https://" + strings.TrimPrefix(srv.URL, "http://")
	if _, err := f.Fetch(context.Background(), u); err == nil || !strings.Contains(err.Error(), "refusing to fetch") {
		t.Fatalf("loopback dial must be refused by the IP policy, got %v", err)
	}
}
