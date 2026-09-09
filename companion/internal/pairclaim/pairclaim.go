// Package pairclaim implements the Companion side of push pairing.
// The relay's pairing page runs in a browser on this same machine and claims
// the flow through the loopback listener; the user confirms in the desktop
// app after comparing the verify code, and the approval itself is performed
// against the relay with this device's credentials. The loopback surface
// grants nothing by itself: a hostile local page can at most make the
// desktop show a prompt whose verify code will not match anything the user
// is looking at.
package pairclaim

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	// claimTTL bounds how long an unanswered prompt stays alive.
	claimTTL = 10 * time.Minute
	// waitBudget is how long one loopback request blocks before answering
	// "pending" (the page retries).
	waitBudget = 25 * time.Second
)

// Claim is one pending push-pairing decision, as shown to the user.
type Claim struct {
	RequestID  string    `json:"request_id"`
	ClientName string    `json:"client_name"`
	VerifyCode string    `json:"verify_code"`
	CreatedAt  time.Time `json:"created_at"`
}

type pending struct {
	claim    Claim
	done     chan struct{}
	approved bool
	nonce    string
}

// Service coordinates loopback claims with the local decision. Info and
// Approve talk to the relay with device credentials; OnClaim surfaces the
// prompt (desktop raise + notification).
type Service struct {
	// AllowedOrigin is the relay's web origin (scheme://host[:port]); claim
	// requests from any other Origin are refused.
	AllowedOrigin string
	// AllowedOriginFn, when set, supplies that origin per request: in direct
	// mode a tunnel can rename this machine while the daemon runs.
	AllowedOriginFn func() string
	Info            func(ctx context.Context, requestID string) (clientName, verify string, err error)
	Approve         func(ctx context.Context, requestID string) (nonce string, err error)
	OnClaim         func(*Claim)
	// OnApproved reports a pairing the user accepted and the relay bound. It
	// fires only after Approve returned, so it describes a platform that can
	// now reach this machine rather than one that merely asked to.
	OnApproved func(clientName string)

	mu      sync.Mutex
	pending map[string]*pending
}

func New() *Service {
	return &Service{pending: map[string]*pending{}}
}

// allowedOrigin returns the origin claims must come from right now.
func (s *Service) allowedOrigin() string {
	if s.AllowedOriginFn != nil {
		if o := s.AllowedOriginFn(); o != "" {
			return o
		}
	}
	return s.AllowedOrigin
}

// Pending lists claims awaiting a decision, for the control API.
func (s *Service) Pending() []Claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Claim, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, p.claim)
	}
	return out
}

// Resolve records the local decision. On approval the relay is asked to
// bind this device and mint the continuation nonce; the waiting loopback
// request picks it up. Returns false for unknown or already-decided claims.
func (s *Service) Resolve(ctx context.Context, requestID string, approved bool) bool {
	s.mu.Lock()
	p, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	if approved && s.Approve != nil {
		if nonce, err := s.Approve(ctx, requestID); err == nil {
			p.approved = true
			p.nonce = nonce
			if s.OnApproved != nil {
				// Off the decision path: the page is waiting on p.done, and
				// recording who was paired is not worth making it wait.
				go s.OnApproved(p.claim.ClientName)
			}
		}
		// An approve failure falls through as a rejection: the page keeps
		// its manual fallback and the user can retry the whole flow.
	}
	close(p.done)
	return true
}

// submit registers (or re-attaches to) the claim and blocks for a decision
// within the wait budget.
func (s *Service) submit(ctx context.Context, requestID string) (status string, nonce string) {
	s.mu.Lock()
	p, ok := s.pending[requestID]
	if !ok {
		s.mu.Unlock()
		name, verify, err := s.Info(ctx, requestID)
		if err != nil {
			return "unknown", ""
		}
		s.mu.Lock()
		// Re-check: a concurrent claim may have registered meanwhile.
		if existing, again := s.pending[requestID]; again {
			p = existing
		} else {
			p = &pending{
				claim: Claim{RequestID: requestID, ClientName: name,
					VerifyCode: verify, CreatedAt: time.Now()},
				done: make(chan struct{}),
			}
			s.pending[requestID] = p
			s.prune()
			if s.OnClaim != nil {
				go s.OnClaim(&p.claim)
			}
		}
	}
	s.mu.Unlock()

	timer := time.NewTimer(waitBudget)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.approved {
			return "approved", p.nonce
		}
		return "rejected", ""
	case <-timer.C:
		return "pending", ""
	case <-ctx.Done():
		return "pending", ""
	}
}

// prune drops prompts nobody answered. Callers hold s.mu.
func (s *Service) prune() {
	cutoff := time.Now().Add(-claimTTL)
	for id, p := range s.pending {
		if p.claim.CreatedAt.Before(cutoff) {
			delete(s.pending, id)
			close(p.done)
		}
	}
}

// Handler serves POST /pair/claim on the loopback listener. Cross-origin
// calls are expected (the page lives on the relay origin) and gated to
// exactly that origin; the Private-Network-Access preflight is answered so
// Chromium allows the https→loopback hop.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed := s.allowedOrigin(); origin != allowed || allowed == "" {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			RequestID string `json:"request_id"`
			// Cancel says the page finished this authorization some other way
			// — the user typed a pairing code — so the prompt this claim
			// raised should go rather than outlive the request it was for
			//. It can only withdraw a question, never answer one: a
			// hostile local page could at worst make the user ask again, and
			// the Origin gate above already keeps other pages out.
			Cancel bool `json:"cancel"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.RequestID == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if body.Cancel {
			s.Resolve(r.Context(), body.RequestID, false)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "canceled"})
			return
		}
		status, nonce := s.submit(r.Context(), body.RequestID)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{"status": status}
		if nonce != "" {
			resp["nonce"] = nonce
		}
		json.NewEncoder(w).Encode(resp)
	})
}
