package authsrv

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func openTestSQLite(t *testing.T) *SQLiteStore {
	t.Helper()
	return openSQLiteAt(t, filepath.Join(t.TempDir(), "auth.db"))
}

func openSQLiteAt(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	s, err := OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLiteMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	s := openSQLiteAt(t, path)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	openSQLiteAt(t, path) // reopening must not re-apply migration 1
}

// The whole flow a platform connector runs, over the file store: device
// registration, pairing code, DCR, authorize, PKCE token exchange, refresh
// rotation, and reuse detection.
func TestFullOAuthFlowOverSQLite(t *testing.T) {
	auth := &Server{Store: openTestSQLite(t)}
	mux := http.NewServeMux()
	auth.Routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	auth.Issuer = ts.URL

	resp, err := http.Post(ts.URL+"/v1/devices", "application/json", strings.NewReader(`{"name":"sqlite-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	var dev struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	json.NewDecoder(resp.Body).Decode(&dev)
	resp.Body.Close()
	if dev.DeviceID == "" || dev.DeviceSecret == "" {
		t.Fatal("device registration returned no credentials")
	}

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

	redirectURI := "https://platform.example/cb"
	resp, err = http.Post(ts.URL+"/register", "application/json",
		strings.NewReader(`{"redirect_uris":["`+redirectURI+`"],"client_name":"SQLite Test"}`))
	if err != nil {
		t.Fatal(err)
	}
	var cl struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(resp.Body).Decode(&cl)
	resp.Body.Close()

	verifier := "sqlite-verifier-sqlite-verifier-123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"client_id": {cl.ClientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"sqlitestate"},
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
	if loc.Query().Get("state") != "sqlitestate" {
		t.Errorf("state = %q", loc.Query().Get("state"))
	}

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

	req, _ = http.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	deviceID, provider, err := auth.ValidateBearerProvider(req)
	if err != nil || deviceID != dev.DeviceID {
		t.Fatalf("ValidateBearerProvider = %q, %v; want %q", deviceID, err, dev.DeviceID)
	}
	if provider != "SQLite Test" {
		t.Errorf("provider = %q; want the registered name for an unrecognized client", provider)
	}

	// An authorization code is single use even though it now lives in a file.
	resp, _ = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("auth code replay = %d; want 400", resp.StatusCode)
	}

	// Refresh rotation and reuse detection survive over SQL.
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
		t.Fatalf("refresh reuse = %d; want 400", resp.StatusCode)
	}
	form = url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok2.RefreshToken}, "client_id": {cl.ClientID}}
	resp, _ = http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("family member survived reuse revocation")
	}
}

func TestSQLiteSingleUseAndExpiry(t *testing.T) {
	s := openTestSQLite(t)
	now := time.Now()

	dev := &Device{ID: "dev_1", SecretHash: "h", Name: "n", CreatedAt: now}
	if err := s.CreateDevice(dev); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDevice(dev.ID)
	if err != nil || got.SecretHash != "h" || !got.CreatedAt.Equal(now.UTC().Truncate(time.Nanosecond)) {
		t.Fatalf("GetDevice = %+v, %v", got, err)
	}
	if _, err := s.GetDevice("dev_missing"); err != ErrNotFound {
		t.Fatalf("GetDevice(missing) = %v; want ErrNotFound", err)
	}

	code := &PairingCode{Code: "AAAA-BBBB", DeviceID: dev.ID, ExpiresAt: now.Add(time.Minute)}
	if err := s.PutPairingCode(code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakePairingCode(code.Code, now); err != nil {
		t.Fatalf("first take: %v", err)
	}
	if _, err := s.TakePairingCode(code.Code, now); err == nil {
		t.Fatal("pairing code reusable")
	}

	// Expired records are rejected even though the row still exists.
	old := &PairingCode{Code: "OLD1-OLD2", DeviceID: dev.ID, ExpiresAt: now.Add(-time.Minute)}
	if err := s.PutPairingCode(old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakePairingCode(old.Code, now); err == nil {
		t.Fatal("expired pairing code accepted")
	}

	token := &AccessToken{Token: "at_hash", DeviceID: dev.ID, ClientID: "cl_1", ExpiresAt: now.Add(-time.Second)}
	if err := s.CreateClient(&Client{ID: "cl_1", Name: "c", RedirectURIs: []string{"https://p.example/cb"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAccessToken(token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAccessToken(token.Token, now); err != ErrNotFound {
		t.Fatalf("expired access token = %v; want ErrNotFound", err)
	}
}

func TestSQLiteClientRoundTrip(t *testing.T) {
	s := openTestSQLite(t)
	now := time.Now()
	in := &Client{ID: "cl_2", Name: "Claude", RedirectURIs: []string{"https://claude.ai/cb", "https://claude.ai/cb2"}, CreatedAt: now}
	if err := s.CreateClient(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.GetClient(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.RedirectURIs) != 2 || out.RedirectURIs[1] != "https://claude.ai/cb2" || out.Name != "Claude" {
		t.Fatalf("GetClient = %+v", out)
	}
}

// Push pairing over the file store: bind is single-shot, and the bound device
// and nonce survive into the consuming read.
func TestSQLitePushPairingBind(t *testing.T) {
	s := openTestSQLite(t)
	now := time.Now()
	cl := &Client{ID: "cl_push", Name: "push-test", RedirectURIs: []string{"https://p.example/cb"}, CreatedAt: now}
	if err := s.CreateClient(cl); err != nil {
		t.Fatal(err)
	}
	req := &AuthRequest{
		ID: "ar_push", ClientID: cl.ID, RedirectURI: "https://p.example/cb",
		CodeChallenge: "ch", VerifyCode: "K7-P2", ExpiresAt: now.Add(time.Minute),
	}
	if err := s.PutAuthRequest(req); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAuthRequest(req.ID, now)
	if err != nil || got.VerifyCode != "K7-P2" || got.DeviceID != "" {
		t.Fatalf("GetAuthRequest = %+v, %v", got, err)
	}
	if err := s.BindAuthRequest(req.ID, "dev_push", "noncehash", now); err != nil {
		t.Fatalf("BindAuthRequest: %v", err)
	}
	if err := s.BindAuthRequest(req.ID, "dev_other", "otherhash", now); err == nil {
		t.Fatal("second bind accepted")
	}
	got, err = s.TakeAuthRequest(req.ID, now)
	if err != nil || got.DeviceID != "dev_push" || got.NonceHash != "noncehash" {
		t.Fatalf("TakeAuthRequest = %+v, %v", got, err)
	}
	if _, err := s.GetAuthRequest(req.ID, now); err == nil {
		t.Fatal("consumed request still readable")
	}
	// An expired request cannot be bound.
	stale := &AuthRequest{ID: "ar_stale", ClientID: cl.ID, RedirectURI: "https://p.example/cb",
		CodeChallenge: "ch", ExpiresAt: now.Add(-time.Minute)}
	if err := s.PutAuthRequest(stale); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAuthRequest(stale.ID, "dev_push", "noncehash", now); err == nil {
		t.Fatal("expired request bound")
	}
}

func TestSQLitePurgeExpired(t *testing.T) {
	s := openTestSQLite(t)
	now := time.Now()
	if err := s.CreateDevice(&Device{ID: "dev_p", SecretHash: "h", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateClient(&Client{ID: "cl_p", RedirectURIs: []string{"https://p.example/cb"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPairingCode(&PairingCode{Code: "GONE-GONE", DeviceID: "dev_p", ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	live := &PairingCode{Code: "LIVE-LIVE", DeviceID: "dev_p", ExpiresAt: now.Add(time.Hour)}
	if err := s.PutPairingCode(live); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAccessToken(&AccessToken{Token: "at_old", DeviceID: "dev_p", ClientID: "cl_p", ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeExpired(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("PurgeExpired = %d; want 2", n)
	}
	if _, err := s.TakePairingCode(live.Code, now); err != nil {
		t.Fatalf("purge removed a live pairing code: %v", err)
	}
}

// The file holds OAuth metadata only. A column whose name
// suggests file content or paths is a design error, not a naming quibble.
func TestSQLiteSchemaHasNoForbiddenColumns(t *testing.T) {
	s := openTestSQLite(t)
	rows, err := s.db.Query(`SELECT m.name, p.name FROM sqlite_master m
		JOIN pragma_table_info(m.name) p WHERE m.type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	forbidden := []string{"path", "body", "content", "diff", "listing", "file", "chat", "payload"}
	seen := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		seen++
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(column), bad) {
				t.Errorf("column %s.%s matches forbidden pattern %q", table, column, bad)
			}
		}
	}
	if seen == 0 {
		t.Fatal("schema introspection returned no columns")
	}
}
