package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/shared/tunnel"
)

// TestMCPOverTunnel exercises the full Phase 0 chain: MCP client → relay
// public endpoint → outbound WSS tunnel → Companion MCP handler → disk.
func TestMCPOverTunnel(t *testing.T) {
	root := t.TempDir()
	src := testDeps(t, root)

	ts, err := tunnel.NewServer("test-token-not-a-secret")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", ts.HandleTunnel)
	mux.Handle("/mcp", ts)
	relay := httptest.NewServer(mux)
	t.Cleanup(relay.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := &tunnel.Client{
		RelayURL: "ws" + strings.TrimPrefix(relay.URL, "http") + "/tunnel",
		Token:    "test-token-not-a-secret",
		Handler:  Handler(src, nil),
	}
	go client.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !ts.CompanionConnected() {
		if time.Now().After(deadline) {
			t.Fatal("companion did not register with the relay")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: relay.URL + "/mcp",
		// The phase 0 tunnel rejects standalone GET SSE streams by design.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("handshake through tunnel: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	var wr changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "tunneled.txt", "content": "via relay",
	}), &wr)
	if wr.Status != "applied" || wr.Operations[0].Status != "created" {
		t.Fatalf("write over tunnel: %+v", wr)
	}

	var rd readFileOutput
	structured(t, callTool(t, session, "read_file", map[string]any{"path": "tunneled.txt"}), &rd)
	if rd.Content != "via relay" || rd.SHA256 != wr.Operations[0].SHA256 {
		t.Fatalf("read over tunnel: %+v", rd)
	}
}
