package redact

import (
	"strings"
	"testing"
)

func TestTextMasksCredentialShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		leaked  string
		wantHit bool
	}{
		{
			// The incident this package was written for: a process table
			// carrying another process's tunnel token.
			name:    "cloudflare tunnel token on a command line",
			in:      `61760 /usr/local/bin/cloudflared tunnel run --token eyJhIjoiZjkwZmQ3MTdiYTBiMWVjMDNjY2Y3YThjYzZlM2MzZmIiLCJ0IjoiZmIwY2I5MTUifQ`,
			leaked:  "eyJhIjoiZjkwZmQ3MTdiYTBiMWVjMDNjY2Y3YThjYzZlM2MzZmIiLCJ0IjoiZmIwY2I5MTUifQ",
			wantHit: true,
		},
		{
			name:    "jwt in a log line",
			in:      `Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop`,
			leaked:  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			wantHit: true,
		},
		{
			name:    "aws access key id",
			in:      "AWS_ACCESS_KEY_ID is AKIAIOSFODNN7EXAMPLE here",
			leaked:  "AKIAIOSFODNN7EXAMPLE",
			wantHit: true,
		},
		{
			name:    "github token",
			in:      "remote: using ghp_16C7e42F292c6912E7710c838347Ae178B4a",
			leaked:  "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
			wantHit: true,
		},
		{
			name:    "openai key",
			in:      "OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz012345",
			leaked:  "sk-proj-abcdefghijklmnopqrstuvwxyz012345",
			wantHit: true,
		},
		{
			name:    "slack token",
			in:      "webhook uses xoxb-123456789012-abcdefghijkl",
			leaked:  "xoxb-123456789012-abcdefghijkl",
			wantHit: true,
		},
		{
			name:    "environment dump",
			in:      "PATH=/usr/bin\nDATABASE_PASSWORD=hunter2trombone\nHOME=/Users/x",
			leaked:  "hunter2trombone",
			wantHit: true,
		},
		{
			name:    "flag with an equals sign",
			in:      "myapp --api-key=9f8e7d6c5b4a3210 --verbose",
			leaked:  "9f8e7d6c5b4a3210",
			wantHit: true,
		},
		{
			name:    "flag with a space",
			in:      "myapp --password s3cr3tvalue --port 80",
			leaked:  "s3cr3tvalue",
			wantHit: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.in)
			if strings.Contains(got, tc.leaked) == tc.wantHit {
				t.Fatalf("secret %q survived redaction:\n%s", tc.leaked, got)
			}
			if !strings.Contains(got, Placeholder) {
				t.Fatalf("nothing was marked as withheld:\n%s", got)
			}
		})
	}
}

func TestTextMasksAWholePrivateKeyBlockNotJustItsHeader(t *testing.T) {
	in := "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAxyz\nabcdefg\n-----END RSA PRIVATE KEY-----\nafter"
	got := Text(in)
	for _, leaked := range []string{"MIIEowIBAAKCAQEAxyz", "abcdefg", "BEGIN RSA PRIVATE KEY"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%q survived:\n%s", leaked, got)
		}
	}
	// Surrounding output must still be readable, or redaction becomes a
	// reason to turn it off.
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Fatalf("redaction ate the surrounding output:\n%s", got)
	}
}

// A pattern that fires on ordinary build output costs more than it saves: a
// developer who sees [redacted] where their test names should be stops
// trusting the audit surface, and the rung they reach for is the open one.
func TestTextLeavesOrdinaryOutputAlone(t *testing.T) {
	for _, s := range []string{
		"?? README.md",
		"ok  \tfylane/companion/internal/tasks\t0.412s",
		"PING 127.0.0.1 (127.0.0.1): 56 data bytes",
		"error: token expected near line 3",
		"go: downloading github.com/example/mod v1.2.3",
		"--- FAIL: TestAuthorizeRejectsAPlainKey (0.01s)",
		"export PATH=/usr/local/bin:$PATH",
	} {
		if got := Text(s); got != s {
			t.Fatalf("ordinary output was altered:\n in: %s\nout: %s", s, got)
		}
	}
}

func TestTextHandlesEmptyInput(t *testing.T) {
	if got := Text(""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestArgvMasksAValueInItsOwnArgument(t *testing.T) {
	got := Argv([]string{"cloudflared", "tunnel", "run", "--token", "abc123def456"})
	if got[4] != Placeholder {
		t.Fatalf("the value after --token was not masked: %v", got)
	}
	if got[0] != "cloudflared" || got[2] != "run" {
		t.Fatalf("the command itself was altered: %v", got)
	}
	// The input must not be modified in place: the audit log holds the same
	// slice and has to keep the truth.
	in := []string{"app", "--secret", "value"}
	Argv(in)
	if in[2] != "value" {
		t.Fatal("Argv mutated its input; the local audit record would lose the real value")
	}
}

func TestArgvDoesNotSwallowTheNextFlag(t *testing.T) {
	got := Argv([]string{"app", "--token", "--verbose"})
	if got[2] != "--verbose" {
		t.Fatalf("a following flag was mistaken for the secret: %v", got)
	}
}
