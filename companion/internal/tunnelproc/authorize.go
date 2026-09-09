package tunnelproc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Where a provider's credential already lives, and the one-off account work
// that has to happen before a tunnel can run. Both are answered from local
// state or by running the provider's own commands — Fylane never asks the
// user to open a terminal, and never downloads anything.

// CloudflareTunnelName is the tunnel this machine creates on the user's
// account. One per machine, reused: creating a second one on every apply
// would litter their dashboard.
const CloudflareTunnelName = "fylane"

// provisionTimeout bounds each account-side call. These talk to the vendor's
// API, so they can hang; a wizard that hangs with them is not a wizard.
const provisionTimeout = 45 * time.Second

// cloudflaredCertPath is where `cloudflared tunnel login` leaves the
// certificate that authorizes this machine against the user's zone.
func cloudflaredCertPath() string {
	if p := os.Getenv("TUNNEL_ORIGIN_CERT"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cloudflared", "cert.pem")
}

// cloudflareLoggedIn reports whether the browser login has already happened.
func cloudflareLoggedIn() bool {
	path := cloudflaredCertPath()
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// ngrokConfigPaths are the places ngrok keeps the authtoken it was given.
// Checked so a user who configured ngrok themselves is not asked again.
func ngrokConfigPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	paths := []string{filepath.Join(home, ".ngrok2", "ngrok.yml")}
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths, filepath.Join(home, "Library", "Application Support", "ngrok", "ngrok.yml"))
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			paths = append(paths, filepath.Join(dir, "ngrok", "ngrok.yml"))
		}
	default:
		paths = append(paths, filepath.Join(home, ".config", "ngrok", "ngrok.yml"))
	}
	return paths
}

// ngrokConfigured reports whether a token is on this machine, either in our
// keychain entry or in ngrok's own config from a previous manual setup.
func ngrokConfigured() bool {
	if t, err := LoadToken(Ngrok); err == nil && t != "" {
		return true
	}
	for _, p := range ngrokConfigPaths() {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

// The sign-in already says which domain this is for: `cloudflared tunnel
// login` writes the chosen zone's id into cert.pem. Asking the user to type a
// domain Fylane could look up is asking them to repeat themselves.

// zoneAPI is Cloudflare's own API, the only place the zone's name can come
// from. Constant on purpose — nothing here takes a URL from anywhere else.
const zoneAPI = "https://api.cloudflare.com/client/v4/zones/"

// suggestedHost is the hostname offered for a fresh setup, under the zone the
// user picked during sign-in.
const suggestedHost = "fylane"

// zoneLookup caches the resolved zone name. The connect screen polls every
// few seconds and this costs a network round trip, so it is resolved once per
// certificate and answered from memory after that. Nothing blocks on it: the
// snapshot returns what is known so far, and the name appears on a later poll.
// The mutex is a named field, not embedded, and the fields are reset one by
// one rather than by assigning a fresh struct: a wholesale assignment would
// zero the lock the assigning goroutine is holding.
type zoneState struct {
	mu    sync.Mutex
	key   string // certificate identity: path + modtime + size
	name  string
	done  bool
	busy  bool
	after time.Time // do not retry a failure before this
}

func (z *zoneState) resetLocked(key string) {
	z.key, z.name, z.done, z.busy, z.after = key, "", false, false, time.Time{}
}

var zoneLookup zoneState

// certKey identifies the current certificate without reading its contents.
func certKey(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
}

// cloudflareZone is the domain this machine signed in for, or "" while it is
// still unknown. It never blocks: a caller that gets "" asks again later.
func cloudflareZone() string {
	path := cloudflaredCertPath()
	key := certKey(path)
	if key == "" {
		return ""
	}
	zoneLookup.mu.Lock()
	defer zoneLookup.mu.Unlock()
	// A new certificate is a different account or zone; the old answer is not
	// about this sign-in.
	if zoneLookup.key != key {
		zoneLookup.resetLocked(key)
	}
	if zoneLookup.done {
		return zoneLookup.name
	}
	if zoneLookup.busy || time.Now().Before(zoneLookup.after) {
		return ""
	}
	zoneLookup.busy = true
	go resolveZone(path, key)
	return ""
}

// SuggestedHostname is the address to offer for a fresh named tunnel, or ""
// when the zone is not known yet.
func SuggestedHostname() string {
	if zone := cloudflareZone(); zone != "" {
		return suggestedHost + "." + zone
	}
	return ""
}

func resolveZone(path, key string) {
	name, err := lookupZoneName(path)
	zoneLookup.mu.Lock()
	defer zoneLookup.mu.Unlock()
	if zoneLookup.key != key {
		return // signed out or signed in again while this was in flight
	}
	zoneLookup.busy = false
	if err != nil {
		// Offline, or a token Cloudflare no longer accepts. Back off rather
		// than asking again on every poll; the field falls back to typing.
		zoneLookup.after = time.Now().Add(time.Minute)
		return
	}
	zoneLookup.name, zoneLookup.done = name, true
}

// certCredentials is the block `cloudflared tunnel login` leaves behind. The
// API token in it is Cloudflare's own credential for this machine: it is used
// to ask Cloudflare a question and is never logged, stored or forwarded.
type certCredentials struct {
	ZoneID   string `json:"zoneID"`
	APIToken string `json:"apiToken"`
}

func readCertCredentials(path string) (certCredentials, error) {
	var c certCredentials
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	block := argoToken.FindSubmatch(raw)
	if block == nil {
		return c, errors.New("no tunnel token in the certificate")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(block[1])), ""))
	if err != nil {
		return c, fmt.Errorf("decoding the tunnel token: %w", err)
	}
	if err := json.Unmarshal(decoded, &c); err != nil {
		return c, fmt.Errorf("reading the tunnel token: %w", err)
	}
	if c.ZoneID == "" || c.APIToken == "" {
		// An account-wide sign-in has no single zone. There is nothing to
		// suggest, and the user types the hostname as before.
		return c, errors.New("the certificate names no single zone")
	}
	return c, nil
}

