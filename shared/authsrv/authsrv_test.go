package authsrv

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// brokenStore simulates an unreachable database: every lookup fails with an
// infrastructure error (NOT ErrNotFound).
type brokenStore struct{ *MemoryStore }

var errDown = errors.New("connection refused")

func (b brokenStore) GetDevice(string) (*Device, error)                 { return nil, errDown }
func (b brokenStore) GetClient(string) (*Client, error)                 { return nil, errDown }
func (b brokenStore) TakeAuthCode(string, time.Time) (*AuthCode, error) { return nil, errDown }
func (b brokenStore) GetAccessToken(string, time.Time) (*AccessToken, error) {
	return nil, errDown
}
func (b brokenStore) GetRefreshToken(string) (*RefreshToken, error) { return nil, errDown }

// writeFailStore reads fine and cannot write. This is the half of an outage
// the lookup-only broken store cannot reach: the request gets past every
// check and then the store will not record what it decided.
type writeFailStore struct {
	*MemoryStore
	failMark   bool
	failRevoke bool
}

func (s writeFailStore) MarkRefreshTokenUsed(token string) error {
	if s.failMark {
		return errDown
	}
	return s.MemoryStore.MarkRefreshTokenUsed(token)
}

func (s writeFailStore) RevokeRefreshFamily(family string) (int, error) {
	if s.failRevoke {
		return 0, errDown
	}
	return s.MemoryStore.RevokeRefreshFamily(family)
}

// seedRefresh puts one usable refresh token in the store and returns the raw
// value a client would present.
func seedRefresh(t *testing.T, store Store, family string, used bool) string {
	t.Helper()
	raw := "rt_" + family
	err := store.PutRefreshToken(&RefreshToken{
		Token:     hashSecret(raw),
		Family:    family,
		DeviceID:  "dev_1",
		ClientID:  "cl_1",
		ExpiresAt: time.Now().Add(time.Hour),
		Used:      used,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func postRefresh(t *testing.T, s *Server, raw string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	s.Routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.Issuer = ts.URL

	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {raw}, "client_id": {"cl_1"}}
	resp, err := http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A store that cannot record the rotation is an outage like any other. It was
// answering 500 server_error — not harmful, since a platform only discards
// credentials on invalid_grant, but it was the one store failure here that did
// not tell the caller to come back shortly.
func TestAFailedRotationWriteIsRetryable(t *testing.T) {
	store := writeFailStore{MemoryStore: NewMemoryStore(), failMark: true}
	raw := seedRefresh(t, store, "fam_1", false)

	status, body := postRefresh(t, &Server{Store: store}, raw)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("refresh with an unwritable store = %d %s, want 503", status, body)
	}
	if strings.Contains(body, "invalid_grant") {
		t.Fatalf("a write failure was answered as invalid_grant: %s", body)
	}
}

// Rotation reuse during a store outage: the refusal stands, because a used
// token is invalid whether or not the family could be revoked, and a 503 here
// would just invite the replayer to try again.
//
// What must not happen is the response claiming the sessions were revoked
// when they were not. An operator reading that line would close the incident.
func TestAFailedRevocationIsNotReportedAsARevocation(t *testing.T) {
	store := writeFailStore{MemoryStore: NewMemoryStore(), failRevoke: true}
	raw := seedRefresh(t, store, "fam_2", true)

	status, body := postRefresh(t, &Server{Store: store}, raw)
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("replayed token = %d %s, want a 400 invalid_grant", status, body)
	}
	if strings.Contains(body, "revoked") {
		t.Errorf("the response claims a revocation that did not happen: %s", body)
	}

	// With a working store the claim is true, and is still made.
	ok := writeFailStore{MemoryStore: NewMemoryStore()}
	raw = seedRefresh(t, ok, "fam_3", true)
	status, body = postRefresh(t, &Server{Store: ok}, raw)
	if status != http.StatusBadRequest || !strings.Contains(body, "revoked") {
		t.Fatalf("replayed token with a working store = %d %s, want the revocation stated", status, body)
	}
}

// The count alone cannot carry the failure: zero revoked and could-not-revoke
// are different facts, and the interface has to be able to say which.
func TestRevokingAFamilyReportsWhetherItHappened(t *testing.T) {
	store := NewMemoryStore()
	seedRefresh(t, store, "fam_4", false)

	n, err := store.RevokeRefreshFamily("fam_4")
	if err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v; want 1, nil", n, err)
	}
	// Nothing left to revoke is a success, not a failure.
	n, err = store.RevokeRefreshFamily("fam_4")
	if err != nil || n != 0 {
		t.Fatalf("second revoke = %d, %v; want 0, nil", n, err)
	}
	if _, err := (writeFailStore{MemoryStore: store, failRevoke: true}).RevokeRefreshFamily("fam_4"); err == nil {
		t.Error("a store failure reported success")
	}
}

