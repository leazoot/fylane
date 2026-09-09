package devicecred

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeRelay implements the relay pairing contract (the real implementation
// lives in shared/authsrv, whose integration tests cover the same
// endpoints; internal-package boundaries keep it unimportable here).
func fakeRelay(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"device_id": "dev_test123", "device_secret": "s3cret",
		})
	})
	mux.HandleFunc("POST /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dev_test123:s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"pairing_code": "K3ZM-7PWQ", "expires_in": 600})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestRegisterAndPair(t *testing.T) {
	keyring.MockInit()
	ts := fakeRelay(t)
	ctx := context.Background()

	creds, err := Register(ctx, ts.URL, "test-device")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if creds.DeviceID != "dev_test123" || creds.DeviceSecret != "s3cret" {
		t.Fatalf("creds = %+v", creds)
	}

	// Credentials round-trip through the (mock) keychain.
	loaded, err := Load(ts.URL)
	if err != nil || loaded != creds {
		t.Fatalf("Load = %+v, %v", loaded, err)
	}
	if loaded.TunnelToken() != "dev_test123:s3cret" {
		t.Fatalf("TunnelToken = %q", loaded.TunnelToken())
	}

	code, expiresIn, err := PairingCode(ctx, ts.URL)
	if err != nil || code != "K3ZM-7PWQ" || expiresIn <= 0 {
		t.Fatalf("PairingCode = %q, %v, %v", code, expiresIn, err)
	}
}

func TestLoadWithoutCredentials(t *testing.T) {
	keyring.MockInit()
	if _, err := Load("https://never-paired.example"); err == nil || !strings.Contains(err.Error(), "pair") {
		t.Fatalf("Load without creds: err = %v", err)
	}
}

func TestPairingWithWrongStoredCredentials(t *testing.T) {
	keyring.MockInit()
	ts := fakeRelay(t)
	if err := Save(ts.URL, Credentials{DeviceID: "dev_other", DeviceSecret: "nope"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PairingCode(context.Background(), ts.URL); err == nil {
		t.Fatal("pairing succeeded with wrong credentials")
	}
}

func TestURLNormalization(t *testing.T) {
	base, err := APIBase("wss://relay.example/tunnel")
	if err != nil || base != "https://relay.example" {
		t.Fatalf("apiBase = %q, %v", base, err)
	}
	if _, err := APIBase("ftp://x"); err == nil {
		t.Fatal("ftp scheme accepted")
	}

	// ws and https forms of the same host share one keychain entry.
	k1, _ := relayKey("wss://relay.example/tunnel")
	k2, _ := relayKey("https://relay.example")
	if k1 != k2 {
		t.Fatalf("relay keys differ: %q vs %q", k1, k2)
	}
}
