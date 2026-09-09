package directsrv

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/shared/authsrv"
	"github.com/leazoot/fylane/shared/tunnel"
)

// mcpProbe stands in for the MCP handler and records what reached it.
type mcpProbe struct {
	hits     int
	provider string
	host     string
}

func (m *mcpProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.hits++
	m.provider = r.Header.Get(tunnel.ProviderHeader)
	m.host = r.Host
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
}

func newTestServer(t *testing.T) (*Server, *mcpProbe, *httptest.Server) {
	t.Helper()
	probe := &mcpProbe{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(context.Background(), t.TempDir(), "test-host", probe, log)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.SetPublicURL(ts.URL)
	return s, probe, ts
}

// Device registration and pairing-code minting must not exist on the public
// surface: in direct mode there is no routing, so whoever mints a code pairs
// themselves to this machine's files.
func TestPublicSurfaceHasNoDeviceOrPairingEndpoints(t *testing.T) {
	_, _, ts := newTestServer(t)
	for _, path := range []string{"/v1/devices", "/v1/pair", "/v1/pair/approve"} {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d; want 404 (endpoint must not be published)", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(ts.URL + "/v1/pair/request?request_id=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/pair/request = %d; want 404", resp.StatusCode)
	}
}

func TestMCPRequiresAnAccessToken(t *testing.T) {
	_, probe, ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /mcp = %d; want 401", resp.StatusCode)
	}
	// The challenge must point at the resource metadata so a connector can
	// discover where to authenticate (RFC 9728).
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "/.well-known/oauth-protected-resource") {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if probe.hits != 0 {
		t.Fatal("unauthenticated request reached the MCP handler")
	}
}

// A token bound to any other device is refused: this deployment has exactly
// one device, so anything else came from somewhere it cannot speak for.
func TestMCPRejectsATokenBoundToAnotherDevice(t *testing.T) {
	s, probe, ts := newTestServer(t)
	raw := "at_foreign"
	sum := sha256.Sum256([]byte(raw))
	hashed := hex.EncodeToString(sum[:])
	if err := s.store.CreateDevice(&authsrv.Device{ID: "dev_other", SecretHash: "h", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateClient(&authsrv.Client{ID: "cl_x", RedirectURIs: []string{"https://p.example/cb"}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.PutAccessToken(&authsrv.AccessToken{
		Token: hashed, DeviceID: "dev_other", ClientID: "cl_x", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign-device token = %d; want 401", resp.StatusCode)
	}
	if probe.hits != 0 {
		t.Fatal("foreign-device token reached the MCP handler")
	}
}

// The whole connect flow with no relay in the path: a code minted locally,
// the standard OAuth dance over the public surface, then a call that lands on
// the MCP handler with the platform stamped.
func TestConnectFlowReachesTheMCPHandler(t *testing.T) {
	s, probe, ts := newTestServer(t)

	code, ttl, err := s.PairingCode(context.Background())
	if err != nil {
		t.Fatalf("PairingCode: %v", err)
	}
	if code == "" || ttl <= 0 {
		t.Fatalf("PairingCode = %q, %v", code, ttl)
	}

	redirectURI := "https://claude.ai/api/mcp/auth_callback"
	resp, err := http.Post(ts.URL+"/register", "application/json",
		strings.NewReader(`{"redirect_uris":["`+redirectURI+`"],"client_name":"Claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	var cl struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(resp.Body).Decode(&cl)
	resp.Body.Close()
	if cl.ClientID == "" {
		t.Fatal("no client id")
	}

	verifier := "direct-verifier-direct-verifier-987654"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"client_id": {cl.ClientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"},
	}
	resp, err = http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindSubmatch(page)
	if m == nil {
		t.Fatalf("no request id in the pairing page: %s", page)
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{"request_id": {string(m[1])}, "pairing_code": {code}}
	resp, err = noRedirect.Post(ts.URL+"/authorize", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	authCode := loc.Query().Get("code")
	if authCode == "" {
		t.Fatalf("no authorization code: %s", resp.Header.Get("Location"))
	}

	form = url.Values{
		"grant_type": {"authorization_code"}, "code": {authCode},
		"client_id": {cl.ClientID}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier},
	}
	resp, err = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(body, &tok)

	req, _ := http.NewRequest("POST", ts.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	// A caller-supplied platform header must not survive: the stamp comes from
	// the OAuth client the token was issued to.
	req.Header.Set(tunnel.ProviderHeader, "chatgpt")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized /mcp = %d", resp.StatusCode)
	}
	if probe.hits != 1 {
		t.Fatalf("MCP handler hits = %d; want 1", probe.hits)
	}
	if probe.provider != "claude" {
		t.Errorf("provider = %q; want claude (inferred from the client)", probe.provider)
	}
}

// The tunnel forwards the public hostname verbatim, and the MCP server
// refuses a non-loopback Host on a loopback listener (DNS-rebinding
// protection). Authenticated calls must arrive with the Host they actually
// came in on, or every real request through a tunnel answers 403.
func TestMCPRequestsArriveWithALoopbackHost(t *testing.T) {
	s, probe, ts := newTestServer(t)
	if err := s.store.CreateClient(&authsrv.Client{ID: "cl_h", RedirectURIs: []string{"https://p.example/cb"}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	raw := "at_host_probe"
	sum := sha256.Sum256([]byte(raw))
	if err := s.store.PutAccessToken(&authsrv.AccessToken{
		Token: hex.EncodeToString(sum[:]), DeviceID: authsrv.LocalDeviceID,
		ClientID: "cl_h", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Host = "tunnel.trycloudflare.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if probe.hits != 1 {
		t.Fatalf("MCP handler hits = %d; want 1", probe.hits)
	}
	if !strings.HasPrefix(probe.host, "127.0.0.1:") {
		t.Fatalf("MCP handler saw Host %q; want the loopback address it arrived on", probe.host)
	}
}

// Discovery must advertise wherever the tunnel currently publishes this
// machine — a quick tunnel renames it on every restart.
func TestDiscoveryFollowsThePublicURL(t *testing.T) {
	s, _, ts := newTestServer(t)
	s.SetPublicURL("https://renamed.example")
	resp, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	if doc["issuer"] != "https://renamed.example" {
		t.Fatalf("issuer = %v", doc["issuer"])
	}
	if doc["token_endpoint"] != "https://renamed.example/token" {
		t.Fatalf("token_endpoint = %v", doc["token_endpoint"])
	}
	if s.ConnectorURL() != "https://renamed.example/mcp" {
		t.Fatalf("ConnectorURL = %q", s.ConnectorURL())
	}
}

func TestConnectorURLEmptyWithoutATunnel(t *testing.T) {
	s, _, _ := newTestServer(t)
	s.SetPublicURL("")
	if s.ConnectorURL() != "" {
		t.Fatalf("ConnectorURL = %q; want empty without a public address", s.ConnectorURL())
	}
	if s.Connected() {
		t.Fatal("Connected without a public address")
	}
}