// A store outage must surface as a retryable 503, never as invalid_grant —
// an invalid_grant makes the platform discard a perfectly good refresh
// token (live incident 2026-08-06: Docker stop → PG down → ChatGPT dropped
// its credentials and demanded re-authentication).
func TestStoreOutageIsRetryableNotInvalidGrant(t *testing.T) {
	s := &Server{Store: brokenStore{NewMemoryStore()}}
	mux := http.NewServeMux()
	s.Routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.Issuer = ts.URL

	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"rt_x"}, "client_id": {"cl_x"}}
	resp, err := http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("refresh during outage = %d %s, want 503", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "invalid_grant") {
		t.Fatalf("outage answered as invalid_grant: %s", body)
	}

	form = url.Values{"grant_type": {"authorization_code"}, "code": {"ac_x"}, "client_id": {"cl_x"}, "code_verifier": {"v"}}
	resp, _ = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("code exchange during outage = %d, want 503", resp.StatusCode)
	}

	req, _ := http.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer at_x")
	if _, _, err := s.ValidateBearerProvider(req); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("ValidateBearerProvider outage err = %v, want ErrStoreUnavailable", err)
	}
	if _, err := s.ValidateBearer(req); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("ValidateBearer outage err = %v, want ErrStoreUnavailable", err)
	}
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{Store: NewMemoryStore()}
	mux := http.NewServeMux()
	s.Routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.Issuer = ts.URL
	return s, ts
}

