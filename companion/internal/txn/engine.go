package txn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-udiff"

	"github.com/leazoot/fylane/companion/internal/textenc"

	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

const (
	// maxOps bounds one change set.
	maxOps = 100
	// maxOpContentBytes bounds a single operation's content.
	maxOpContentBytes = 16 << 20
	// rollbackRetention is how long backups stay rollbackable.
	rollbackRetention = 7 * 24 * time.Hour
	// binaryDiffLabel replaces diffs of binary content in previews.
	binaryDiffLabel = "(binary change)"
)

// Engine executes change sets against workspaces. All fields are required.
type Engine struct {
	Store *store.Store
	// BackupRoot is the local backup/recycle area; per-change-set
	// subdirectories are created inside it. Never inside a workspace.
	BackupRoot string
	// Approver is the local approval authority. There is no configuration
	// that skips it.
	Approver Approver
	// MaxBackupBytes overrides the backup-area size budget; 0 means the
	// 1 GiB default.
	MaxBackupBytes int64
	// Impact answers how far a change reaches, for the approval face. Nil
	// leaves every preview without the line, which is a complete answer:
	// nobody asked.
	Impact Impacter

	mu       sync.Mutex
	inflight map[string]bool
}

// Execute runs the 14-step transaction for req against ws. It returns a
// Result for conflict/denied/failed/applied outcomes; error is reserved for
// invalid requests and internal failures with no disk effect.
func (e *Engine) Execute(ctx context.Context, ws *workspace.Workspace, req Request) (*Result, error) {
	if e.Store == nil || e.BackupRoot == "" || e.Approver == nil {
		return nil, fmt.Errorf("txn engine is not fully configured")
	}
	// Step 1: workspace must be writable (its status was already checked
	// when the caller opened it).
	if !ws.Writable() {
		return nil, fmt.Errorf("workspace %s is read-only", ws.ID())
	}
	if len(req.Operations) == 0 {
		return nil, fmt.Errorf("change set has no operations")
	}
	if len(req.Operations) > maxOps {
		return nil, fmt.Errorf("change set exceeds %d operations; split it", maxOps)
	}
	if req.ChangeSetID == "" {
		req.ChangeSetID = store.NewID("chg")
	}

	// Idempotency: a retried change set must not apply twice (backend rule).
	// A retry of a pending change set resumes it instead.
	done, resume, err := e.claim(ctx, req.ChangeSetID)
	if err != nil || done != nil {
		return done, err
	}
	defer e.release(req.ChangeSetID)

	started := time.Now()

	opsJSON, err := json.Marshal(req.Operations)
	if err != nil {
		return nil, fmt.Errorf("encoding operations: %w", err)
	}
	if resume != nil && !bytes.Equal(normalizeJSON(resume.Operations), normalizeJSON(opsJSON)) {
		return nil, fmt.Errorf("change set %s is pending with different operations; use a new change_set_id", req.ChangeSetID)
	}

	// Steps 2-8: validate, hash-check, and preview every operation before
	// touching anything. Resumed change sets re-validate from the current
	// disk state — a file changed since the original request surfaces as a
	// conflict here.
	plan, conflict, err := e.plan(ws, req.Operations)
	if err != nil {
		return nil, err
	}
	if conflict != nil {
		if resume != nil {
			e.finish(resume, store.ChangeSetFailed, started, "conflict on retry: "+conflict.Reason)
		}
		return &Result{Status: StatusConflict, Conflict: conflict}, nil
	}

	rec := resume
	if rec == nil {
		rec = &store.ChangeSet{
			ID:           req.ChangeSetID,
			WorkspaceID:  ws.ID(),
			Provider:     req.Provider,
			Summary:      req.Summary,
			Operations:   opsJSON,
			Status:       store.ChangeSetPending,
			BeforeHashes: beforeHashes(plan),
		}
		if err := e.Store.CreateChangeSet(ctx, rec); err != nil {
			return nil, err
		}
	}

	// Still step 8: how far each change reaches, for the face that is about
	// to ask. It runs after the change set is recorded and before the
	// question, so a slow answer costs the prompt and never the journal.
	attachImpact(ctx, e.Impact, ws, plan)
	// And how much a recursive delete takes, for the same face and on the
	// same terms. The recycle limit is read here rather than inside the
	// measurement: what fits is a property of this engine's configuration,
	// not of the tree.
	attachTrees(ctx, plan)
	for _, p := range plan {
		p.preview.BeyondUndo = p.preview.Tree.BeyondUndo(e.treeLimit())
	}

	// Step 9: approval. Local approval is the only final authority
	//.
	decision, err := e.Approver.Approve(ctx, approvalRequest(ws, req, plan))
	if err != nil {
		e.finish(rec, store.ChangeSetFailed, started, "approval error")
		return nil, fmt.Errorf("approval: %w", err)
	}
	if decision.Pending {
		// No decision within the blocking budget: the change set stays
		// pending (never auto-denied), and the caller retries with the same
		// change_set_id to pick the decision up.
		return &Result{Status: StatusPending, ChangeSetID: rec.ID,
			Reason: "waiting for local approval; retry this call with the same change_set_id"}, nil
	}
	if !decision.Approved {
		reason := decision.Reason
		if reason == "" {
			reason = "user_rejected"
		}
		e.finish(rec, store.ChangeSetDenied, started, reason)
		return &Result{Status: StatusDenied, ChangeSetID: rec.ID, Reason: reason}, nil
	}
	rec.Status = store.ChangeSetApproved
	rec.ApprovedAt = time.Now().UTC()
	if err := e.Store.UpdateChangeSet(ctx, rec); err != nil {
		return nil, err
	}

	// Step 10: durable backups before any disk mutation.
	backupDir := filepath.Join(e.BackupRoot, rec.ID)
	if err := e.backup(plan, backupDir); err != nil {
		os.RemoveAll(backupDir)
		e.finish(rec, store.ChangeSetFailed, started, "backup failed")
		return &Result{Status: StatusFailed, ChangeSetID: rec.ID,
			Reason:  fmt.Sprintf("backup failed: %v", err),
			Effects: notStartedEffects(plan)}, nil
	}
	rec.BackupLocation = rec.ID

	// Steps 11-12: atomic apply with re-verification; any failure restores
	// the already-applied operations.
	if applyErr := e.apply(plan, backupDir); applyErr != nil {
		e.finish(rec, store.ChangeSetFailed, started, applyErr.Error())
		// apply has already run whatever undo it could. What the caller
		// needs now is not the error but the world, and the engine is the
		// one holding the before hashes, the after hashes and the backup —
		// so it reads the disk and says, per path, which of the three
		// happened instead of handing the question back.
		return &Result{Status: StatusFailed, ChangeSetID: rec.ID, Reason: applyErr.Error(),
			Effects: reconcile(plan)}, nil
	}

	// Step 13: journal.
	now := time.Now().UTC()
	rec.Status = store.ChangeSetApplied
	rec.AfterHashes = afterHashes(plan)
	rec.AppliedAt = now
	rec.RollbackDeadline = now.Add(rollbackRetention)
	if err := e.Store.UpdateChangeSet(context.WithoutCancel(ctx), rec); err != nil {
		return nil, fmt.Errorf("journaling change set: %w", err)
	}
	e.audit(rec, plan, started)

	// Step 14: result.
	res := &Result{
		Status:                 StatusApplied,
		ChangeSetID:            rec.ID,
		RollbackAvailableUntil: rec.RollbackDeadline,
	}
	for _, p := range plan {
		res.Operations = append(res.Operations, OpResult{Path: p.resultPath(), Status: p.status, SHA256: p.afterSHA})
	}
	return res, nil
}

