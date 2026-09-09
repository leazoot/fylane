package tunnel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-token-not-a-secret"

// startRelay returns a relay-like server wiring Server into /tunnel + /mcp.
func startRelay(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	ts, err := NewServer(testToken)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", ts.HandleTunnel)
	mux.Handle("/mcp", ts)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return ts, httpServer
}

func wsURL(httpServer *httptest.Server) string {
	return "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/tunnel"
}

// startClient runs a tunnel client over handler and waits for registration.
func startClient(t *testing.T, ts *Server, httpServer *httptest.Server, handler http.Handler) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{RelayURL: wsURL(httpServer), Token: testToken, Handler: handler, EagerSSE: true}
	go client.Run(ctx)
	waitForCompanion(t, ts, true)
	return cancel
}

func waitForCompanion(t *testing.T, ts *Server, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ts.CompanionConnected() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("companion connected != %v within timeout", want)
}

func TestForwardRoundTrip(t *testing.T) {
	ts, httpServer := startRelay(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Method", r.Method)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "echo:%s:%s", r.URL.RequestURI(), body)
	})
	cancel := startClient(t, ts, httpServer, handler)
	defer cancel()

	start := time.Now()
	resp, err := http.Post(httpServer.URL+"/mcp", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rtt := time.Since(start)

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated || string(body) != "echo:/mcp:hello" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Echo-Method") != "POST" {
		t.Errorf("response header not forwarded")
	}
	// Acceptance: local round trip well under the 1s LAN budget.
	if rtt > time.Second {
		t.Errorf("round trip took %s, want < 1s", rtt)
	}
}

func TestForwardConcurrent(t *testing.T) {
	ts, httpServer := startRelay(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		time.Sleep(50 * time.Millisecond)
		w.Write(body)
	})
	cancel := startClient(t, ts, httpServer, handler)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := fmt.Sprintf("req-%d", i)
			resp, err := http.Post(httpServer.URL+"/mcp", "text/plain", strings.NewReader(payload))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != payload {
				errs <- fmt.Errorf("got %q, want %q", body, payload)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestOfflineCompanion(t *testing.T) {
	_, httpServer := startRelay(t)
	resp, err := http.Post(httpServer.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestTunnelAuthRequired(t *testing.T) {
	ts, httpServer := startRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	bad := &Client{RelayURL: wsURL(httpServer), Token: "wrong-token", Handler: http.NotFoundHandler()}
	go bad.Run(ctx)
	<-ctx.Done()
	if ts.CompanionConnected() {
		t.Fatal("companion with a wrong token was accepted")
	}
}

func TestReconnect(t *testing.T) {
	ts, httpServer := startRelay(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	cancel := startClient(t, ts, httpServer, handler)
	defer cancel()

	// Kill the active tunnel from the relay side; the client must dial back.
	ts.mu.Lock()
	conn := ts.conns[legacyIdentity]
	ts.mu.Unlock()
	conn.close(4000, "test-induced drop")
	waitForCompanion(t, ts, false)
	waitForCompanion(t, ts, true)

	resp, err := http.Post(httpServer.URL+"/mcp", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); string(body) != "ok" {
		t.Fatalf("after reconnect got %q", body)
	}
}

// TestStreamingResponse proves chunks reach the caller while the handler is
// still running (no buffering until completion).
func TestStreamingResponse(t *testing.T) {
	ts, httpServer := startRelay(t)
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		w.Write([]byte("part1\n"))
		w.(http.Flusher).Flush()
		<-release // hold the handler open until the test saw part1
		w.Write([]byte("part2\n"))
	})
	cancel := startClient(t, ts, httpServer, handler)
	defer cancel()

	resp, err := http.Post(httpServer.URL+"/mcp", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil || string(buf[:n]) != "part1\n" {
		t.Fatalf("first read = %q, %v; want part1 while handler still running", buf[:n], err)
	}
	close(release) // only now may the handler finish
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(rest) != "part2\n" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
}

// TestSSEKeepalive verifies comment heartbeats flow while an SSE handler is
// silently blocked (the pending-approval scenario).
func TestSSEKeepalive(t *testing.T) {
	old := sseKeepaliveInterval
	sseKeepaliveInterval = 50 * time.Millisecond
	defer func() { sseKeepaliveInterval = old }()

	ts, httpServer := startRelay(t)
	release := make(chan struct{})
	// Mirrors the MCP SDK: the SSE content type is set immediately but
	// nothing is written (headers not flushed) until the tool completes.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-release
		w.Write([]byte("event: message\ndata: done\n\n"))
	})
	cancel := startClient(t, ts, httpServer, handler)
	defer cancel()

	resp, err := http.Post(httpServer.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// While the handler blocks, at least one keepalive comment must arrive.
	buf := make([]byte, 256)
	n, err := resp.Body.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), ": keepalive") {
		t.Fatalf("first read = %q, %v; want keepalive comment", buf[:n], err)
	}
	close(release)
	rest, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(rest), "data: done") {
		t.Fatalf("rest = %q, %v", rest, err)
	}
}

func TestGetRejected(t *testing.T) {
	ts, httpServer := startRelay(t)
	cancel := startClient(t, ts, httpServer, http.NotFoundHandler())
	defer cancel()

	resp, err := http.Get(httpServer.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}
}

// MaxFrameBytes is a published number: the security notes name the
// tunnel frame ceiling as part of the answer to oversized writes. The three
// call sites all spend the constant, so nothing else would fail if it grew.
func TestThePublishedFrameLimitIsTheOneWeShip(t *testing.T) {
	if MaxFrameBytes != 64<<20 {
		t.Errorf("MaxFrameBytes = %d, published as a 64 MiB tunnel frame", MaxFrameBytes)
	}
}
