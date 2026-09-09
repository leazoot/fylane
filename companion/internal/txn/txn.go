// Package txn implements the Fylane local file transaction: the
// 14-step pipeline every write to a workspace must go through. No other code
// path may modify workspace files on behalf of a remote caller.
//
// Step map: 1 validate workspace → 2-4 normalize/resolve/symlink-check every
// path (sandbox) → 5 policy check → 6 base-hash check → 7-8 build the new
// state and diffs (in memory; disk staging happens inside the atomic
// replace) → 9 approval → 10 durable backups → 11 atomic apply → 12 verify
// result hashes → 13 journal (change set + audit events) → 14 result.
package txn

import (
	"context"
	"time"
)

// Operation types, matching the apply_change_set input structure.
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpMove   = "move"
	OpDelete = "delete"
)

// Result statuses (plus the pending_approval degradation from the
// Stage 1 timeout findings).
const (
	StatusApplied  = "applied"
	StatusConflict = "conflict"
	StatusDenied   = "denied"
	StatusFailed   = "failed"
	StatusPending  = "pending_approval"
)

// Operation is one file operation inside a change set.
type Operation struct {
	Type string `json:"type"`
	// Path is the target for create/update/delete.
	Path string `json:"path,omitempty"`
	// From/To are used by move.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Content is the full new file content for create/update.
	Content string `json:"content,omitempty"`
	// ExpectedSHA256 is the base hash for update/move/delete of a file.
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	// Recursive must be set to delete a non-empty directory.
	Recursive bool `json:"recursive,omitempty"`
}

// Request is one change-set execution request. Retrying with the same
// ChangeSetID is idempotent: an already-applied change set returns its stored
// result instead of applying again.
type Request struct {
	// ChangeSetID identifies the request (chg_...). Empty means generate.
	ChangeSetID string
	// Provider is the platform that initiated the change (claude/chatgpt/...).
	Provider string
	// Summary is the model-supplied description shown at approval.
	Summary    string
	Operations []Operation
	// MustAsk forces local confirmation for this change set even where the
	// approval policy would have let it through. A route rule marked "ask"
	// sets it. It can only ever add a confirmation, never remove one — the
	// policy table stays the floor.
	MustAsk bool
}

// OpResult is the per-operation outcome inside an applied result.
type OpResult struct {
	Path   string `json:"path"`
	Status string `json:"status" jsonschema:"created | updated | moved | deleted"`
	SHA256 string `json:"sha256,omitempty"`
}

// Conflict describes why a change set was rejected before approval
// .
type Conflict struct {
	Path          string `json:"path"`
	Reason        string `json:"reason"`
	CurrentSHA256 string `json:"current_sha256,omitempty"`
	Action        string `json:"action"`
	// Diff shows what a rollback would overwrite when a file changed after
	// apply (show the diff, never overwrite silently).
	Diff string `json:"diff,omitempty"`
}

// Result is the outcome of executing a change set.
type Result struct {
	Status                 string     `json:"status"`
	ChangeSetID            string     `json:"change_set_id,omitempty"`
	Operations             []OpResult `json:"operations,omitempty"`
	RollbackAvailableUntil time.Time  `json:"rollback_available_until,omitzero"`
	Conflict               *Conflict  `json:"conflict,omitempty"`
	Reason                 string     `json:"reason,omitempty" jsonschema:"Set for denied/failed results."`
	// Effects is what actually happened to each path, present only when a
	// change set did not complete. A failed result used to say the same
	// thing about every path it touched — the disk state is not known — and
	// that was a statement about the result rather than about the world.
	Effects []OpEffect `json:"effects,omitempty"`
	// Replayed marks a result served from an earlier, already-finished run
	// of the same change set. Internal to the Core — a retry must not be
	// counted twice by anything downstream. Never serialized.
	Replayed bool `json:"-"`
}