// claim reserves the change set ID. It returns a stored terminal result for
// retries of an already-finished change set, or the existing record when a
// pending change set is being resumed.
func (e *Engine) claim(ctx context.Context, id string) (*Result, *store.ChangeSet, error) {
	e.mu.Lock()
	if e.inflight == nil {
		e.inflight = make(map[string]bool)
	}
	if e.inflight[id] {
		e.mu.Unlock()
		return nil, nil, fmt.Errorf("change set %s is already executing", id)
	}
	e.inflight[id] = true
	e.mu.Unlock()

	rec, err := e.Store.GetChangeSet(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		e.release(id)
		return nil, nil, err
	}
	switch rec.Status {
	case store.ChangeSetPending:
		// Resume: the caller keeps the claim until Execute finishes.
		return nil, rec, nil
	case store.ChangeSetApplied:
		e.release(id)
		res := &Result{Status: StatusApplied, ChangeSetID: rec.ID,
			RollbackAvailableUntil: rec.RollbackDeadline, Replayed: true}
		var ops []Operation
		if err := json.Unmarshal(rec.Operations, &ops); err == nil {
			for _, op := range ops {
				target := op.Path
				status := op.Type + "d"
				if op.Type == OpMove {
					target, status = op.To, "moved"
				}
				res.Operations = append(res.Operations, OpResult{Path: target, Status: status, SHA256: rec.AfterHashes[target]})
			}
		}
		return res, nil, nil
	case store.ChangeSetDenied:
		e.release(id)
		return &Result{Status: StatusDenied, ChangeSetID: rec.ID, Reason: "user_rejected", Replayed: true}, nil, nil
	default:
		e.release(id)
		return &Result{Status: StatusFailed, ChangeSetID: rec.ID, Replayed: true,
			Reason: fmt.Sprintf("change set already finished with status %s; use a new change_set_id", rec.Status)}, nil, nil
	}
}

