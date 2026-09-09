//go:build windows

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mkfifo is unsupported on Windows; the fifo test skips itself first.
func mkfifo(string) error { return errors.New("mkfifo unsupported on windows") }

// TestResolveRejectsJunctionEscape verifies that an NTFS junction inside the
// workspace cannot redirect operations outside it (junctions).
func TestResolveRejectsJunctionEscape(t *testing.T) {
	root := newRoot(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	junction := filepath.Join(root, "jdir")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v (%s)", err, out)
	}

	for _, rel := range []string{"jdir", "jdir/secret.txt", "jdir/new.txt"} {
		if _, _, err := Resolve(root, rel, OpRead); err == nil {
			t.Errorf("Resolve(%q): junction escape not blocked", rel)
		}
	}
}