// noRedirect returns a client that surfaces 302s instead of following them.
func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func postJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func postForm(t *testing.T, client *http.Client, u string, form url.Values, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

// registerDevice registers a device and requests a pairing code.
func registerDevice(t *testing.T, ts *httptest.Server) (creds string, pairingCode string) {
	t.Helper()
	resp, body := postJSON(t, ts.URL+"/v1/devices", map[string]string{"name": "test-mac"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("device register = %d %s", resp.StatusCode, body)
	}
	var dev struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	json.Unmarshal(body, &dev)
	creds = dev.DeviceID + ":" + dev.DeviceSecret

	resp, body = postForm(t, http.DefaultClient, ts.URL+"/v1/pair", url.Values{},
		map[string]string{"Authorization": "Bearer " + creds})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pairing code = %d %s", resp.StatusCode, body)
	}
	var pc struct {
		PairingCode string `json:"pairing_code"`
	}
	json.Unmarshal(body, &pc)
	return creds, pc.PairingCode
}

// registerClient runs dynamic client registration.
func registerClient(t *testing.T, ts *httptest.Server) (clientID, redirectURI string) {
	t.Helper()
	redirectURI = "https://platform.example/oauth/callback"
	resp, body := postJSON(t, ts.URL+"/register", map[string]any{
		"redirect_uris": []string{redirectURI}, "client_name": "Claude",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d %s", resp.StatusCode, body)
	}
	var cl struct {
		ClientID string `json:"client_id"`
	}
	json.Unmarshal(body, &cl)
	return cl.ClientID, redirectURI
}

var requestIDRe = regexp.MustCompile(`name="request_id" value="([^"]+)"`)

// authorize runs the authorize flow with the pairing code and returns the
// authorization code.
func authorize(t *testing.T, ts *httptest.Server, clientID, redirectURI, challenge, pairingCode string) string {
	t.Helper()
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "state": {"st4te"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	resp, err := http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize GET = %d %s", resp.StatusCode, page)
	}
	m := requestIDRe.FindSubmatch(page)
	if m == nil {
		t.Fatalf("no request_id in page: %s", page)
	}

	resp, body := postForm(t, noRedirect(), ts.URL+"/authorize", url.Values{
		"request_id": {string(m[1])}, "pairing_code": {pairingCode},
	}, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize POST = %d %s", resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), redirectURI) || loc.Query().Get("state") != "st4te" {
		t.Fatalf("redirect = %s", loc)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect: %s", loc)
	}
	return code
}

// TestPushPairingFlow walks the zero-typing path end to end and
// closes the replay/confusion doors: info lookup requires device creds, a
// request approves once, the continuation nonce is single-use, and a wrong
// nonce burns the request instead of leaving it retryable.
func TestPushPairingFlow(t *testing.T) {
	_, ts := newTestServer(t)
	creds, _ := registerDevice(t, ts)
	clientID, redirectURI := registerClient(t, ts)
	verifier, challenge := pkcePair()

	// Authorize page: extract request id and the displayed verify code.
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"pushstate"},
	}
	resp, err := http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	rid := requestIDRe.FindSubmatch(page)
	if rid == nil {
		t.Fatalf("no request id in page")
	}
	requestID := string(rid[1])
	vc := regexp.MustCompile(`([2-9A-HJKMNP-Z]{2}-[2-9A-HJKMNP-Z]{2})`).FindSubmatch(page)
	if vc == nil {
		t.Fatalf("no verify code in page: %s", page)
	}

	// Info lookup needs device credentials; anonymous callers get nothing.
	resp, err = http.Get(ts.URL + "/v1/pair/request?request_id=" + requestID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous pair info = %d, want 401", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/v1/pair/request?request_id="+requestID, nil)
	req.Header.Set("Authorization", "Bearer "+creds)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var info struct {
		ClientName string `json:"client_name"`
		VerifyCode string `json:"verify_code"`
	}
	json.Unmarshal(body, &info)
	if resp.StatusCode != http.StatusOK || info.VerifyCode != string(vc[1]) {
		t.Fatalf("pair info = %d %s, page code %s", resp.StatusCode, body, vc[1])
	}

	// Approve with device creds; second approve must conflict.
	approve := func() (*http.Response, []byte) {
		raw, _ := json.Marshal(map[string]string{"request_id": requestID})
		req, _ := http.NewRequest("POST", ts.URL+"/v1/pair/approve", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+creds)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	resp, body = approve()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, body)
	}
	var appr struct {
		Nonce string `json:"nonce"`
	}
	json.Unmarshal(body, &appr)
	if appr.Nonce == "" {
		t.Fatal("no nonce")
	}
	if resp, _ = approve(); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409", resp.StatusCode)
	}

	// Continue with the nonce: standard code redirect, then tokens.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, body = postForm(t, noRedirect, ts.URL+"/authorize/continue",
		url.Values{"request_id": {requestID}, "nonce": {appr.Nonce}}, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("continue = %d %s", resp.StatusCode, body)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("state") != "pushstate" || loc.Query().Get("code") == "" {
		t.Fatalf("continue redirect = %s", resp.Header.Get("Location"))
	}
	resp, body = postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token after push pairing = %d %s", resp.StatusCode, body)
	}

	// Replay: the request was consumed with the nonce.
	resp, _ = postForm(t, noRedirect, ts.URL+"/authorize/continue",
		url.Values{"request_id": {requestID}, "nonce": {appr.Nonce}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("continue replay = %d, want 400", resp.StatusCode)
	}
}