// normalizeJSON re-marshals raw JSON so semantically equal operation lists
// compare equal regardless of field ordering or whitespace.
func normalizeJSON(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

func (e *Engine) release(id string) {
	e.mu.Lock()
	delete(e.inflight, id)
	e.mu.Unlock()
}

// finish records a terminal status and audit trail for a change set that did
// not apply.
func (e *Engine) finish(rec *store.ChangeSet, status string, started time.Time, reason string) {
	ctx := context.Background()
	rec.Status = status
	if err := e.Store.UpdateChangeSet(ctx, rec); err != nil {
		return
	}
	e.Store.AppendAuditEvent(ctx, &store.AuditEvent{
		ChangeSetID: rec.ID,
		EventType:   "change_set_" + status,
		Result:      reason,
		DurationMS:  time.Since(started).Milliseconds(),
	})
}

func (e *Engine) audit(rec *store.ChangeSet, plan []*plannedOp, started time.Time) {
	ctx := context.Background()
	for _, p := range plan {
		e.Store.AppendAuditEvent(ctx, &store.AuditEvent{
			ChangeSetID:  rec.ID,
			EventType:    p.op.Type,
			PathRelative: p.resultPath(),
			Result:       "ok",
		})
	}
	e.Store.AppendAuditEvent(ctx, &store.AuditEvent{
		ChangeSetID: rec.ID,
		EventType:   "change_set_applied",
		Result:      "ok",
		DurationMS:  time.Since(started).Milliseconds(),
	})
}

// plannedOp is one fully validated operation.
type plannedOp struct {
	op          Operation
	canonical   string // primary path (from-path for moves)
	toCanonical string // move target
	abs, toAbs  string
	oldData     []byte
	oldMode     os.FileMode
	isDir       bool
	beforeSHA   string
	afterSHA    string
	status      string // created | updated | moved | deleted
	preview     OpPreview
}

func (p *plannedOp) resultPath() string {
	if p.op.Type == OpMove {
		return p.toCanonical
	}
	return p.canonical
}

// plan validates every operation (steps 2-6) and builds previews and diffs
// (steps 7-8, in memory). It touches nothing on disk.
func (e *Engine) plan(ws *workspace.Workspace, ops []Operation) ([]*plannedOp, *Conflict, error) {
	seen := map[string]bool{}
	claim := func(canonical string) error {
		if seen[canonical] {
			return fmt.Errorf("path %s appears in more than one operation; one change set may touch each path once", canonical)
		}
		seen[canonical] = true
		return nil
	}

	var plan []*plannedOp
	for i, op := range ops {
		if len(op.Content) > maxOpContentBytes {
			return nil, nil, fmt.Errorf("operation %d: content exceeds %d MiB", i, maxOpContentBytes>>20)
		}
		p := &plannedOp{op: op}
		var conflict *Conflict
		var err error
		switch op.Type {
		case OpCreate:
			conflict, err = e.planCreate(ws, p)
		case OpUpdate:
			conflict, err = e.planUpdate(ws, p)
		case OpMove:
			conflict, err = e.planMove(ws, p)
		case OpDelete:
			conflict, err = e.planDelete(ws, p)
		default:
			return nil, nil, fmt.Errorf("operation %d: unknown type %q", i, op.Type)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("operation %d (%s): %w", i, op.Type, err)
		}
		if conflict != nil {
			return nil, conflict, nil
		}
		if err := claim(p.canonical); err != nil {
			return nil, nil, err
		}
		if p.toCanonical != "" {
			if err := claim(p.toCanonical); err != nil {
				return nil, nil, err
			}
		}
		plan = append(plan, p)
	}
	return plan, nil, nil
}

func (e *Engine) planCreate(ws *workspace.Workspace, p *plannedOp) (*Conflict, error) {
	abs, canonical, err := ws.Resolve(p.op.Path, sandbox.OpWrite)
	if err != nil {
		return nil, err
	}
	p.abs, p.canonical = abs, canonical
	if info, err := os.Lstat(abs); err == nil {
		current := ""
		if info.Mode().IsRegular() {
			current = hashFile(abs)
		}
		return &Conflict{Path: canonical, Reason: "already_exists", CurrentSHA256: current,
			Action: "the path already exists; use update with its sha256, or choose another path"}, nil
	}
	p.afterSHA = hashBytes([]byte(p.op.Content))
	p.status = "created"
	p.preview = OpPreview{Type: OpCreate, Path: canonical,
		Diff:      diffText(canonical, "", p.op.Content),
		Sensitive: ws.Sensitive(canonical)}
	return nil, nil
}

func (e *Engine) planUpdate(ws *workspace.Workspace, p *plannedOp) (*Conflict, error) {
	abs, canonical, err := ws.Resolve(p.op.Path, sandbox.OpWrite)
	if err != nil {
		return nil, err
	}
	p.abs, p.canonical = abs, canonical
	conflict, err := loadBase(p, abs, canonical)
	if conflict != nil || err != nil {
		return conflict, err
	}
	p.afterSHA = hashBytes([]byte(p.op.Content))
	p.status = "updated"
	p.preview = OpPreview{Type: OpUpdate, Path: canonical,
		Diff:      diffText(canonical, string(p.oldData), p.op.Content),
		Sensitive: ws.Sensitive(canonical)}
	return nil, nil
}

func (e *Engine) planMove(ws *workspace.Workspace, p *plannedOp) (*Conflict, error) {
	abs, canonical, err := ws.Resolve(p.op.From, sandbox.OpDelete)
	if err != nil {
		return nil, err
	}
	toAbs, toCanonical, err := ws.Resolve(p.op.To, sandbox.OpWrite)
	if err != nil {
		return nil, err
	}
	p.abs, p.canonical = abs, canonical
	p.toAbs, p.toCanonical = toAbs, toCanonical

	conflict, err := loadBase(p, abs, canonical)
	if conflict != nil || err != nil {
		return conflict, err
	}
	if toInfo, err := os.Lstat(toAbs); err == nil {
		// Renaming only the letter case of a name is legal on
		// case-insensitive filesystems: source and target are the same file.
		fromInfo, statErr := os.Lstat(abs)
		if statErr != nil || !os.SameFile(fromInfo, toInfo) {
			return &Conflict{Path: toCanonical, Reason: "target_exists",
				Action: "the move target already exists; choose another name or delete it first"}, nil
		}
	}
	p.afterSHA = p.beforeSHA
	p.status = "moved"
	p.preview = OpPreview{Type: OpMove, Path: canonical, To: toCanonical,
		Sensitive: ws.Sensitive(canonical) || ws.Sensitive(toCanonical)}
	return nil, nil
}

func (e *Engine) planDelete(ws *workspace.Workspace, p *plannedOp) (*Conflict, error) {
	abs, canonical, err := ws.Resolve(p.op.Path, sandbox.OpDelete)
	if err != nil {
		return nil, err
	}
	p.abs, p.canonical = abs, canonical
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return &Conflict{Path: canonical, Reason: "target_missing",
			Action: "the path does not exist; refresh the directory listing"}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		p.isDir = true
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 && !p.op.Recursive {
			return nil, fmt.Errorf("%s is a non-empty directory; set recursive=true to delete it", canonical)
		}
		p.status = "deleted"
		p.preview = OpPreview{Type: OpDelete, Path: canonical,
			RecursiveDelete: len(entries) > 0, Sensitive: ws.Sensitive(canonical)}
		return nil, nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file or directory", canonical)
	}
	conflict, err := loadBase(p, abs, canonical)
	if conflict != nil || err != nil {
		return conflict, err
	}
	p.status = "deleted"
	p.preview = OpPreview{Type: OpDelete, Path: canonical,
		Diff:      diffText(canonical, string(p.oldData), ""),
		Sensitive: ws.Sensitive(canonical)}
	return nil, nil
}

