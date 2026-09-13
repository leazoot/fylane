// Package approval is the local approval authority.
// Every write reaching the transaction engine passes through Service: policy
// may auto-approve narrowly defined low-risk operations; everything else
// blocks on an explicit local decision within a per-platform budget, then
// degrades to pending_approval (Stage 1 timeout findings). There is no
// configuration that approves everything.
package approval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leazoot/fylane/companion/internal/txn"
)

// Policy modes. There is deliberately no "always allow" mode.
const (
	ModeSafe     = "safe"     // every write operation requires confirmation
	ModeBalanced = "balanced" // non-sensitive creates auto-approve
	// ModeOpen auto-approves non-sensitive creates, updates and moves. A
	// delete still asks: it is the one operation whose undo copy can run
	// out (D33), and the one a person cannot glance past. Sensitive paths
	// and route rules marked "ask" still ask at every mode. Never the
	// default, and never set without an explicit confirmation (D37).
	ModeOpen = "open"
)

// Budgets holds the blocking-approval time budgets measured against the real
// platforms: the universal default is 50s; Claude tolerates 240s.
// ChatGPT's stateless pipeline (2026-07-28) times tool calls out around 60s —
// measured live 2026-08-07 — so it uses the default: degrading to
// pending_approval before the platform's own timeout is what lets it relay a
// useful message and retry instead of reporting an opaque error.
type Budgets struct {
	Default     time.Duration
	PerProvider map[string]time.Duration
}

// DefaultBudgets returns the verified budget table.
func DefaultBudgets() Budgets {
	return Budgets{
		Default: 50 * time.Second,
		PerProvider: map[string]time.Duration{
			"claude": 240 * time.Second,
		},
	}
}

// For returns the blocking budget for a provider.
func (b Budgets) For(provider string) time.Duration {
	if d, ok := b.PerProvider[provider]; ok {
		return d
	}
	return b.Default
}

// Pending is one approval waiting for a local decision.
type Pending struct {
	Request   *txn.ApprovalRequest
	CreatedAt time.Time

	mu        sync.Mutex
	decided   bool
	decidedAt time.Time
	decision  txn.Decision
	done      chan struct{}
}

// Done is closed once the request has been decided, by whichever surface
// answered it. A prompt that is still asking can stop when another one has
// already answered.
func (p *Pending) Done() <-chan struct{} { return p.done }

func (p *Pending) resolve(d txn.Decision) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.decided {
		return false
	}
	p.decided = true
	p.decidedAt = time.Now()
	p.decision = d
	close(p.done)
	return true
}

// Service implements txn.Approver. Zero value is not usable; use New.
type Service struct {
	budgets Budgets
	// onRequest, when set, is called for every approval that needs a human
	// decision (the desktop UI subscribes here). Never called for
	// auto-approved operations.
	onRequest func(*Pending)

	// OnDecision, when set, is called once per resolved approval with the
	// wall-clock wait from prompt creation to the local decision — the
	// precise "approval latency" Beta metric. Platform retries
	// re-attach to the same Pending, so the wait spans them. Never called
	// for auto-approved operations (their wait is zero by definition).
	OnDecision func(p *Pending, approved bool, wait time.Duration)

	mu      sync.Mutex
	mode    string
	pending map[string]*Pending
	// decided holds decisions whose caller never came back to collect them
	// (the platform gave up waiting, or the request was cancelled). A retry
	// of the same change set replays the decision instead of asking again;
	// entries expire so a stale approval can never authorise a later write.
	decided map[string]*Pending
}

// decisionTTL bounds how long an uncollected decision is replayed to a
// retry. It is long enough for a platform to come back after degrading to
// pending_approval, and short enough that the answer still belongs to the
// request the user actually looked at.
const decisionTTL = 15 * time.Minute

// ErrConfirmationRequired is returned when ModeOpen is set without the
// caller confirming it. Like the command gate's open rung, the confirmation
// is the point: it is the moment the user takes on what it means. A mode
// read back from disk at start-up needs none — it was confirmed when it
// was written.
var ErrConfirmationRequired = errors.New("the open write mode writes files without asking; set confirm to acknowledge that")