var argoToken = regexp.MustCompile(`(?s)-----BEGIN ARGO TUNNEL TOKEN-----(.*?)-----END ARGO TUNNEL TOKEN-----`)

func lookupZoneName(path string) (string, error) {
	creds, err := readCertCredentials(path)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zoneAPI+url.PathEscape(creds.ZoneID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+creds.APIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cloudflare answered %d", resp.StatusCode)
	}
	var body struct {
		Success bool `json:"success"`
		Result  struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	// Capped: this is a small document, and an unbounded read from the network
	// is an unbounded allocation.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if !body.Success || body.Result.Name == "" {
		return "", errors.New("cloudflare named no zone")
	}
	return body.Result.Name, nil
}

// cloudflareSignOut forgets the Cloudflare authorization on this machine.
//
// It removes the certificate the browser sign-in wrote, and nothing else. The
// tunnel this machine created still exists on the user's account and its
// credentials file stays put: deleting either would be reaching into their
// Cloudflare account from a button labelled "sign out", and an already
// configured tunnel would stop working. Signing in again re-issues the
// certificate and the setup continues from where it was.
func cloudflareSignOut() error {
	path := cloudflaredCertPath()
	if path == "" {
		return errors.New("cannot tell where cloudflared keeps its certificate")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing the Cloudflare certificate: %w", err)
	}
	// A token stored from the older dashboard-token setup is the other way in;
	// leaving it would make "signed out" untrue on the next start.
	return DeleteToken(CloudflareNamed)
}

// bareHost strips a scheme a user pasted along with the hostname.
func bareHost(h string) string {
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.TrimSuffix(h, "/")
}

// runProvider runs one short provider command and returns its combined
// output. The output is returned rather than logged: a tunnel command prints
// hostnames and account identifiers, and those have no business in a log.
func runProvider(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out), err
}

// lastLine is the part of a failed command's output worth putting on screen:
// the final non-empty line, capped. Whole outputs are pages of log records.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 200 {
				return l[:200]
			}
			return l
		}
	}
	return "no output"
}

// provisionCloudflareNamed creates the tunnel on the user's account and
// points their hostname at it. Both steps are what the dashboard would have
// done, which is why the token field is no longer the only way in.
func provisionCloudflareNamed(ctx context.Context, opt Options) error {
	// A dashboard token describes a tunnel that already exists and already
	// has its DNS; doing this again would fight the user's own setup.
	if opt.Token != "" {
		return nil
	}
	host := bareHost(opt.Hostname)
	if host == "" {
		return errors.New("a named tunnel needs the hostname it should answer on")
	}
	bin, ok := lookup("cloudflared", shippedDir())
	if !ok {
		return errors.New("cloudflared is not available")
	}
	if !cloudflareLoggedIn() {
		return errors.New("cloudflared is not connected to a Cloudflare account yet")
	}
	// Creating a tunnel that is already there is the normal case on every
	// apply after the first, so that particular failure is the success path.
	if out, err := runProvider(ctx, bin, "tunnel", "create", CloudflareTunnelName); err != nil {
		if !strings.Contains(out, "already exists") {
			return fmt.Errorf("creating the tunnel on your Cloudflare account: %s", lastLine(out))
		}
	}
	// --overwrite-dns so pointing an existing record at this machine works;
	// without it, moving Fylane to another machine fails on a stale record.
	if out, err := runProvider(ctx, bin, "tunnel", "route", "dns", "--overwrite-dns", CloudflareTunnelName, host); err != nil {
		return fmt.Errorf("pointing %s at the tunnel: %s", host, lastLine(out))
	}
	return nil
}
