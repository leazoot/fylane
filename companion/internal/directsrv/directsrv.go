// Package directsrv serves Fylane's public surface from the Companion itself:
// the OAuth endpoints a platform connector needs, plus /mcp behind the access
// token those endpoints issue. A tunnel points straight at this listener, so
// no relay process exists in the path — the relay is only worth a hop when it
// runs on someone else's machine.
//
// The listener still binds loopback: reaching it from outside requires the
// tunnel, which the user starts deliberately.
//
// What this package must never expose is device registration or pairing-code
// minting. In relay mode an access token names whose tunnel a call is routed
// to; here everything that arrives goes to this machine, so a stranger able
// to mint a pairing code would be pairing themselves to the user's files.
// Codes come from the desktop app over the local control API instead.
package directsrv

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/leazoot/fylane/shared/authsrv"
	"github.com/leazoot/fylane/shared/ratelimit"
	"github.com/leazoot/fylane/shared/tunnel"
)

// Server owns the auth store and the public mux.
type Server struct {
	store *authsrv.SQLiteStore
	auth  *authsrv.Server
	mcp   http.Handler
	log   *slog.Logger

	mu        sync.RWMutex
	publicURL string
}

// Open prepares the direct-connect surface: the auth database in dataDir, the
// single local device, and the handler wrapping mcp. deviceName is shown on
// nothing remote — it only labels the local row.
func Open(ctx context.Context, dataDir, deviceName string, mcp http.Handler, log *slog.Logger) (*Server, error) {
	store, err := authsrv.OpenSQLite(ctx, filepath.Join(dataDir, "auth.db"))
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, mcp: mcp, log: log}
	s.auth = &authsrv.Server{Store: store, IssuerFn: s.PublicURL}
	if err := s.auth.EnsureLocalDevice(deviceName); err != nil {
		store.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the auth database.
func (s *Server) Close() error { return s.store.Close() }

// SetPublicURL records the address platforms reach this Companion at. The
// tunnel decides it, and a quick tunnel decides it again on every restart, so
// it is settable at runtime rather than fixed at startup.
func (s *Server) SetPublicURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicURL = u
}

// SetClaimOrigin tells the pairing page which loopback origin to claim
// through. In direct mode that listener is this same process, so the address
// is known exactly rather than assumed — a Companion moved off the default
// port keeps push pairing instead of silently losing it. Call before
// serving; the page reads it per request.
func (s *Server) SetClaimOrigin(origin string) { s.auth.CompanionOrigin = origin }

// PublicURL returns the current public base URL, empty when no tunnel is up.
func (s *Server) PublicURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicURL
}

// ConnectorURL is what the user pastes into a platform; empty without a
// public URL, because a connector address that resolves to nothing is worse
// than none.
func (s *Server) ConnectorURL() string {
	if u := s.PublicURL(); u != "" {
		return u + "/mcp"
	}
	return ""
}

// Connected reports whether platforms can currently reach this machine. It
// satisfies the control API's tunnel-status interface.
func (s *Server) Connected() bool { return s.PublicURL() != "" }

// PairingCode mints a code for the local device (desktop connect sheet).
func (s *Server) PairingCode(context.Context) (string, time.Duration, error) {
	return s.auth.IssuePairingCode(authsrv.LocalDeviceID)
}

// RequestInfo and Approve back push pairing without a network round trip:
// this process is the authorization server the pairing page is talking to.

func (s *Server) RequestInfo(_ context.Context, requestID string) (clientName, verify string, err error) {
	return s.auth.RequestInfo(requestID)
}

func (s *Server) Approve(_ context.Context, requestID string) (nonce string, err error) {
	return s.auth.ApproveRequest(requestID, authsrv.LocalDeviceID)
}

// Handler returns the public mux: OAuth endpoints under a per-IP limiter and
// /mcp behind the bearer token they issue.
func (s *Server) Handler() http.Handler {
	authMux := http.NewServeMux()
	s.auth.DirectRoutes(authMux)

	mux := http.NewServeMux()
	mux.Handle("/", s.withIPLimit(ratelimit.New(5, 20), s.withAccessLog(authMux)))
	mux.Handle("/mcp", s.guardedMCP())
	return mux
}

// guardedMCP validates the platform access token and stamps the calling
// platform. A token bound to any other device is refused: in this mode there
// is exactly one device, so anything else is a token from another deployment.
func (s *Server) guardedMCP() http.Handler {
	// Every caller arrives through the same tunnel and the same one device, so
	// neither an IP nor a device key distinguishes anyone. The ceiling is
	// therefore global — one machine's worth of MCP traffic.
	limiter := ratelimit.New(5, 20)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceID, provider, err := s.auth.ValidateBearerProvider(r)
		if errors.Is(err, authsrv.ErrStoreUnavailable) {
			// Infrastructure failure, not an auth verdict: a 401 here would
			// push clients into pointless re-auth loops.
			http.Error(w, "service temporarily unavailable", http.StatusServiceUnavailable)
			s.log.Warn("direct mcp request rejected", "status", http.StatusServiceUnavailable)
			return
		}
		if err != nil || deviceID != authsrv.LocalDeviceID {
			w.Header().Set("WWW-Authenticate", s.auth.WWWAuthenticate())
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.log.Info("direct mcp request rejected", "status", http.StatusUnauthorized)
			return
		}
		if !limiter.Allow("mcp") {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			s.log.Warn("direct mcp request rejected", "status", http.StatusTooManyRequests)
			return
		}
		// Stamp (never merge) the platform so approval budgets apply per
		// provider; a caller-supplied value must not survive.
		r.Header.Set(tunnel.ProviderHeader, provider)
		s.mcp.ServeHTTP(w, localHost(r))
	})
}

// localHost rewrites the request's Host to the address it actually arrived
// on. The MCP server enables DNS-rebinding protection for loopback listeners
// and rejects any other Host — correct for the unauthenticated local
// listener, but here the tunnel forwards the public hostname verbatim and the
// request has already presented a device-bound access token. The relay path
// gets this for free, because the tunnel client re-issues the call locally.
func localHost(r *http.Request) *http.Request {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return r
	}
	r2 := r.Clone(r.Context())
	r2.Host = local.String()
	return r2
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// withIPLimit throttles callers that hold no credential yet — registration,
// token, and pairing-page brute force.
func (s *Server) withIPLimit(l *ratelimit.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r)) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// withAccessLog records method, path, status, and duration — never query
// strings, bodies, or headers: OAuth codes and tokens travel there.
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.log.Info("auth request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

// PurgeLoop removes expired auth records hourly until ctx is cancelled.
func (s *Server) PurgeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.store.PurgeExpired(ctx, time.Now()); err != nil && ctx.Err() == nil {
				s.log.Warn("purging expired auth records", "error", err)
			}
		}
	}
}
