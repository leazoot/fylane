package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/leazoot/fylane/relay/internal/pgstore"
	"github.com/leazoot/fylane/shared/authsrv"
	"github.com/leazoot/fylane/shared/buildinfo"
	"github.com/leazoot/fylane/shared/ratelimit"
	"github.com/leazoot/fylane/shared/tunnel"
)

const (
	// ratePerSecond and rateBurst are the abuse-control ceiling every
	// public entry point shares: the OAuth surface, the per-device tunnel
	// calls, and legacy /mcp. Published in the security notes.
	ratePerSecond = 5
	rateBurst     = 20
)

// setOnline reports device tunnel state to the metadata store, when one is
// configured. Best effort: status reporting never blocks the tunnel.
func setOnline(pg *pgstore.Store, deviceID string, online bool) {
	if pg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pg.SetDeviceOnline(ctx, deviceID, online, ""); err != nil {
		log.Printf("relay: recording device status: %v", err)
	}
	if online {
		if err := pg.RecordConnect(ctx, deviceID); err != nil {
			log.Printf("relay: recording connect: %v", err)
		}
	}
}

var version = buildinfo.Version

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("fylane-relay", version)
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			log.Fatalf("fylane-relay: %v", err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  fylane-relay serve [-addr 127.0.0.1:9000] [-issuer https://relay.example]
  fylane-relay version

Authentication modes:
  default          OAuth 2.1 + PKCE with device pairing; Companions register
                   at /v1/devices and tunnel with device credentials.
  FYLANE_RELAY_DB  where OAuth metadata persists: a PostgreSQL URL, or any
                   other value as a SQLite file path. Unset keeps it in
                   memory, so every restart drops existing pairings.
  FYLANE_TUNNEL_TOKEN set
                   legacy shared-token mode for simple self-hosting: the
                   token authenticates the tunnel, and /mcp callers present
                   it as a bearer header or as /mcp/<token> (for platforms
                   that cannot send custom headers).`)
}

// clientIP extracts the peer address for per-IP throttling of endpoints that
// have no authenticated device identity yet.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// withIPLimit throttles a handler per client IP. The device-keyed limiter on
// /mcp cannot cover callers that fail authentication or hold no device.
func withIPLimit(l *ratelimit.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r)) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// withAccessLog emits one line per auth-surface request — method, path,
// status, duration only. Query strings, bodies, and headers never reach the
// log: OAuth parameters and codes travel there.
func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		log.Printf("authsrv: %s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// legacyMCP guards the public MCP endpoint in shared-token mode. AI platforms
// cannot attach custom headers, so the token is accepted either as a bearer
// header or as a capability URL segment (/mcp/<token>). The segment is
// rewritten to /mcp before forwarding so the token never reaches access logs
// or the companion.
func legacyMCP(ts *tunnel.Server, token string, limiter *ratelimit.Limiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(clientIP(r)) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if seg := strings.TrimPrefix(r.URL.Path, "/mcp/"); seg != r.URL.Path {
			presented = strings.TrimSuffix(seg, "/")
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/mcp"
		r2.URL.RawPath = ""
		ts.ServeHTTP(w, r2)
	})
}

// isPostgresDSN distinguishes a PostgreSQL connection string from a SQLite
// file path in FYLANE_RELAY_DB.
func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") ||
		strings.Contains(dsn, "host=") || strings.Contains(dsn, "dbname=")
}

// startPurge sweeps expired auth records hourly and returns the stopper. Every
// read already treats an expired record as absent; this only keeps the file
// from growing forever on a long-running relay.
func startPurge(store *authsrv.SQLiteStore) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := store.PurgeExpired(ctx, time.Now()); err != nil && ctx.Err() == nil {
					log.Printf("relay: purging expired auth records: %v", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9000", "listen address")
	issuer := fs.String("issuer", "", "public base URL of this relay (required unless FYLANE_TUNNEL_TOKEN is set)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	mux := http.NewServeMux()
	legacyToken := os.Getenv("FYLANE_TUNNEL_TOKEN")

	var ts *tunnel.Server
	if legacyToken != "" {
		// Legacy self-hosting mode: one shared secret, no OAuth surface.
		// The token gates /mcp too — there is no unauthenticated endpoint.
		var err error
		ts, err = tunnel.NewServer(legacyToken)
		if err != nil {
			return err
		}
		guarded := legacyMCP(ts, legacyToken, ratelimit.New(ratePerSecond, rateBurst))
		mux.Handle("/mcp", guarded)
		mux.Handle("/mcp/", guarded)
		log.Printf("running in legacy shared-token mode (FYLANE_TUNNEL_TOKEN)")
	} else {
		if *issuer == "" || !strings.HasPrefix(*issuer, "http") {
			return fmt.Errorf("-issuer is required in OAuth mode (public https base URL)")
		}
		var store authsrv.Store
		switch dsn := os.Getenv("FYLANE_RELAY_DB"); {
		case isPostgresDSN(dsn):
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			pg, err := pgstore.Open(ctx, dsn)
			cancel()
			if err != nil {
				return err
			}
			defer pg.Close()
			store = pg
			log.Printf("using PostgreSQL metadata store")
		case dsn != "":
			// Anything that is not a PostgreSQL URL is a SQLite file path, so
			// a self-hosted relay is one binary and one file.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			lite, err := authsrv.OpenSQLite(ctx, dsn)
			cancel()
			if err != nil {
				return err
			}
			defer lite.Close()
			store = lite
			stopPurge := startPurge(lite)
			defer stopPurge()
			log.Printf("using SQLite metadata store")
		default:
			store = authsrv.NewMemoryStore()
			log.Printf("using in-memory metadata store (set FYLANE_RELAY_DB for persistence)")
		}
		auth := &authsrv.Server{Issuer: strings.TrimRight(*issuer, "/"), Store: store}
		// OAuth and pairing endpoints have no device identity to key on, so
		// they get per-IP throttling (registration, token, pairing brute
		// force). Mounted as the fallback handler; /mcp and /tunnel below
		// take precedence.
		authMux := http.NewServeMux()
		auth.Routes(authMux)
		mux.Handle("/", withIPLimit(ratelimit.New(ratePerSecond, rateBurst), withAccessLog(authMux)))

		pg, _ := store.(*pgstore.Store)
		ts = tunnel.NewServerAuth(auth.AuthenticateDevice)
		ts.OnConnect = func(deviceID string) { setOnline(pg, deviceID, true) }
		ts.OnDisconnect = func(deviceID string) { setOnline(pg, deviceID, false) }

		// Basic per-device abuse control.
		limiter := ratelimit.New(ratePerSecond, rateBurst)

		// The MCP resource requires a platform access token; requests route
		// to the tunnel of the device the token is bound to.
		mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deviceID, provider, err := auth.ValidateBearerProvider(r)
			if errors.Is(err, authsrv.ErrStoreUnavailable) {
				// Infrastructure failure, not an auth verdict: a 401 here
				// would push clients into pointless re-auth loops.
				http.Error(w, "service temporarily unavailable", http.StatusServiceUnavailable)
				log.Printf("relay: %s /mcp -> 503 (store unavailable)", r.Method)
				return
			}
			if err != nil {
				w.Header().Set("WWW-Authenticate", auth.WWWAuthenticate())
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				log.Printf("relay: %s /mcp -> 401", r.Method)
				return
			}
			if !limiter.Allow(deviceID) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				log.Printf("relay: %s /mcp -> 429", r.Method)
				return
			}
			// Stamp (never merge) the platform so approval budgets apply per
			// provider; a caller-supplied value must not survive.
			r.Header.Set(tunnel.ProviderHeader, provider)
			start := time.Now()
			status := ts.ForwardTo(w, r, deviceID)
			if pg != nil {
				errCode := ""
				if status >= 400 {
					errCode = fmt.Sprintf("http_%d", status)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				pg.RecordCall(ctx, deviceID, time.Since(start), errCode)
				cancel()
			}
		}))
	}
	mux.HandleFunc("/tunnel", ts.HandleTunnel)
	// Liveness for process supervisors and container health checks. It says
	// the process is up and nothing else: how many devices are paired or
	// online is not a stranger's business.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	log.Printf("relay listening on %s (public MCP at /mcp, companion tunnel at /tunnel)", *addr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		log.Println("shut down")
		return nil
	}
}