// loadBase loads the current file for update/move/delete and enforces the
// base-hash contract (step 6, conflict shapes).
func loadBase(p *plannedOp, abs, canonical string) (*Conflict, error) {
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return &Conflict{Path: canonical, Reason: "target_missing",
			Action: "the file does not exist; use create instead"}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", canonical)
	}
	if info.Size() > maxOpContentBytes {
		return nil, fmt.Errorf("%s exceeds the %d MiB transaction limit", canonical, maxOpContentBytes>>20)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	p.oldData = data
	p.oldMode = info.Mode().Perm()
	p.beforeSHA = hashBytes(data)
	if p.op.ExpectedSHA256 == "" {
		return &Conflict{Path: canonical, Reason: "expected_sha256_missing", CurrentSHA256: p.beforeSHA,
			Action: "read the file and pass its sha256 as expected_sha256"}, nil
	}
	if !strings.EqualFold(p.op.ExpectedSHA256, p.beforeSHA) {
		return &Conflict{Path: canonical, Reason: "base_hash_mismatch", CurrentSHA256: p.beforeSHA,
			Action: "read the latest file and regenerate the change"}, nil
	}
	return nil, nil
}

// backup copies every pre-image into backupDir (step 10). Files land under
// files/<canonical>; deleted directory trees under files/<canonical>/...
func (e *Engine) backup(plan []*plannedOp, backupDir string) error {
	for _, p := range plan {
		switch {
		case p.op.Type == OpCreate:
			continue
		case p.isDir:
			if err := copyTreeDurable(p.abs, filepath.Join(backupDir, "files", filepath.FromSlash(p.canonical))); err != nil {
				return err
			}
		default:
			if err := copyFileDurable(p.abs, filepath.Join(backupDir, "files", filepath.FromSlash(p.canonical))); err != nil {
				return err
			}
		}
	}
	return nil
}