// New returns a Service in the given mode. An empty mode means ModeSafe —
// the default policy is the strict one.
func New(mode string, budgets Budgets, onRequest func(*Pending)) (*Service, error) {
	switch mode {
	case "":
		mode = ModeSafe
	case ModeSafe, ModeBalanced, ModeOpen:
	default:
		return nil, fmt.Errorf("unknown approval mode %q", mode)
	}
	if budgets.Default <= 0 {
		return nil, fmt.Errorf("approval budget must be positive")
	}
	return &Service{mode: mode, budgets: budgets, onRequest: onRequest,
		pending: map[string]*Pending{}, decided: map[string]*Pending{}}, nil
}

// Approve implements txn.Approver: auto-approve per policy, otherwise block
// for the provider's budget waiting for a local decision. Budget expiry and
// context cancellation both return a Pending decision — never a denial; the
// caller retries with the same change_set_id and re-attaches to the same
// pending approval.
func (s *Service) Approve(ctx context.Context, req *txn.ApprovalRequest) (txn.Decision, error) {
	if s.autoApproved(req) {
		return txn.Decision{Approved: true, Reason: "policy_auto_approved"}, nil
	}

	p, isNew := s.attach(req)
	if isNew && s.onRequest != nil {
		s.onRequest(p)
	}

	timer := time.NewTimer(s.budgets.For(req.Provider))
	defer timer.Stop()
	select {
	case <-p.done:
		s.remove(req.ChangeSetID)
		return p.decision, nil
	case <-timer.C:
		return txn.Decision{Pending: true, Reason: "approval_budget_exceeded"}, nil
	case <-ctx.Done():
		// The platform gave up waiting; the approval itself stays open.
		return txn.Decision{Pending: true, Reason: "caller_disconnected"}, nil
	}
}

