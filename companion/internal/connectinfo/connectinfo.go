// Package connectinfo preflights a relay for platform onboarding: it checks
// the OAuth discovery documents platforms rely on and renders the connector
// values and per-platform steps the user needs. It never touches the OS
// keychain; the caller adds pairing state to the report.
package connectinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/leazoot/fylane/companion/internal/devicecred"
)

// Report is the outcome of a relay preflight.
type Report struct {
	RelayBase    string
	ConnectorURL string
	Issuer       string
	// DCR and PKCES256 report what the discovery documents advertise;
	// platforms need both to connect unattended.
	DCR      bool
	PKCES256 bool
	// Problems lists failed checks in the order they were found. An empty
	// list means the relay looks ready for platform connectors.
	Problems []string
	// DeviceID is filled by the caller when this machine is paired.
	DeviceID string
	// PairHint is filled by the caller when this machine is not paired.
	PairHint string
}

// Inspect fetches and validates the relay's OAuth discovery documents.
// It returns an error only for an unusable relay URL; reachability and
// metadata failures land in Report.Problems so the user sees all of them
// at once.
func Inspect(ctx context.Context, relayURL string) (*Report, error) {
	base, err := devicecred.APIBase(relayURL)
	if err != nil {
		return nil, err
	}
	r := &Report{RelayBase: base, ConnectorURL: base + "/mcp"}

	var protected struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := fetchJSON(ctx, base+"/.well-known/oauth-protected-resource", &protected); err != nil {
		r.Problems = append(r.Problems, "protected-resource discovery: "+err.Error()+
			" (is the relay running in OAuth mode with -issuer set?)")
	} else if len(protected.AuthorizationServers) == 0 {
		r.Problems = append(r.Problems, "protected-resource discovery lists no authorization servers")
	} else {
		r.Issuer = protected.AuthorizationServers[0]
	}

	var srv struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	if err := fetchJSON(ctx, base+"/.well-known/oauth-authorization-server", &srv); err != nil {
		r.Problems = append(r.Problems, "authorization-server discovery: "+err.Error())
		return r, nil
	}
	if srv.AuthorizationEndpoint == "" || srv.TokenEndpoint == "" {
		r.Problems = append(r.Problems, "authorization-server metadata is missing the authorize or token endpoint")
	}
	r.DCR = srv.RegistrationEndpoint != ""
	if !r.DCR {
		r.Problems = append(r.Problems, "no registration_endpoint: platforms cannot self-register (DCR)")
	}
	r.PKCES256 = slices.Contains(srv.CodeChallengeMethods, "S256")
	if !r.PKCES256 {
		r.Problems = append(r.Problems, "S256 not advertised in code_challenge_methods_supported")
	}
	if r.Issuer == "" {
		r.Issuer = srv.Issuer
	}
	return r, nil
}

func fetchJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}

// Render writes the human-readable onboarding sheet.
func Render(w io.Writer, r *Report) {
	fmt.Fprintf(w, "Fylane relay preflight\n")
	fmt.Fprintf(w, "  relay:         %s\n", r.RelayBase)
	fmt.Fprintf(w, "  connector URL: %s\n", r.ConnectorURL)
	if r.Issuer != "" {
		fmt.Fprintf(w, "  OAuth issuer:  %s\n", r.Issuer)
	}
	switch {
	case len(r.Problems) == 0:
		fmt.Fprintf(w, "  discovery:     ok (DCR + PKCE S256)\n")
	default:
		fmt.Fprintf(w, "  discovery:     %d problem(s), see below\n", len(r.Problems))
	}
	switch {
	case r.DeviceID != "":
		fmt.Fprintf(w, "  device:        paired (%s)\n", r.DeviceID)
	case r.PairHint != "":
		fmt.Fprintf(w, "  device:        not paired — %s\n", r.PairHint)
	}

	fmt.Fprint(w, `
Add the connector URL above on each platform, then enter the pairing code
from `+"`fylane-companion pair`"+` on the authorization page:
  Claude (web): Settings -> Connectors -> Add custom connector, then click
                Connect on the new entry to start authorization.
  ChatGPT:      Settings -> Connectors (Developer mode / paid plan), add the
                URL; the handshake and authorization run automatically.
  Grok:         Add Custom MCP Connector with the URL and authorize.
                Note: Grok shows no platform-side write confirmation and
                caps tool calls at ~60s; local approval still gates every
                write, and long-pending writes return pending_approval for
                retry with the same change_set_id.
`)

	if len(r.Problems) > 0 {
		fmt.Fprintf(w, "\nProblems:\n")
		for _, p := range r.Problems {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}
}
