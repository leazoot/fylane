// Package tunnel implements the Fylane relay tunnel: the Relay accepts
// public MCP traffic over HTTP and forwards it, in memory only, to a
// Companion that connected outbound over WebSocket. Frames carry opaque HTTP
// request/response pairs — the tunnel never inspects or persists bodies.
package tunnel

import "net/http"

// FrameType discriminates tunnel frames. A response is streamed as one
// header frame, zero or more chunk frames, and a final end frame, so
// long-running handlers (SSE, blocked approvals) deliver bytes as they are
// produced instead of after completion.
type FrameType string

const (
	FrameRequest        FrameType = "request"
	FrameResponseHeader FrameType = "response_header"
	FrameResponseChunk  FrameType = "response_chunk"
	FrameResponseEnd    FrameType = "response_end"
)

// Frame is one tunneled message. Body is base64-encoded by encoding/json;
// it is forwarded verbatim and never logged.
type Frame struct {
	Type FrameType `json:"type"`
	// ID correlates response frames with their request. Assigned by the relay.
	ID uint64 `json:"id"`

	// Request fields.
	Method string      `json:"method,omitempty"`
	URL    string      `json:"url,omitempty"` // path and query only, never a host
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`

	// Response-header fields (Header is shared with requests, Body with
	// chunk frames).
	Status int `json:"status,omitempty"`
}

// MaxFrameBytes bounds a single tunneled message. Generous enough for the
// tool payload sizes probed in Phase 0 (up to tens of MiB).
const MaxFrameBytes = 64 << 20

// MaxChunkBytes bounds one response chunk; larger handler writes are split.
const MaxChunkBytes = 1 << 20

// ProviderHeader names the calling platform on forwarded MCP requests. The
// relay stamps it from OAuth client metadata (overwriting anything the
// caller sent); the Companion uses it only to pick an approval budget —
// never as an authorization signal.
const ProviderHeader = "X-Fylane-Provider"

// AuthHeader carries the shared tunnel token on the Companion's WebSocket
// dial. The token itself comes from the FYLANE_TUNNEL_TOKEN environment
// variable on both ends.
const AuthHeader = "Authorization"
