// Package devicecred manages the Companion's relay device credentials:
// registration against the relay, OS-keychain storage (secrets never touch
// config files or the database), and pairing-code retrieval.
package devicecred

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// keyringService namespaces Fylane entries in the OS keychain.
const keyringService = "fylane-companion"

// Credentials identify this device to one relay.
type Credentials struct {
	DeviceID     string `json:"device_id"`
	DeviceSecret string `json:"device_secret"`
}

// TunnelToken renders the credentials in the tunnel's bearer format.
func (c Credentials) TunnelToken() string {
	return c.DeviceID + ":" + c.DeviceSecret
}

// relayKey normalizes a relay URL (any scheme) to its keychain entry name.
func relayKey(relayURL string) (string, error) {
	u, err := url.Parse(relayURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid relay URL %q", relayURL)
	}
	return "relay:" + u.Host, nil
}

// Save stores credentials for relayURL in the OS keychain.
func Save(relayURL string, creds Credentials) error {
	key, err := relayKey(relayURL)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, key, string(raw)); err != nil {
		return fmt.Errorf("storing device credentials in the OS keychain: %w", err)
	}
	return nil
}

// Load reads credentials for relayURL from the OS keychain.
func Load(relayURL string) (Credentials, error) {
	var creds Credentials
	key, err := relayKey(relayURL)
	if err != nil {
		return creds, err
	}
	raw, err := keyring.Get(keyringService, key)
	if err == keyring.ErrNotFound {
		return creds, fmt.Errorf("no device credentials for this relay; run `fylane-companion pair -relay %s` first", relayURL)
	}
	if err != nil {
		return creds, fmt.Errorf("reading device credentials from the OS keychain: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return creds, fmt.Errorf("stored device credentials are invalid; re-run pair")
	}
	return creds, nil
}

// APIBase converts a ws://, wss://, http://, or https:// relay URL to its
// HTTP API base with any path (e.g. /tunnel) stripped.
func APIBase(relayURL string) (string, error) {
	u, err := url.Parse(relayURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid relay URL %q", relayURL)
	}
	scheme := u.Scheme
	switch scheme {
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported relay URL scheme %q", u.Scheme)
	}
	return scheme + "://" + u.Host, nil
}

// Register creates device credentials on the relay and stores them in the
// OS keychain. Existing credentials for the relay are replaced.
func Register(ctx context.Context, relayURL, name string) (Credentials, error) {
	var creds Credentials
	base, err := APIBase(relayURL)
	if err != nil {
		return creds, err
	}
	body, _ := json.Marshal(map[string]string{"name": name})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/devices", bytes.NewReader(body))
	if err != nil {
		return creds, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return creds, fmt.Errorf("registering device with relay: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return creds, fmt.Errorf("relay refused device registration (%d)", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return creds, err
	}
	if creds.DeviceID == "" || creds.DeviceSecret == "" {
		return creds, fmt.Errorf("relay returned incomplete device credentials")
	}
	if err := Save(relayURL, creds); err != nil {
		return creds, err
	}
	return creds, nil
}

// PairingCode asks the relay for a fresh pairing code using the stored
// device credentials.
func PairingCode(ctx context.Context, relayURL string) (code string, expiresIn time.Duration, err error) {
	creds, err := Load(relayURL)
	if err != nil {
		return "", 0, err
	}
	base, err := APIBase(relayURL)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/pair", strings.NewReader("{}"))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.TunnelToken())
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("requesting pairing code: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", 0, fmt.Errorf("relay refused the pairing request (%d); re-run pair to refresh credentials", resp.StatusCode)
	}
	var out struct {
		PairingCode string `json:"pairing_code"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.PairingCode, time.Duration(out.ExpiresIn) * time.Second, nil
}

// PairRequestInfo fetches the authoritative client name and verify code of
// a pending push-pairing request. Device credentials required.
func PairRequestInfo(ctx context.Context, relayURL, requestID string) (clientName, verify string, err error) {
	creds, err := Load(relayURL)
	if err != nil {
		return "", "", err
	}
	base, err := APIBase(relayURL)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		base+"/v1/pair/request?request_id="+url.QueryEscape(requestID), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+creds.TunnelToken())
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetching pairing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("relay refused the pairing lookup (%d)", resp.StatusCode)
	}
	var out struct {
		ClientName string `json:"client_name"`
		VerifyCode string `json:"verify_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return out.ClientName, out.VerifyCode, nil
}

// PairApprove approves a push-pairing request with this device's credentials
// and returns the single-use continuation nonce for the browser page.
func PairApprove(ctx context.Context, relayURL, requestID string) (string, error) {
	creds, err := Load(relayURL)
	if err != nil {
		return "", err
	}
	base, err := APIBase(relayURL)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"request_id": requestID})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/pair/approve", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+creds.TunnelToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("approving pairing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("relay refused the pairing approval (%d)", resp.StatusCode)
	}
	var out struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Nonce, nil
}
