package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/directsrv"
)

// `share` has no window to poll, so the daemon tells it where the machine
// became reachable. Both halves matter: the address platforms connect to, and
// a way to authorize one — a URL alone leaves the caller at a login screen
// with nothing to type.
func TestReadyIsToldHowToReachAndHowToAuthorize(t *testing.T) {
	fakeTunnelBinary(t)
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	direct, err := directsrv.Open(ctx, dataDir, "test-host", http.NotFoundHandler(), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()

	a := New(&Config{DataDir: dataDir, Addr: "127.0.0.1:8787",
		DirectAddr: "127.0.0.1:8892", TunnelProvider: "cloudflare-quick"}, testLogger())

	announced := make(chan string, 1)
	var mint func(context.Context) (string, time.Duration, error)
	a.Ready = func(url string, code func(context.Context) (string, time.Duration, error)) {
		mint = code
		announced <- url
	}

	m, err := a.startTunnel(ctx, direct)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	var url string
	select {
	case url = <-announced:
	case <-time.After(10 * time.Second):
		t.Fatal("the tunnel came up and nothing was announced")
	}
	// The connector URL, not the bare public one: it is announced after the
	// address is in place, so a caller is never sent to the previous one.
	if url != "https://switched-tunnel.trycloudflare.com/mcp" {
		t.Fatalf("announced %q", url)
	}
	if mint == nil {
		t.Fatal("no way to issue a pairing code came with the address")
	}
	code, ttl, err := mint(ctx)
	if err != nil {
		t.Fatalf("issuing a pairing code: %v", err)
	}
	if code == "" || ttl <= 0 {
		t.Fatalf("code = %q, valid for %v", code, ttl)
	}
}
