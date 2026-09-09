package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/shared/authsrv"
)

// testDSN returns the test database DSN. Tests are skipped when PostgreSQL
// is unreachable (docker run postgres:16-alpine on 127.0.0.1:15432 locally).
func testDSN() string {
	if dsn := os.Getenv("FYLANE_TEST_PG"); dsn != "" {
		return dsn
	}
	return "postgres://postgres:fylanetest@127.0.0.1:15432/fylane_relay?sslmode=disable"
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Open(ctx, testDSN())
	if err != nil {
		t.Skipf("PostgreSQL not available (%v); start it to run relay store tests", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsIdempotent(t *testing.T) {
	s1 := openTestStore(t)
	s1.Close()
	openTestStore(t) // reopening must not fail re-applying migration 1
}

func TestFullOAuthFlowOverPostgres(t *testing.T) {
	pg := openTestStore(t)
	auth := &authsrv.Server{Store: pg}
	mux := http.NewServeMux()
	auth.Routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	auth.Issuer = ts.URL

	// Device + pairing code.
	resp, err := http.Post(ts.URL+"/v1/devices", "application/json", strings.NewReader(`{"name":"pg-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	var dev struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	json.NewDecoder(resp.Body).Decode(&dev)
	resp.Body.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/pair", nil)
	req.Header.Set("Authorization", "Bearer "+dev.DeviceID+":"+dev.DeviceSecret)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var pair struct {
		PairingCode string `json:"pairing_code"`
	}
	json.NewDecoder(resp.Body).Decode(&pair)
	resp.Body.Close()
	if pair.PairingCode == "" {
		t.Fatal("no pairing code")
	}

	// Client registration.
	redirectURI := "https://platform.example/cb"
	resp, err = http.Post(ts.URL+"/register", "application/json",
		strings.NewReader(`{"redirect_uris":["`+redirectURI+`"],"client_name":"PG Test"}`))
	if err != nil {
		t.Fatal(err)
	}
	var cl struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(resp.Body).Decode(&cl)
	resp.Body.Close()

	// Authorize with pairing code.
	verifier := "pg-verifier-pg-verifier-pg-verifier-123456"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"client_id": {cl.ClientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"pgstate"},
	}
	resp, err = http.Get(ts.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindSubmatch(page)
	if m == nil {
		t.Fatalf("no request id: %s", page)
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{"request_id": {string(m[1])}, "pairing_code": {pair.PairingCode}}
	resp, err = noRedirect.Post(ts.URL+"/authorize", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no auth code: %s", resp.Header.Get("Location"))
	}

	// Token exchange with PKCE.
	form = url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {cl.ClientID}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier},
	}
	resp, err = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(body, &tok)

	// Bearer validates and binds to the device.
	req, _ = http.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	deviceID, err := auth.ValidateBearer(req)
	if err != nil || deviceID != dev.DeviceID {
		t.Fatalf("ValidateBearer = %q, %v; want %q", deviceID, err, dev.DeviceID)
	}

	// Refresh rotation + reuse detection survive over SQL.
	form = url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}, "client_id": {cl.ClientID}}
	resp, _ = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d %s", resp.StatusCode, body)
	}
	var tok2 struct {
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(body, &tok2)

	resp, _ = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("refresh reuse = %d", resp.StatusCode)
	}
	form = url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok2.RefreshToken}, "client_id": {cl.ClientID}}
	resp, _ = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("family member survived reuse revocation")
	}
}

func TestSingleUseAtomicity(t *testing.T) {
	pg := openTestStore(t)
	now := time.Now()

	dev := &authsrv.Device{ID: "dev_" + randomSuffix(t), SecretHash: "h", CreatedAt: now}
	if err := pg.CreateDevice(dev); err != nil {
		t.Fatal(err)
	}
	code := &authsrv.PairingCode{Code: "PG" + randomSuffix(t), DeviceID: dev.ID, ExpiresAt: now.Add(time.Minute)}
	if err := pg.PutPairingCode(code); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.TakePairingCode(code.Code, now); err != nil {
		t.Fatal("first take failed")
	}
	if _, err := pg.TakePairingCode(code.Code, now); err == nil {
		t.Fatal("pairing code reusable")
	}

	// Expired artifacts are rejected even though the row exists.
	old := &authsrv.PairingCode{Code: "OLD" + randomSuffix(t), DeviceID: dev.ID, ExpiresAt: now.Add(-time.Minute)}
	pg.PutPairingCode(old)
	if _, err := pg.TakePairingCode(old.Code, now); err == nil {
		t.Fatal("expired pairing code accepted")
	}
}

// Push pairing over SQL: bind is atomic and single-shot, and the bound
// device/nonce survive into the consuming read.
func TestPushPairingBindOverPostgres(t *testing.T) {
	pg := openTestStore(t)
	now := time.Now()
	cl := &authsrv.Client{ID: "cl_" + randomSuffix(t), Name: "push-test",
		RedirectURIs: []string{"https://p.example/cb"}, CreatedAt: now}
	if err := pg.CreateClient(cl); err != nil {
		t.Fatal(err)
	}
	req := &authsrv.AuthRequest{
		ID: "ar_" + randomSuffix(t), ClientID: cl.ID, RedirectURI: "https://p.example/cb",
		CodeChallenge: "ch", VerifyCode: "K7-P2", ExpiresAt: now.Add(time.Minute),
	}
	if err := pg.PutAuthRequest(req); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetAuthRequest(req.ID, now)
	if err != nil || got.VerifyCode != "K7-P2" || got.DeviceID != "" {
		t.Fatalf("GetAuthRequest = %+v, %v", got, err)
	}
	if err := pg.BindAuthRequest(req.ID, "dev_push", "noncehash", now); err != nil {
		t.Fatalf("BindAuthRequest: %v", err)
	}
	if err := pg.BindAuthRequest(req.ID, "dev_other", "otherhash", now); err == nil {
		t.Fatal("second bind accepted")
	}
	got, err = pg.TakeAuthRequest(req.ID, now)
	if err != nil || got.DeviceID != "dev_push" || got.NonceHash != "noncehash" {
		t.Fatalf("TakeAuthRequest = %+v, %v", got, err)
	}
	if _, err := pg.GetAuthRequest(req.ID, now); err == nil {
		t.Fatal("consumed request still readable")
	}
}

func TestDeviceStatusAndMetrics(t *testing.T) {
	pg := openTestStore(t)
	ctx := context.Background()
	dev := &authsrv.Device{ID: "dev_" + randomSuffix(t), SecretHash: "h", CreatedAt: time.Now()}
	if err := pg.CreateDevice(dev); err != nil {
		t.Fatal(err)
	}

	if err := pg.SetDeviceOnline(ctx, dev.ID, true, "0.0.1-dev"); err != nil {
		t.Fatalf("SetDeviceOnline: %v", err)
	}
	if err := pg.SetDeviceOnline(ctx, "dev_unknown", true, ""); err == nil {
		t.Fatal("unknown device accepted")
	}

	if err := pg.RecordCall(ctx, dev.ID, 120*time.Millisecond, ""); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordCall(ctx, dev.ID, 80*time.Millisecond, "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordCall(ctx, dev.ID, 40*time.Millisecond, "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordCall(ctx, dev.ID, 10*time.Millisecond, "http_503"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := pg.RecordConnect(ctx, dev.ID); err != nil {
			t.Fatal(err)
		}
	}
	var calls, errs, latency int64
	var lastErr string
	err := pg.db.QueryRow(`SELECT call_count, error_count, last_error_code, total_latency_ms
		FROM device_stats WHERE device_id = $1`, dev.ID).Scan(&calls, &errs, &lastErr, &latency)
	if err != nil || calls != 4 || errs != 3 || lastErr != "http_503" || latency != 250 {
		t.Fatalf("stats = %d/%d/%q/%d, %v", calls, errs, lastErr, latency, err)
	}

	// The Beta metric set must be queryable through the public API,
	// including the per-code failure breakdown and connection count.
	all, err := pg.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	var m *DeviceMetrics
	for _, dm := range all {
		if dm.DeviceID == dev.ID {
			m = dm
		}
	}
	if m == nil {
		t.Fatalf("device %s missing from Metrics", dev.ID)
	}
	if m.ConnectCount != 2 || m.CallCount != 4 || m.ErrorCount != 3 ||
		m.TotalLatencyMS != 250 || !m.Online || m.Version != "0.0.1-dev" {
		t.Fatalf("metrics = %+v", m)
	}
	if m.ErrorCodes["timeout"] != 2 || m.ErrorCodes["http_503"] != 1 || len(m.ErrorCodes) != 2 {
		t.Fatalf("error codes = %v", m.ErrorCodes)
	}
}

// The forbidden-column list: no column in the relay schema may be able to hold
// paths, file bodies, diffs, listings, or chat content.
func TestSchemaHasNoForbiddenColumns(t *testing.T) {
	pg := openTestStore(t)
	rows, err := pg.db.Query(`SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	forbidden := []string{"path", "body", "content", "diff", "listing", "file", "chat", "payload"}
	for rows.Next() {
		var table, column string
		rows.Scan(&table, &column)
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(column), bad) {
				t.Errorf("column %s.%s matches forbidden pattern %q", table, column, bad)
			}
		}
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	f, err := os.Open("/dev/urandom")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	io.ReadFull(f, buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}
