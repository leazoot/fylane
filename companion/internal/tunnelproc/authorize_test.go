package tunnelproc

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// writeCert lays down a cert.pem shaped like the one `cloudflared tunnel
// login` writes: one base64 JSON block between the ARGO TUNNEL TOKEN markers.
func writeCert(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cert.pem")
	pem := "-----BEGIN ARGO TUNNEL TOKEN-----\n" +
		base64.StdEncoding.EncodeToString([]byte(body)) + "\n" +
		"-----END ARGO TUNNEL TOKEN-----\n"
	if err := os.WriteFile(path, []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheSignInAlreadySaysWhichZone(t *testing.T) {
	// The whole reason the hostname does not have to be typed: the zone the
	// user picked in the browser is recorded in the certificate.
	path := writeCert(t, `{"zoneID":"abc123","accountID":"acct","apiToken":"tok"}`)
	creds, err := readCertCredentials(path)
	if err != nil {
		t.Fatalf("readCertCredentials: %v", err)
	}
	if creds.ZoneID != "abc123" || creds.APIToken != "tok" {
		t.Errorf("creds = %+v", creds)
	}
}

func TestAnAccountWideSignInNamesNoZone(t *testing.T) {
	// Choosing "all zones" leaves no single domain to suggest. That has to be
	// an error rather than an empty string treated as a hostname, or the
	// field would fill itself with "fylane." and nothing after it.
	path := writeCert(t, `{"accountID":"acct","apiToken":"tok"}`)
	if _, err := readCertCredentials(path); err == nil {
		t.Error("an account-wide certificate was read as naming a zone")
	}
}

func TestARuinedCertificateIsAnErrorNotAPanic(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCertCredentials(empty); err == nil {
		t.Error("a file with no token block was read as a certificate")
	}
	garbled := filepath.Join(dir, "garbled.pem")
	body := "-----BEGIN ARGO TUNNEL TOKEN-----\n!!!not base64!!!\n-----END ARGO TUNNEL TOKEN-----\n"
	if err := os.WriteFile(garbled, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCertCredentials(garbled); err == nil {
		t.Error("an undecodable token block was accepted")
	}
	if _, err := readCertCredentials(filepath.Join(dir, "absent.pem")); err == nil {
		t.Error("a missing certificate was accepted")
	}
}

func TestZoneLookupSurvivesBeingAskedFromEverywhereAtOnce(t *testing.T) {
	// Regression: the cache used to reset itself by assigning a whole fresh
	// struct, which zeroed the embedded mutex the assigning goroutine was
	// holding — "unlock of unlocked mutex", a hard crash of the Core. The
	// connect screen polls this, so concurrent callers are the normal case.
	t.Setenv("TUNNEL_ORIGIN_CERT", writeCert(t, `{"zoneID":"z","apiToken":"t"}`))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The answer is empty here (no network, and the lookup is
			// asynchronous); what is being asserted is that asking does not
			// bring the process down.
			_ = SuggestedHostname()
		}()
	}
	wg.Wait()
}

func TestSuggestingNothingWhenThereIsNoCertificate(t *testing.T) {
	t.Setenv("TUNNEL_ORIGIN_CERT", filepath.Join(t.TempDir(), "absent.pem"))
	if got := SuggestedHostname(); got != "" {
		t.Errorf("SuggestedHostname = %q; want nothing offered with no sign-in", got)
	}
}