// A wrong continuation nonce burns the request: guessing is not retryable.
func TestPushPairingBadNonceBurnsRequest(t *testing.T) {
	s, ts := newTestServer(t)
	creds, _ := registerDevice(t, ts)
	clientID, redirectURI := registerClient(t, ts)
	_, challenge := pkcePair()

	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"},
	}
	resp, err := http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	requestID := string(requestIDRe.FindSubmatch(page)[1])

	raw, _ := json.Marshal(map[string]string{"request_id": requestID})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/pair/approve", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+creds)
	req.Header.Set("Content-Type", "application/json")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ = postForm(t, noRedirect, ts.URL+"/authorize/continue",
		url.Values{"request_id": {requestID}, "nonce": {"pn_guessed"}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad nonce = %d, want 400", resp.StatusCode)
	}
	// Even the real device cannot resurrect it: the request is gone.
	if resp, _ = postForm(t, noRedirect, ts.URL+"/authorize/continue",
		url.Values{"request_id": {requestID}, "nonce": {"pn_anything"}}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("burned request answered %d", resp.StatusCode)
	}
	_ = s
}

func pkcePair() (verifier, challenge string) {
	verifier = "test-verifier-test-verifier-test-verifier-1234"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestInferProvider(t *testing.T) {
	cases := []struct {
		name string
		uris []string
		want string
	}{
		{"Claude", []string{"https://claude.ai/api/mcp/auth_callback"}, "claude"},
		{"", []string{"https://claude.ai/api/mcp/auth_callback"}, "claude"},
		{"ChatGPT Connector", nil, "chatgpt"},
		{"", []string{"https://chatgpt.com/connector_platform_oauth_redirect"}, "chatgpt"},
		{"Grok", []string{"https://grok.com/callback"}, "grok"},
		{"", []string{"https://accounts.x.ai/callback"}, "grok"},
		{"Some IDE", []string{"https://ide.example/cb"}, "unknown"},
	}
	for _, tc := range cases {
		if got := inferProvider(tc.name, tc.uris); got != tc.want {
			t.Errorf("inferProvider(%q, %v) = %q, want %q", tc.name, tc.uris, got, tc.want)
		}
	}
}

func TestValidateBearerProvider(t *testing.T) {
	s, ts := newTestServer(t)
	_, pairingCode := registerDevice(t, ts)
	clientID, redirectURI := registerClient(t, ts) // client_name "Claude"
	verifier, challenge := pkcePair()
	code := authorize(t, ts, clientID, redirectURI, challenge, pairingCode)

	resp, body := postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(body, &tok)

	req, _ := http.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	deviceID, provider, err := s.ValidateBearerProvider(req)
	if err != nil || !strings.HasPrefix(deviceID, "dev_") || provider != "claude" {
		t.Fatalf("ValidateBearerProvider = %q, %q, %v", deviceID, provider, err)
	}
}

func TestCredentialsStoredHashed(t *testing.T) {
	s, ts := newTestServer(t)
	_, pairingCode := registerDevice(t, ts)
	clientID, redirectURI := registerClient(t, ts)
	verifier, challenge := pkcePair()
	code := authorize(t, ts, clientID, redirectURI, challenge, pairingCode)

	m := s.Store.(*MemoryStore)
	m.mu.Lock()
	_, rawStored := m.codes[code]
	_, hashStored := m.codes[hashSecret(code)]
	m.mu.Unlock()
	if rawStored || !hashStored {
		t.Fatalf("auth code storage: raw=%v hashed=%v", rawStored, hashStored)
	}

	resp, body := postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(body, &tok)

	// A dump of the metadata store must not contain a usable credential.
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.access[tok.AccessToken]; ok {
		t.Fatal("raw access token stored")
	}
	if _, ok := m.access[hashSecret(tok.AccessToken)]; !ok {
		t.Fatal("hashed access token missing")
	}
	if _, ok := m.refresh[tok.RefreshToken]; ok {
		t.Fatal("raw refresh token stored")
	}
	if _, ok := m.refresh[hashSecret(tok.RefreshToken)]; !ok {
		t.Fatal("hashed refresh token missing")
	}
}

func TestFullOAuthFlow(t *testing.T) {
	s, ts := newTestServer(t)
	_, pairingCode := registerDevice(t, ts)
	clientID, redirectURI := registerClient(t, ts)
	verifier, challenge := pkcePair()

	code := authorize(t, ts, clientID, redirectURI, challenge, pairingCode)

	resp, body := postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	json.Unmarshal(body, &tok)
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.TokenType != "Bearer" || tok.ExpiresIn <= 0 {
		t.Fatalf("token response = %s", body)
	}

	// The access token validates and is bound to the paired device.
	req, _ := http.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	deviceID, err := s.ValidateBearer(req)
	if err != nil || !strings.HasPrefix(deviceID, "dev_") {
		t.Fatalf("ValidateBearer = %q, %v", deviceID, err)
	}

	// The authorization code is single-use.
	resp, _ = postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {clientID}, "code_verifier": {verifier},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("code replay = %d", resp.StatusCode)
	}

	// Refresh rotation: new pair, old refresh token single-use.
	resp, body = postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken},
		"client_id": {clientID},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d %s", resp.StatusCode, body)
	}
	var tok2 struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(body, &tok2)
	if tok2.RefreshToken == tok.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// Reusing the consumed refresh token revokes the whole family.
	resp, _ = postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken},
		"client_id": {clientID},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("refresh reuse = %d", resp.StatusCode)
	}
	resp, _ = postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok2.RefreshToken},
		"client_id": {clientID},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("family member survived reuse revocation")
	}
}

