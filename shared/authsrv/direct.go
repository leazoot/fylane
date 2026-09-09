package authsrv

import (
	"errors"
	"net/http"
	"time"
)

// Direct mode: the Companion serves these endpoints itself and a tunnel
// points straight at it, so no relay process is involved at all. The
// difference from relay mode is not cosmetic and drives what may be exposed:
//
// In relay mode an access token names which device's tunnel a call is routed
// to, so a stranger who registers a device only ever reaches their own
// machine. In direct mode there is no routing — everything that arrives at
// /mcp arrives at this Companion. Device registration and pairing-code
// minting therefore MUST NOT be reachable from the network: the one device is
// created in process, and codes come from the desktop app over the local
// control API. DirectRoutes exists so that is a property of the mux rather
// than of a guard someone can forget.

// LocalDeviceID is the identity of the single device of a Companion serving
// its own endpoints. It is not a credential: nothing can authenticate as it,
// because the secret minted with the row is discarded immediately.
const LocalDeviceID = "dev_local"

// DirectRoutes registers only the endpoints a platform connector needs to
// complete an OAuth flow: discovery, dynamic client registration, the pairing
// page, and the token endpoint. Device registration (/v1/devices) and pairing
// codes (/v1/pair) are deliberately absent — see above.
func (s *Server) DirectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleASMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleResourceMetadata)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("GET /authorize", s.handleAuthorizeGet)
	mux.HandleFunc("POST /authorize", s.handleAuthorizePost)
	mux.HandleFunc("POST /authorize/continue", s.handleAuthorizeContinue)
	mux.HandleFunc("POST /token", s.handleToken)
}

// EnsureLocalDevice creates the single local device row if it does not exist
// yet, so grants have something to bind to. The generated secret is thrown
// away on purpose: in direct mode nothing presents device credentials, and a
// stored secret would only be one more thing that could leak.
func (s *Server) EnsureLocalDevice(name string) error {
	_, err := s.Store.GetDevice(LocalDeviceID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.Store.CreateDevice(&Device{
		ID:         LocalDeviceID,
		SecretHash: hashSecret(randomToken(32)),
		Name:       name,
		CreatedAt:  time.Now(),
	})
}
