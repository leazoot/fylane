package connectinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRelay serves the two discovery documents the way authsrv does; the
// test asserts against this contract, not the relay package (which is not
// importable across module internal boundaries).
func fakeRelay(t *testing.T, dcr, s256 bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var ts *httptest.Server
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"resource":"` + ts.URL + `/mcp","authorization_servers":["` + ts.URL + `"]}`))
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"issuer":"` + ts.URL + `","authorization_endpoint":"` + ts.URL + `/authorize","token_endpoint":"` + ts.URL + `/token"`
		if dcr {
			body += `,"registration_endpoint":"` + ts.URL + `/register"`
		}
		if s256 {
			body += `,"code_challenge_methods_supported":["S256"]`
		}
		w.Write([]byte(body + `}`))
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestInspectHealthyRelay(t *testing.T) {
	ts := fakeRelay(t, true, true)
	r, err := Inspect(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 0 {
		t.Fatalf("problems = %v, want none", r.Problems)
	}
	if r.ConnectorURL != ts.URL+"/mcp" || r.Issuer != ts.URL || !r.DCR || !r.PKCES256 {
		t.Fatalf("report = %+v", r)
	}
}

func TestInspectFlagsMissingCapabilities(t *testing.T) {
	ts := fakeRelay(t, false, false)
	r, err := Inspect(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.Problems, "; ")
	if !strings.Contains(joined, "registration_endpoint") || !strings.Contains(joined, "S256") {
		t.Fatalf("problems = %v", r.Problems)
	}
}

func TestInspectUnreachableRelay(t *testing.T) {
	ts := fakeRelay(t, true, true)
	ts.Close()
	r, err := Inspect(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) == 0 || !strings.Contains(r.Problems[0], "unreachable") {
		t.Fatalf("problems = %v", r.Problems)
	}
}

func TestInspectLegacyModeRelay(t *testing.T) {
	// A relay without OAuth (legacy shared-token mode) serves no discovery
	// documents; the report must say so instead of looking healthy.
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	r, err := Inspect(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) == 0 || !strings.Contains(r.Problems[0], "OAuth mode") {
		t.Fatalf("problems = %v", r.Problems)
	}
}

func TestInspectRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"", "ftp://relay", "not a url"} {
		if _, err := Inspect(context.Background(), bad); err == nil {
			t.Errorf("Inspect(%q) must fail", bad)
		}
	}
}

func TestRenderIncludesEssentials(t *testing.T) {
	var b strings.Builder
	Render(&b, &Report{
		RelayBase:    "https://relay.example",
		ConnectorURL: "https://relay.example/mcp",
		Issuer:       "https://relay.example",
		DCR:          true, PKCES256: true,
		DeviceID: "dev_1",
	})
	out := b.String()
	for _, want := range []string{"https://relay.example/mcp", "paired (dev_1)", "Claude", "ChatGPT", "Grok", "pairing code"} {
		if !strings.Contains(out, want) {
			t.Errorf("render output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Problems") {
		t.Error("healthy report must not render a problems section")
	}
}