func TestPKCEEnforced(t *testing.T) {
	_, ts := newTestServer(t)
	_, pairingCode := registerDevice(t, ts)
	clientID, redirectURI := registerClient(t, ts)
	_, challenge := pkcePair()

	code := authorize(t, ts, clientID, redirectURI, challenge, pairingCode)

	// Wrong verifier fails; the code is consumed either way.
	resp, body := postForm(t, http.DefaultClient, ts.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {clientID}, "code_verifier": {"wrong-verifier-wrong-verifier-wrong-1234"},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "PKCE") {
		t.Fatalf("wrong verifier = %d %s", resp.StatusCode, body)
	}

	// Authorize without a challenge redirects back with an error.
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"},
	}
	req, _ := http.NewRequest("GET", ts.URL+"/authorize?"+q.Encode(), nil)
	resp2, err := noRedirect().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound || !strings.Contains(resp2.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("missing challenge = %d %s", resp2.StatusCode, resp2.Header.Get("Location"))
	}
}

func TestAuthorizeRejectsBadClients(t *testing.T) {
	_, ts := newTestServer(t)
	clientID, _ := registerClient(t, ts)

	// Unknown client.
	resp, _ := http.Get(ts.URL + "/authorize?client_id=cl_bogus&redirect_uri=https://x.example/cb&response_type=code")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown client = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Unregistered redirect_uri must not be redirected to.
	resp, err := noRedirect().Get(ts.URL + "/authorize?client_id=" + clientID +
		"&redirect_uri=" + url.QueryEscape("https://evil.example/cb") + "&response_type=code")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unregistered redirect = %d", resp.StatusCode)
	}

	// DCR rejects non-https redirect URIs.
	resp2, _ := postJSON(t, ts.URL+"/register", map[string]any{
		"redirect_uris": []string{"http://insecure.example/cb"},
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("http redirect accepted = %d", resp2.StatusCode)
	}
}

func TestWrongPairingCodeReprompts(t *testing.T) {
	_, ts := newTestServer(t)
	clientID, redirectURI := registerClient(t, ts)
	_, challenge := pkcePair()

	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"},
	}
	resp, err := http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := requestIDRe.FindSubmatch(page)

	resp2, body := postForm(t, noRedirect(), ts.URL+"/authorize", url.Values{
		"request_id": {string(m[1])}, "pairing_code": {"NOPE-CODE"},
	}, nil)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(body), "not valid") {
		t.Fatalf("wrong pairing code = %d %s", resp2.StatusCode, body)
	}
	// The form is re-armed with a fresh request id.
	if requestIDRe.FindSubmatch(body) == nil {
		t.Fatal("re-prompt lost the request id")
	}
}

