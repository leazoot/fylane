// Package workspace models a local directory exposed to remote MCP callers.
// The absolute root path never leaves this process: remote surfaces see only
// the opaque workspace ID and workspace-relative paths.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/store"
)

// Workspace is a runtime handle for a single sandboxed directory. Its policy
// is fixed at creation; the one mutable part is the ignore-rule cache, which
// is a memo of what is on disk and changes no answer.
type Workspace struct {
	id             string
	name           string
	root           string
	writable       bool
	excludeRules   []string
	sensitiveRules []string
	ignore         *ignoreCache
	// network is whether the programs started for this workspace may reach
	// the network. Ad-hoc workspaces (New) allow it: that is the stored
	// default and the behaviour every workspace had before BL-8.
	network bool
}

// New validates root and returns an ad-hoc Workspace for it with the default
// rules and an ID derived from the path. Used by tooling that runs without a
// database; persisted workspaces go through FromRecord.
func New(root string) (*Workspace, error) {
	real, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(real))
	return &Workspace{
		id:             "ws_" + hex.EncodeToString(sum[:6]),
		name:           filepath.Base(real),
		root:           real,
		writable:       true,
		excludeRules:   DefaultExcludeRules(),
		sensitiveRules: DefaultSensitiveRules(),
		ignore:         newIgnoreCache(real),
	}, nil
}

// FromRecord builds a runtime handle from a persisted workspace record,
// re-validating that the root still exists.
func FromRecord(rec *store.Workspace) (*Workspace, error) {
	real, err := resolveRoot(rec.RootPath)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", rec.ID, err)
	}
	return &Workspace{
		id:             rec.ID,
		name:           rec.Name,
		root:           real,
		writable:       rec.Mode == store.ModeReadWrite,
		excludeRules:   rec.ExcludeRules,
		sensitiveRules: rec.SensitiveRules,
		ignore:         newIgnoreCache(real),
		network:        rec.Network != store.NetworkDeny,
	}, nil
}

// resolveRoot converts root to a symlink-resolved absolute directory path so
// later escape checks compare against the real location.
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving workspace root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving workspace root: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("checking workspace root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root %q is not a directory", root)
	}
	// Asked here rather than only when a workspace is added, so a root that
	// was local at registration and is a network mount today is refused on
	// the next open instead of inheriting an answer from months ago.
	if err := checkLocalRoot(real); err != nil {
		return "", err
	}
	return real, nil
}

// ID returns the opaque identifier safe to expose to remote callers.
func (w *Workspace) ID() string { return w.id }

// Name returns the display name (root directory base name).
func (w *Workspace) Name() string { return w.name }

// Root returns the absolute root path. Local use only — must never be
// serialized into MCP responses, Relay traffic, or logs that leave the
// machine.
func (w *Workspace) Root() string { return w.root }

// Writable reports whether write operations are allowed (workspace mode is
// read_write).
func (w *Workspace) Writable() bool { return w.writable }

// Network reports whether the programs started for this workspace may reach
// the network. It is what the workspace asks for; what the machine can
// actually deny is the kernel's separate answer (readbox.Box.Network).
func (w *Workspace) Network() bool { return w.network }

// Excluded reports whether the canonical relative path is hidden from
// listing, search, and read.
//
// Two independent sources say yes: the workspace's own exclude rules, and the
// repository's .gitignore files. They are OR'd rather than merged, and that
// is not an optimization — it is what keeps a "!" line in a .gitignore from
// reaching the workspace's rules. Ignore files are ordinary workspace
// content, so what they are trusted to do is hide more, never less.
//
// isDir separates a "build/" rule from a file named "build"; both callers
// that walk a directory already know the answer, and a read is asking about
// a file.
func (w *Workspace) Excluded(rel string, isDir bool) bool {
	return MatchesAny(w.excludeRules, rel) || w.ignore.ignored(rel, isDir)
}

// Sensitive reports whether the canonical relative path matches the
// workspace's sensitive-file rules.
func (w *Workspace) Sensitive(rel string) bool {
	return MatchesAny(w.sensitiveRules, rel)
}

// Resolve validates rel through the path sandbox (lexical and filesystem
// checks for the given operation) and returns the absolute path inside the
// workspace along with the canonical relative form.
func (w *Workspace) Resolve(rel string, op sandbox.Op) (abs string, canonical string, err error) {
	return sandbox.Resolve(w.root, rel, op)
}
