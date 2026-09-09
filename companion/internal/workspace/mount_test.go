package workspace

import (
	"testing"
)

// The guard sits in resolveRoot, which every workspace goes through. An
// ordinary local directory must still pass it — if this fails on a developer
// or CI machine, the classifier is over-reaching and that is the bug, not the
// test.
func TestResolveRootAcceptsLocalDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveRoot(dir); err != nil {
		t.Fatalf("resolveRoot(%q) on a local temp dir: %v", dir, err)
	}
}

func TestNonLocalFSReportsLocalDirectoryAsLocal(t *testing.T) {
	kind, err := nonLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("nonLocalFS: %v", err)
	}
	if kind != "" {
		t.Fatalf("local temp dir classified as %q, want local", kind)
	}
}