// fetchPairingPage returns the rendered /authorize page.
func fetchPairingPage(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	clientID, redirectURI := registerClient(t, ts)
	_, challenge := pkcePair()
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"},
	}
	resp, err := http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	return string(page)
}

func TestPairingPageClaimsAtTheConfiguredCompanion(t *testing.T) {
	// A Companion moved off the default port used to lose push pairing with
	// no explanation, because the address was written into the page.
	s, ts := newTestServer(t)
	if page := fetchPairingPage(t, ts); !strings.Contains(page, DefaultCompanionOrigin+"/pair/claim") {
		t.Fatalf("default claim URL missing from the page:\n%s", page)
	}
	s.CompanionOrigin = "http://127.0.0.1:9911"
	page := fetchPairingPage(t, ts)
	if !strings.Contains(page, "http://127.0.0.1:9911/pair/claim") {
		t.Fatalf("configured claim URL missing from the page:\n%s", page)
	}
	if strings.Contains(page, DefaultCompanionOrigin) {
		t.Fatal("the page still carries the hardcoded default alongside the configured origin")
	}
}

func TestPairingPageShowsTheCodeFormWithoutScript(t *testing.T) {
	// The two paths are shown one at a time, and the script is what hides the
	// form — so a browser running none still has a way through, and a browser
	// running it does not offer both at once.
	_, ts := newTestServer(t)
	page := fetchPairingPage(t, ts)
	if !strings.Contains(page, `<form id="manual"`) {
		t.Fatal("the page has no pairing-code form")
	}
	if strings.Contains(page, `<form id="manual" hidden`) {
		t.Fatal("the form is hidden in the markup, so a no-script browser cannot pair at all")
	}
	if !strings.Contains(page, `manual.hidden = true`) {
		t.Fatal("the script does not put the page on one path at a time")
	}
	// Cancelling the claim on the way out is what stops the desktop prompt
	// outliving the request it was raised for.
	if !strings.Contains(page, "cancel: true") || !strings.Contains(page, "keepalive: true") {
		t.Fatalf("the page does not withdraw its claim when the code wins:\n%s", page)
	}
}

func TestPairingPageCarriesTheNonceIntoTheContinueForm(t *testing.T) {
	// The nonce is what proves the approval happened on this machine, and
	// /authorize/continue refuses the request without it. A page that submits
	// the form without filling the field sends the user, who has just
	// approved on the desktop, to "invalid continuation" — the approval
	// worked and the browser said it did not.
	_, ts := newTestServer(t)
	page := fetchPairingPage(t, ts)

	nonceAt := strings.Index(page, `document.getElementById("nonce").value`)
	if nonceAt < 0 {
		t.Fatal("the page never writes the approval nonce into the continue form")
	}
	submitAt := strings.Index(page, `document.getElementById("cont").submit()`)
	if submitAt < 0 {
		t.Fatal("the page never submits the continue form")
	}
	if nonceAt > submitAt {
		t.Fatal("the form is submitted before the nonce is written into it")
	}
	if !strings.Contains(page, `<input type="hidden" name="nonce" id="nonce"`) {
		t.Fatal("the continue form has no nonce field to fill")
	}
}

func TestPairingPageCountsDownTheServersOwnDeadline(t *testing.T) {
	// The page used to compute its own ten-minute deadline from load time. The
	// store is what actually expires the request, so a page counting to a
	// different zero either refuses a code that still works or keeps offering
	// one that no longer does.
	_, ts := newTestServer(t)
	page := fetchPairingPage(t, ts)

	// html/template pads a value injected into script context with spaces.
	m := regexp.MustCompile(`var SECS =\s*(\d+)\s*;`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the page does not carry the request's remaining seconds")
	}
	secs, _ := strconv.Atoi(m[1])
	// A freshly issued request has all but a moment of its TTL left. The
	// window is what makes this a countdown rather than a constant: a page
	// with its own idea of the deadline drifts out of it.
	if full := int(authRequestTTL.Seconds()); secs > full || secs < full-5 {
		t.Fatalf("SECS = %d, want the request's remaining time (near %d)", secs, full)
	}
	if strings.Contains(page, "10 * 60 * 1000") {
		t.Fatal("the page still computes a deadline of its own")
	}
	if !strings.Contains(page, `id="cttl"`) || !strings.Contains(page, `id="nttl"`) {
		t.Fatal("one of the two layouts has nowhere to show the remaining time")
	}
}

