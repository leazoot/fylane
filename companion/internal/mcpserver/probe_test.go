package mcpserver

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func startProbeSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(Handler(testDeps(t, t.TempDir()), &Options{EnableWaitProbe: true}))
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// TestWaitProbeHoldsCall verifies a tool call can stay pending and still
// complete — the local half of the approval-blocking validation. The
// default 2-second hold keeps the suite fast; set FYLANE_PROBE_TEST_SECONDS
// (e.g. 600) for the long manual runs against the real chain.
func TestWaitProbeHoldsCall(t *testing.T) {
	seconds := 2
	if v := os.Getenv("FYLANE_PROBE_TEST_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("FYLANE_PROBE_TEST_SECONDS: %v", err)
		}
		seconds = n
	}
	session := startProbeSession(t)

	start := time.Now()
	var out waitProbeOutput
	structured(t, callTool(t, session, "wait_probe", map[string]any{"seconds": seconds}), &out)
	elapsed := time.Since(start)

	if !out.Completed || out.RequestedSeconds != seconds {
		t.Fatalf("probe output: %+v", out)
	}
	if min := time.Duration(seconds) * time.Second; elapsed < min {
		t.Errorf("call returned after %s, want >= %s", elapsed, min)
	}
}

func TestWaitProbeRejectsBadDuration(t *testing.T) {
	session := startProbeSession(t)
	for _, s := range []int{0, -1, maxProbeSeconds + 1} {
		if res := callTool(t, session, "wait_probe", map[string]any{"seconds": s}); !res.IsError {
			t.Errorf("wait_probe(%d): expected error result", s)
		}
	}
}

// TestProbesHiddenByDefault ensures the diagnostic tools never leak into
// the default tool set.
func TestProbesHiddenByDefault(t *testing.T) {
	session, _ := startSession(t)
	for name, args := range map[string]map[string]any{
		"wait_probe":    {"seconds": 1},
		"payload_probe": {"mib": 0.01},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: name, Arguments: args,
		})
		if err == nil && !res.IsError {
			t.Errorf("%s callable without -probe", name)
		}
	}
}

func TestPayloadProbeSize(t *testing.T) {
	session := startProbeSession(t)
	var out payloadProbeOutput
	structured(t, callTool(t, session, "payload_probe", map[string]any{"mib": 0.25}), &out)

	want := int(0.25 * 1024 * 1024)
	if out.PayloadBytes != want || len(out.Payload) != want {
		t.Fatalf("payload_bytes=%d len=%d, want %d", out.PayloadBytes, len(out.Payload), want)
	}
	// The payload must not be trivially compressible filler: every 64-char
	// hex line is distinct.
	lines := map[string]bool{}
	for _, l := range strings.SplitN(out.Payload, "\n", 100)[:99] {
		if lines[l] {
			t.Fatalf("repeated payload line %q", l)
		}
		lines[l] = true
	}
}

func TestPayloadProbeRejectsBadSize(t *testing.T) {
	session := startProbeSession(t)
	for _, mib := range []float64{0, -1, maxPayloadMiB + 1} {
		if res := callTool(t, session, "payload_probe", map[string]any{"mib": mib}); !res.IsError {
			t.Errorf("payload_probe(%v): expected error result", mib)
		}
	}
}
