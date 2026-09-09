package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/leazoot/fylane/companion/internal/pairclaim"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/shared/authsrv"
)

// seenInterval bounds how often one provider's row is rewritten. A list that
// says "connected" does not need a write per tool call.
const seenInterval = time.Minute

// seenRecorder records that a platform is actively using its grant, which is
// the "last seen" time beside its row. It writes last_tool_call_at only:
// pairedRecorder owns the other timestamp, and a caller that overwrote both
// would erase the record of when the user granted access.
//
// It runs on the request path, so it must not block it or fail it. The write
// is best effort — a row that loses a race is the next call's job — and it is
// throttled per provider.
//
// RemoteConnectorID is the provider's own name in both modes: direct mode has
// no remote connector to name, and the OAuth client id rotates, which is not
// what a row meant to be stable should be keyed on.
func seenRecorder(ctx context.Context, st *store.Store, log *slog.Logger) func(string) {
	var mu sync.Mutex
	last := map[string]time.Time{}

	return func(provider string) {
		now := time.Now().UTC()

		mu.Lock()
		if seen, ok := last[provider]; ok && now.Sub(seen) < seenInterval {
			mu.Unlock()
			return
		}
		last[provider] = now
		mu.Unlock()

		go func() {
			err := st.UpsertConnector(ctx, &store.Connector{
				Provider:          provider,
				RemoteConnectorID: provider,
				Status:            "active",
				LastToolCallAt:    now,
			})
			if err != nil {
				log.Warn("recording that a platform called", "provider", provider, "error", err)
			}
		}()
	}
}

// newPairClaims wires a pairing service with the two things both modes do
// with a prompt: announce it, and record the grant once it is approved. Only
// where the request details come from differs between relay and direct mode,
// so the caller supplies that and nothing else.
//
// announce is a parameter rather than a direct call so a test can exercise
// this wiring without putting a notification on someone's screen. It is not a
// package-level seam: OnClaim runs on its own goroutine, and a variable a
// test swapped back afterwards would be read while it was being written.
func newPairClaims(log *slog.Logger, paired func(string), announce func(string)) *pairclaim.Service {
	claims := pairclaim.New()
	claims.OnApproved = paired
	claims.OnClaim = func(c *pairclaim.Claim) {
		log.Info("push pairing requested", "verify_code", c.VerifyCode)
		announce(c.ClientName)
	}
	return claims
}

// pairedRecorder records that the user approved a platform's request to reach
// this machine. That approval — not the first tool call — is when the lane's
// "connected" list should light up: the grant is what the user did, and a
// platform that holds one is connected whether or not it has asked for
// anything yet.
//
// It takes the client name the pairing prompt showed and maps it with the
// same table the relay stamps requests with, so both sides name the platform
// identically. A name that maps to nothing is dropped rather than given a
// row: the list shows three known platforms, and an unrecognized caller
// belongs in the audit log, not on it.
func pairedRecorder(ctx context.Context, st *store.Store, log *slog.Logger) func(string) {
	return func(clientName string) {
		provider := authsrv.ProviderFromClient(clientName, nil)
		if provider == "unknown" {
			return
		}
		err := st.UpsertConnector(ctx, &store.Connector{
			Provider:          provider,
			RemoteConnectorID: provider,
			Status:            "active",
			LastConnectedAt:   time.Now().UTC(),
		})
		if err != nil {
			log.Warn("recording a pairing", "provider", provider, "error", err)
		}
	}
}
