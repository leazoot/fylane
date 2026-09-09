package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Op is the kind of filesystem operation a path is being resolved for.
// Write-class operations get additional protections (e.g. `.git`).
type Op int

const (
	OpRead Op = iota
	OpWrite
	OpDelete
)

// maxAbsPathLen bounds the joined absolute path. Kept under the classic
// Windows MAX_PATH-extended and POSIX PATH_MAX limits.
const maxAbsPathLen = 4096

// Resolve validates rel against the workspace root at both the lexical and
// the filesystem level, and returns the absolute target path plus the
// canonical relative form.
//
// root must already be an absolute, symlink-resolved directory path (as
// produced by workspace.New). On top of CleanPath, Resolve walks every
// existing path component with Lstat so that no symbolic link or junction is
// followed (a real-path check at every level), rejects device and other
// special files, and blocks write-class operations on `.git`.
//
// Components that do not exist yet are permitted — they cannot redirect the
// path, and writes create them fresh under the validated parent.
func Resolve(root, rel string, op Op) (string, string, error) {
	canonical, err := CleanPath(rel)
	if err != nil {
		return "", "", err
	}

	comps := strings.Split(canonical, "/")
	if op != OpRead {
		for _, c := range comps {
			if strings.EqualFold(c, ".git") {
				return "", "", fmt.Errorf("%w: %s is under .git", ErrProtectedPath, canonical)
			}
		}
	}

	abs := filepath.Join(root, filepath.FromSlash(canonical))
	if len(abs) > maxAbsPathLen {
		return "", "", ErrPathTooLong
	}

	cur := root
	for i, c := range comps {
		cur = filepath.Join(cur, c)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				break
			}
			return "", "", fmt.Errorf("inspecting path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("%w: %s", ErrSymlink, strings.Join(comps[:i+1], "/"))
		}
		if i < len(comps)-1 {
			if !info.IsDir() {
				return "", "", fmt.Errorf("%w: %s", ErrNotDirectory, strings.Join(comps[:i+1], "/"))
			}
			continue
		}
		if info.Mode()&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeIrregular) != 0 {
			return "", "", fmt.Errorf("%w: %s", ErrSpecialFile, canonical)
		}
	}

	// Defense in depth: independently confirm that the deepest existing
	// ancestor really lives under root once all links are resolved.
	if err := verifyRealPath(root, abs); err != nil {
		return "", "", err
	}
	return abs, canonical, nil
}

// verifyRealPath resolves the deepest existing ancestor of abs and checks it
// is still inside root.
func verifyRealPath(root, abs string) error {
	p := abs
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			sep := string(filepath.Separator)
			if real != root && !strings.HasPrefix(real, root+sep) {
				return ErrPathTraversal
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("resolving real path: %w", err)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ErrPathTraversal
		}
		p = parent
	}
}
