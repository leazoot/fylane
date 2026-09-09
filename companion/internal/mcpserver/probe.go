package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxProbeSeconds caps wait_probe at just under the tunnel's 15-minute
// forward budget so the probe, not the tunnel, is the limiting factor.
const maxProbeSeconds = 14 * 60

type waitProbeInput struct {
	Seconds int `json:"seconds" jsonschema:"How long to hold the call open, in seconds (1-840)."`
}

type waitProbeOutput struct {
	RequestedSeconds int    `json:"requested_seconds"`
	ElapsedMS        int64  `json:"elapsed_ms"`
	Completed        bool   `json:"completed"`
	Note             string `json:"note,omitempty"`
}

// waitProbe simulates a pending local approval by blocking for the
// requested duration. It observes context cancellation so an abandoned call
// does not leak the goroutine.
func (t *toolset) waitProbe(ctx context.Context, _ *mcp.CallToolRequest, in waitProbeInput) (*mcp.CallToolResult, waitProbeOutput, error) {
	var zero waitProbeOutput
	if in.Seconds < 1 || in.Seconds > maxProbeSeconds {
		return nil, zero, errors.New("seconds must be between 1 and 840")
	}
	start := time.Now()
	timer := time.NewTimer(time.Duration(in.Seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil, waitProbeOutput{
			RequestedSeconds: in.Seconds,
			ElapsedMS:        time.Since(start).Milliseconds(),
			Completed:        true,
			Note:             "call survived the wait; the platform did not time out first",
		}, nil
	case <-ctx.Done():
		return nil, zero, ctx.Err()
	}
}

// maxPayloadMiB caps payload_probe under the tunnel's 64 MiB frame budget;
// Platform measurements are wanted up to 50 MiB.
const maxPayloadMiB = 50.0

type payloadProbeInput struct {
	MiB float64 `json:"mib" jsonschema:"Approximate payload size to return, in MiB (0.01-50)."`
}

type payloadProbeOutput struct {
	RequestedMiB float64 `json:"requested_mib"`
	PayloadBytes int     `json:"payload_bytes"`
	Payload      string  `json:"payload"`
}

// payloadProbe returns a text payload of the requested size so platform
// tool-output limits can be measured. The content is a deterministic
// pseudo-random hex stream: repetitive filler would compress away in transit
// and understate what the platform actually accepts.
func (t *toolset) payloadProbe(ctx context.Context, _ *mcp.CallToolRequest, in payloadProbeInput) (*mcp.CallToolResult, payloadProbeOutput, error) {
	var zero payloadProbeOutput
	if in.MiB < 0.01 || in.MiB > maxPayloadMiB {
		return nil, zero, errors.New("mib must be between 0.01 and 50")
	}
	if ctx.Err() != nil {
		return nil, zero, ctx.Err()
	}
	size := int(in.MiB * 1024 * 1024)
	return nil, payloadProbeOutput{
		RequestedMiB: in.MiB,
		PayloadBytes: size,
		Payload:      payloadText(size),
	}, nil
}

// payloadText generates size bytes of xorshift64-based hex, one 64-char line
// per four states.
func payloadText(size int) string {
	var b strings.Builder
	b.Grow(size + 65)
	x := uint64(0x9e3779b97f4a7c15)
	for b.Len() < size {
		for i := 0; i < 4; i++ {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			fmt.Fprintf(&b, "%016x", x)
		}
		b.WriteByte('\n')
	}
	return b.String()[:size]
}