// OpPreview is what the approver (local UI) sees for one operation.
type OpPreview struct {
	Type string `json:"type"`
	Path string `json:"path"`
	// To is set for move operations.
	To string `json:"to,omitempty"`
	// Diff is a unified diff of the change; "(binary change)" for binary
	// content, empty for moves and directory deletes.
	Diff string `json:"diff,omitempty"`
	// Sensitive marks operations touching sensitive files (high-risk).
	Sensitive bool `json:"sensitive,omitempty"`
	// RecursiveDelete marks non-empty directory deletion (double confirm).
	RecursiveDelete bool `json:"recursive_delete,omitempty"`
	// Tree is how much that delete takes. Nil where the question does not
	// apply or the budget ran out before the walk started — which is not the
	// same as an empty directory, and a zero here would read as one.
	Tree *TreeSize `json:"tree,omitempty"`
	// BeyondUndo says the copy this delete makes will not fit in the recycle
	// area, so the undo button will not have anything behind it. Said only
	// when it is known.
	BeyondUndo bool `json:"beyond_undo,omitempty"`
	// Impact is how far this operation reaches — how many places outside the
	// file use the symbols it disturbs. Nil means nobody was asked; see
	// OpImpact for why that is not the same as zero. It goes only to the
	// local approver, like the rest of this struct.
	Impact *OpImpact `json:"impact,omitempty"`
}

// Approval kinds. The prompt is one screen but not one question: a write is
// asked about before it lands, a command before it runs, a disclosure before
// data leaves, a delegation before an agent starts working unattended. Which
// one it is decides what the screen has to show and what its buttons can
// honestly say, so the request states it rather than letting the UI guess
// from which fields happen to be populated.
const (
	KindWrite      = "write"
	KindCommand    = "command"
	KindDisclosure = "disclosure"
	KindDelegation = "delegation"
	// KindProxy is a call Fylane forwards to an MCP server installed on this
	// machine. It is its own question because none of the other four
	// can be asked honestly about it: there is no diff, the rule table never
	// saw the operation, and unlike a delegation the work is not handed to an
	// agent working on the user's behalf — it is handed to a program whose
	// effect nobody here can name.
	KindProxy = "proxy"
)

// ApprovalRequest is handed to the Approver before any disk change.
type ApprovalRequest struct {
	ChangeSetID   string      `json:"change_set_id"`
	WorkspaceID   string      `json:"workspace_id"`
	WorkspaceName string      `json:"workspace_name"`
	Provider      string      `json:"provider"`
	Summary       string      `json:"summary"`
	Operations    []OpPreview `json:"operations"`
	// Kind is one of the constants above. An empty value is read as
	// KindWrite so a request built before this field existed still renders.
	Kind string `json:"kind,omitempty"`
	// MustAsk is carried from the request: the user asked for this kind of
	// file to always stop here.
	MustAsk bool `json:"must_ask,omitempty"`
	// Command is the argv of a command awaiting approval. It is set
	// instead of Operations: a command has no diff to show, so the desktop
	// prompt renders the argv and Rule instead.
	Command []string `json:"command,omitempty"`
	// Rule is the cmdrule identifier that asked for this confirmation.
	Rule string `json:"rule,omitempty"`
	// Reason is that rule's own sentence about why it stopped this command.
	// The desktop prompt used to show neither, which left the user deciding
	// on an argv alone — the rule table knows why it objected and that is
	// the part worth reading.
	Reason string `json:"reason,omitempty"`
	// Dir is the workspace-relative working directory of a command.
	Dir string `json:"dir,omitempty"`
	// Network is what this run gets from the outbound boundary, as one of
	// readbox.Reach's words. It is a statement, not a second question: the
	// prompt already asks whether the command may run, and asking "and may it
	// use the network" in the same breath is the merge the approval rules forbid — while
	// nothing here can work out in advance whether the command needs it.
	Network string `json:"network,omitempty"`
	// Grant marks the one-time question that authorizes commands in this
	// workspace rather than one particular command. The desktop
	// prompt has to say which of the two it is asking.
	Grant bool `json:"grant,omitempty"`
}

// Decision is the approver's verdict.
type Decision struct {
	Approved bool
	// Pending means no decision was reached within the blocking budget.
	// The change set stays pending; retrying with the same change_set_id
	// re-attaches to the same approval. Pending is never a denial.
	Pending bool
	// Reason is surfaced to the caller when not approved (e.g.
	// "user_rejected", "approval_timeout").
	Reason string
}

// Approver is the local approval authority (local approval is the
// ONLY final authority). Implementations block until the user decides or a
// budget expires; there is no bypass.
type Approver interface {
	Approve(ctx context.Context, req *ApprovalRequest) (Decision, error)
}