// apply mutates the disk (step 11) and verifies the outcome (step 12). On
// any failure it restores every already-applied operation in reverse order.
func (e *Engine) apply(plan []*plannedOp, backupDir string) (err error) {
	var undo []func() error
	defer func() {
		if err == nil {
			return
		}
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i](); uerr != nil {
				err = fmt.Errorf("%w (restore also failed: %v)", err, uerr)
			}
		}
	}()

	for _, p := range plan {
		p := p
		switch p.op.Type {
		case OpCreate:
			if _, statErr := os.Lstat(p.abs); statErr == nil {
				return fmt.Errorf("%s appeared while awaiting approval; re-plan the change", p.canonical)
			}
			if werr := AtomicWrite(p.abs, []byte(p.op.Content), 0o644); werr != nil {
				return fmt.Errorf("creating %s: %w", p.canonical, werr)
			}
			undo = append(undo, func() error { return os.Remove(p.abs) })
		case OpUpdate:
			if cerr := stillAtBase(p); cerr != nil {
				return cerr
			}
			if werr := AtomicWrite(p.abs, []byte(p.op.Content), p.oldMode); werr != nil {
				return fmt.Errorf("updating %s: %w", p.canonical, werr)
			}
			undo = append(undo, func() error { return AtomicWrite(p.abs, p.oldData, p.oldMode) })
		case OpMove:
			if cerr := stillAtBase(p); cerr != nil {
				return cerr
			}
			if merr := os.Rename(p.abs, p.toAbs); merr != nil {
				if strings.Contains(merr.Error(), "cross-device") {
					return fmt.Errorf("moving %s: cross-volume moves are not supported", p.canonical)
				}
				return fmt.Errorf("moving %s: %w", p.canonical, merr)
			}
			undo = append(undo, func() error { return os.Rename(p.toAbs, p.abs) })
		case OpDelete:
			if p.isDir {
				if derr := os.RemoveAll(p.abs); derr != nil {
					return fmt.Errorf("deleting %s: %w", p.canonical, derr)
				}
				backupPath := filepath.Join(backupDir, "files", filepath.FromSlash(p.canonical))
				undo = append(undo, func() error { return copyTreeDurable(backupPath, p.abs) })
			} else {
				if cerr := stillAtBase(p); cerr != nil {
					return cerr
				}
				if derr := os.Remove(p.abs); derr != nil {
					return fmt.Errorf("deleting %s: %w", p.canonical, derr)
				}
				undo = append(undo, func() error { return AtomicWrite(p.abs, p.oldData, p.oldMode) })
			}
		}
	}

	// Step 12: verify the new state before declaring success.
	for _, p := range plan {
		switch p.op.Type {
		case OpCreate, OpUpdate:
			if got := hashFile(p.abs); got != p.afterSHA {
				return fmt.Errorf("verification failed for %s after write", p.canonical)
			}
		case OpMove:
			if got := hashFile(p.toAbs); got != p.afterSHA {
				return fmt.Errorf("verification failed for %s after move", p.toCanonical)
			}
		case OpDelete:
			if _, statErr := os.Lstat(p.abs); !os.IsNotExist(statErr) {
				return fmt.Errorf("verification failed: %s still exists after delete", p.canonical)
			}
		}
	}
	return nil
}