// Mode returns the active approval policy mode.
func (s *Service) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// SetMode switches the approval policy at runtime (the desktop settings
// page). There is no mode that approves everything: ModeOpen still asks for
// deletes and sensitive paths, and it cannot be reached without confirm.
// Pending approvals are unaffected.
func (s *Service) SetMode(mode string, confirm bool) error {
	switch mode {
	case ModeSafe, ModeBalanced:
	case ModeOpen:
		if !confirm {
			return ErrConfirmationRequired
		}
	default:
		return fmt.Errorf("unknown approval mode %q", mode)
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	return nil
}

// autoApproved applies the policy table. Balanced mode passes one shape:
// a change set consisting purely of non-sensitive creates ("safe new
// files"). Open mode passes three: non-sensitive creates, updates and
// moves. A delete, a sensitive path, or a set that mixes one in asks at
// every mode; so does a route rule marked "ask".
func (s *Service) autoApproved(req *txn.ApprovalRequest) bool {
	// A route rule the user marked "ask" outranks the policy: it is the
	// user asking for this file to stop here, and the policy table only
	// ever decides what may pass without one.
	if req.MustAsk {
		return false
	}
	mode := s.Mode()
	if mode != ModeBalanced && mode != ModeOpen {
		return false
	}
	// A request with no file operations is not a set of "safe new files";
	// it is one the policy table was never written for — a command,
	// or a malformed change set. The loop below would find nothing to object
	// to and approve it, so refuse before reaching it.
	if len(req.Operations) == 0 {
		return false
	}
	// A command is judged by the rule table (internal/cmdrule), not by the
	// file policy, and never passes on this path.
	if len(req.Command) > 0 {
		return false
	}
	for _, op := range req.Operations {
		if op.Sensitive {
			return false
		}
		switch op.Type {
		case txn.OpCreate:
		case txn.OpUpdate, txn.OpMove:
			if mode != ModeOpen {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// attach returns the pending approval for the change set, creating it on
// first call. Retries of the same change set share one entry — the user sees
// one prompt no matter how often the platform retries.
func (s *Service) attach(req *txn.ApprovalRequest) (*Pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pending[req.ChangeSetID]; ok {
		return p, false
	}
	// A decision the previous caller never collected belongs to this same
	// change set: replay it rather than prompting the user twice. "The same
	// caller" is part of that — a decision is replayed to the platform it was
	// made for and to no other. Callers build keys that already carry the
	// provider; this is the backstop for one that forgets, and it
	// costs a second prompt rather than a silent authorization.
	if p, ok := s.decided[req.ChangeSetID]; ok {
		fresh := time.Since(p.decidedAt) < decisionTTL
		if fresh && p.Request.Provider == req.Provider {
			// Collected once, then gone. remove() already clears it after a
			// successful collection; doing it here as well closes the case
			// where the collecting call's context dies at the same moment its
			// decision arrives — select would take ctx.Done(), remove() would
			// never run, and the decision would be replayed to whatever asked
			// next instead of the user being asked again.
			delete(s.decided, req.ChangeSetID)
			return p, false
		}
		// A decision belonging to another platform is left where it is; only
		// a stale one is swept here.
		if !fresh {
			delete(s.decided, req.ChangeSetID)
		}
	}
	p := &Pending{Request: req, CreatedAt: time.Now(), done: make(chan struct{})}
	s.pending[req.ChangeSetID] = p
	return p, true
}

// sweep drops decisions nobody collected before they went stale. Called
// under s.mu.
func (s *Service) sweep() {
	for id, p := range s.decided {
		if time.Since(p.decidedAt) >= decisionTTL {
			delete(s.decided, id)
		}
	}
}

func (s *Service) remove(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	delete(s.decided, id)
	s.mu.Unlock()
}

// Pending lists approvals awaiting a decision, oldest first. This is what
// the desktop UI renders.
func (s *Service) Pending() []*Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Pending, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, p)
	}
	sortPending(out)
	return out
}

// Resolve records the local decision for a pending approval. It returns
// false when the change set is unknown or already decided. A decision made
// after the caller degraded to pending_approval is picked up by the retry.
func (s *Service) Resolve(changeSetID string, approved bool, reason string) bool {
	s.mu.Lock()
	p, ok := s.pending[changeSetID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	if reason == "" {
		if approved {
			reason = "user_approved"
		} else {
			reason = "user_rejected"
		}
	}
	if !p.resolve(txn.Decision{Approved: approved, Reason: reason}) {
		return false
	}
	if s.OnDecision != nil {
		s.OnDecision(p, approved, time.Since(p.CreatedAt))
	}
	// The prompt is answered, so it leaves the pending list at once — a
	// decision the user already made must never keep the gate shut. The
	// waiter still holds this pointer and reads the decision from the
	// closed channel; a retry that arrives later finds it in `decided`.
	s.mu.Lock()
	delete(s.pending, changeSetID)
	s.decided[changeSetID] = p
	s.sweep()
	s.mu.Unlock()
	return true
}

// ApproveRead blocks for a read confirmation of a sensitive file (
// sensitive reads are confirmed every time, in every mode). The pending
// entry is keyed by workspace and path, so a platform retry after
// pending_approval re-attaches to the same prompt.
func (s *Service) ApproveRead(ctx context.Context, provider, workspaceID, workspaceName, path string) (txn.Decision, error) {
	who := provider
	if who == "" {
		who = "the caller"
	}
	req := &txn.ApprovalRequest{
		// The provider is part of the key for the same reason it is part of a
		// command's: approving .env for one platform is not approving
		// it for the next one to ask.
		ChangeSetID:   "read:" + who + ":" + workspaceID + ":" + path,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
		Provider:      provider,
		Summary:       "Read sensitive file " + path,
		Operations:    []txn.OpPreview{{Type: "read", Path: path, Sensitive: true}},
		// A sensitive read changes nothing on disk, so a prompt that framed
		// it as a pending write asked the wrong question and offered to show
		// a diff that does not exist. What is at stake is the file's contents
		// leaving for the platform.
		Kind:   txn.KindDisclosure,
		Reason: "the contents of this file would be sent to " + who,
	}
	return s.Approve(ctx, req)
}

func sortPending(ps []*Pending) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].CreatedAt.Before(ps[j-1].CreatedAt); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}
