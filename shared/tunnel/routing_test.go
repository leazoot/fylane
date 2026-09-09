package tunnel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// startDeviceRelay wires a Server whose auth derives the device identity
// from the bearer credential ("<device>:<secret>" style), like the OAuth
// relay does.
func startDeviceRelay(t *testing.T) (*Server, *httptest.Server, *lifecycleLog) {
	t.Helper()
	events := &lifecycleLog{}
	ts := NewServerAuth(func(r *http.Request) (string, error) {
		raw, ok := strings.CutPrefix(r.Header.Get(AuthHeader), "Bearer ")
		if !ok {
			return "", io.EOF
		}
		device, _, ok := strings.Cut(raw, ":")
		if !ok || device == "" {
			return "", io.EOF
		}
		return device, nil
	})
	ts.OnConnect = func(id string) { events.add("connect:" + id) }
	ts.OnDisconnect = func(id string) { events.add("disconnect:" + id) }

	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", ts.HandleTunnel)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return ts, httpServer, events
}

type lifecycleLog struct {
	mu     sync.Mutex
	events []string
}

func (l *lifecycleLog) add(e string) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *lifecycleLog) has(e string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, got := range l.events {
		if got == e {
			return true
		}
	}
	return false
}

func startDevice(t *testing.T, ts *Server, httpServer *httptest.Server, device, answer string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(answer))
	})
	client := &Client{RelayURL: wsURL(httpServer), Token: device + ":secret", Handler: handler, EagerSSE: true}
	go client.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !ts.DeviceConnected(device) {
		if time.Now().After(deadline) {
			t.Fatalf("device %s never connected", device)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cancel
}

func TestRoutingPerDevice(t *testing.T) {
	ts, httpServer, events := startDeviceRelay(t)
	cancelA := startDevice(t, ts, httpServer, "dev_a", "answer-from-a")
	defer cancelA()
	cancelB := startDevice(t, ts, httpServer, "dev_b", "answer-from-b")
	defer cancelB()

	// Each device's tunnel answers only its own requests.
	for device, want := range map[string]string{"dev_a": "answer-from-a", "dev_b": "answer-from-b"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader("x"))
		ts.ForwardTo(rec, req, device)
		if body := rec.Body.String(); body != want {
			t.Fatalf("device %s got %q, want %q", device, body, want)
		}
	}
	if !events.has("connect:dev_a") || !events.has("connect:dev_b") {
		t.Fatalf("missing connect events: %v", events.events)
	}
}

func TestOfflineDeviceFailsImmediately(t *testing.T) {
	ts, _, _ := startDeviceRelay(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("x"))
	start := time.Now()
	status := ts.ForwardTo(rec, req, "dev_offline")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("offline device status = %d, want 503", status)
	}
	// Offline writes are never queued: the answer is immediate.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("offline answer took %v, want immediate", elapsed)
	}
	if !strings.Contains(rec.Body.String(), "offline") {
		t.Fatalf("offline body = %q", rec.Body.String())
	}
}

func TestDisconnectReportsOffline(t *testing.T) {
	ts, httpServer, events := startDeviceRelay(t)
	cancel := startDevice(t, ts, httpServer, "dev_x", "hi")
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for !events.has("disconnect:dev_x") {
		if time.Now().After(deadline) {
			t.Fatalf("no disconnect event: %v", events.events)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ts.DeviceConnected("dev_x") {
		t.Fatal("device still marked connected")
	}
}