// stillAtBase re-checks the base hash immediately before mutating (the file
// may have changed while approval was pending).
func stillAtBase(p *plannedOp) error {
	if got := hashFile(p.abs); !strings.EqualFold(got, p.beforeSHA) {
		return fmt.Errorf("%s changed while awaiting approval; read it again and re-plan the change", p.canonical)
	}
	return nil
}

func approvalRequest(ws *workspace.Workspace, req Request, plan []*plannedOp) *ApprovalRequest {
	ar := &ApprovalRequest{
		ChangeSetID:   req.ChangeSetID,
		WorkspaceID:   ws.ID(),
		WorkspaceName: ws.Name(),
		Provider:      req.Provider,
		Summary:       req.Summary,
		Kind:          KindWrite,
		MustAsk:       req.MustAsk,
	}
	for _, p := range plan {
		ar.Operations = append(ar.Operations, p.preview)
	}
	return ar
}

func beforeHashes(plan []*plannedOp) map[string]string {
	m := map[string]string{}
	for _, p := range plan {
		if p.beforeSHA != "" {
			m[p.canonical] = p.beforeSHA
		}
	}
	return m
}

func afterHashes(plan []*plannedOp) map[string]string {
	m := map[string]string{}
	for _, p := range plan {
		if p.afterSHA != "" {
			m[p.resultPath()] = p.afterSHA
		}
	}
	return m
}

// diffText builds a unified diff preview, replacing binary content with a
// label — raw binary bytes never reach the approval UI or logs. Encoded text
// (UTF-16/GBK) is decoded first so the approval UI shows readable content
// instead of raw bytes.
func diffText(path, old, new string) string {
	oldText, _, oldOK := textenc.DetectDecode([]byte(old))
	newText, _, newOK := textenc.DetectDecode([]byte(new))
	if !oldOK || !newOK {
		return binaryDiffLabel
	}
	oldLabel, newLabel := "a/"+path, "b/"+path
	if old == "" {
		oldLabel = "/dev/null"
	}
	if new == "" {
		newLabel = "/dev/null"
	}
	return udiff.Unified(oldLabel, newLabel, oldText, newText)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFile returns the hex SHA-256 of the file, or "" when unreadable.
func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashBytes(data)
}