func TestExpiredPairingPageStopsOfferingToSubmit(t *testing.T) {
	// Past the deadline the store refuses the request outright. A form that
	// still looked usable would spend the user's typing on an error they
	// could have been shown a minute earlier.
	_, ts := newTestServer(t)
	page := fetchPairingPage(t, ts)

	for _, want := range []string{"go.disabled = true", "input.disabled = true", "cancelClaim()"} {
		if !strings.Contains(page, want) {
			t.Errorf("the expiry path does not do %q", want)
		}
	}
	if !strings.Contains(page, "xExpired") {
		t.Error("nothing tells the user why the form stopped working")
	}
}

func TestDeviceAuthAndPairingCodes(t *testing.T) {
	s, ts := newTestServer(t)
	creds, code := registerDevice(t, ts)

	if !regexp.MustCompile(`^[2-9A-HJ-NP-Z]{4}-[2-9A-HJ-NP-Z]{4}$`).MatchString(code) {
		t.Fatalf("pairing code format = %q", code)
	}

	// Device credentials authenticate; garbage does not.
	req, _ := http.NewRequest("GET", "/tunnel", nil)
	req.Header.Set("Authorization", "Bearer "+creds)
	if _, err := s.AuthenticateDevice(req); err != nil {
		t.Fatalf("valid device creds rejected: %v", err)
	}
	req.Header.Set("Authorization", "Bearer dev_x:wrong")
	if _, err := s.AuthenticateDevice(req); err == nil {
		t.Fatal("bogus device creds accepted")
	}

	// Pairing codes are single-use.
	if _, err := s.Store.TakePairingCode(code, time.Now()); err != nil {
		t.Fatal("pairing code not stored")
	}
	if _, err := s.Store.TakePairingCode(code, time.Now()); err == nil {
		t.Fatal("pairing code reusable")
	}

	// Pairing without device credentials is refused.
	resp, _ := postForm(t, http.DefaultClient, ts.URL+"/v1/pair", url.Values{}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pair = %d", resp.StatusCode)
	}
}

func TestDiscoveryDocuments(t *testing.T) {
	s, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if meta["issuer"] != s.Issuer || meta["token_endpoint"] != s.Issuer+"/token" {
		t.Fatalf("AS metadata = %v", meta)
	}

	resp, err = http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	var rm map[string]any
	json.NewDecoder(resp.Body).Decode(&rm)
	resp.Body.Close()
	if rm["resource"] != s.Issuer+"/mcp" {
		t.Fatalf("resource metadata = %v", rm)
	}
	if !strings.Contains(s.WWWAuthenticate(), "resource_metadata=") {
		t.Fatalf("WWW-Authenticate = %s", s.WWWAuthenticate())
	}
}

func TestExpiredArtifacts(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Now()

	s.Store.PutPairingCode(&PairingCode{Code: "OLD1-OLD1", DeviceID: "dev_x", ExpiresAt: now.Add(-time.Second)})
	if _, err := s.Store.TakePairingCode("OLD1-OLD1", now); err == nil {
		t.Fatal("expired pairing code accepted")
	}
	s.Store.PutAuthCode(&AuthCode{Code: "ac_old", ExpiresAt: now.Add(-time.Second)})
	if _, err := s.Store.TakeAuthCode("ac_old", now); err == nil {
		t.Fatal("expired auth code accepted")
	}
	s.Store.PutAccessToken(&AccessToken{Token: "at_old", DeviceID: "dev_x", ExpiresAt: now.Add(-time.Second)})
	req, _ := http.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer at_old")
	if _, err := s.ValidateBearer(req); err == nil {
		t.Fatal("expired access token accepted")
	}
}
