package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newRoot returns a symlink-resolved temp workspace root, matching what
// workspace.New feeds into Resolve (on macOS /tmp itself is a symlink).
func newRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAcceptsNormalPaths(t *testing.T) {
	root := newRoot(t)
	mustWrite(t, filepath.Join(root, "src", "main.go"), "package main")

	cases := []string{
		"src/main.go",         // existing file
		"src/new.go",          // new file in existing dir
		"brand/new/dir/x.txt", // nothing exists yet
	}
	for _, rel := range cases {
		abs, canonical, err := Resolve(root, rel, OpWrite)
		if err != nil {
			t.Errorf("Resolve(%q): unexpected error %v", rel, err)
			continue
		}
		if canonical != rel {
			t.Errorf("Resolve(%q) canonical = %q", rel, canonical)
		}
		if want := filepath.Join(root, filepath.FromSlash(rel)); abs != want {
			t.Errorf("Resolve(%q) abs = %q, want %q", rel, abs, want)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := newRoot(t)
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")

	// A symlinked file pointing outside the workspace.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	// A symlinked directory pointing outside the workspace.
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing back inside the workspace: still rejected — links
	// are never followed, regardless of target.
	mustWrite(t, filepath.Join(root, "real.txt"), "data")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "inner-link.txt")); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"link.txt", "linkdir/secret.txt", "linkdir/new.txt", "inner-link.txt"} {
		for _, op := range []Op{OpRead, OpWrite, OpDelete} {
			if _, _, err := Resolve(root, rel, op); !errors.Is(err, ErrSymlink) {
				t.Errorf("Resolve(%q, op %d) = %v, want ErrSymlink", rel, op, err)
			}
		}
	}
}

func TestResolveRejectsSpecialFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mkfifo not available on windows")
	}
	root := newRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	if _, _, err := Resolve(root, "pipe", OpRead); !errors.Is(err, ErrSpecialFile) {
		t.Errorf("Resolve(pipe) = %v, want ErrSpecialFile", err)
	}
}

func TestResolveRejectsFileAsDirectory(t *testing.T) {
	root := newRoot(t)
	mustWrite(t, filepath.Join(root, "file.txt"), "x")
	if _, _, err := Resolve(root, "file.txt/sub.txt", OpRead); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("Resolve(file.txt/sub.txt) = %v, want ErrNotDirectory", err)
	}
}

func TestResolveProtectsGit(t *testing.T) {
	root := newRoot(t)
	mustWrite(t, filepath.Join(root, ".git", "config"), "[core]")

	for _, rel := range []string{".git/config", ".git", "sub/.git/HEAD", ".GIT/config"} {
		for _, op := range []Op{OpWrite, OpDelete} {
			if _, _, err := Resolve(root, rel, op); !errors.Is(err, ErrProtectedPath) {
				t.Errorf("Resolve(%q, op %d) = %v, want ErrProtectedPath", rel, op, err)
			}
		}
	}
	// Reading repository metadata inside the workspace is allowed.
	if _, _, err := Resolve(root, ".git/config", OpRead); err != nil {
		t.Errorf("Resolve(.git/config, read) = %v, want nil", err)
	}
}

func TestResolveBlocksWorkspaceRootOperations(t *testing.T) {
	root := newRoot(t)
	for _, rel := range []string{".", "./", "a/.."} {
		for _, op := range []Op{OpRead, OpWrite, OpDelete} {
			if _, _, err := Resolve(root, rel, op); !errors.Is(err, ErrWorkspaceRoot) {
				t.Errorf("Resolve(%q, op %d) = %v, want ErrWorkspaceRoot", rel, op, err)
			}
		}
	}
}

func TestResolveRejectsTraversalAndAbsolute(t *testing.T) {
	root := newRoot(t)
	cases := map[string]error{
		"../outside.txt":          ErrPathTraversal,
		"a/../../outside.txt":     ErrPathTraversal,
		"..\\outside.txt":         ErrPathTraversal,
		"/etc/passwd":             ErrAbsolutePath,
		"C:\\Windows\\system.ini": ErrAbsolutePath,
		"\\\\server\\share\\x":    ErrAbsolutePath,
	}
	for rel, want := range cases {
		if _, _, err := Resolve(root, rel, OpRead); !errors.Is(err, want) {
			t.Errorf("Resolve(%q) = %v, want %v", rel, err, want)
		}
	}
}
