package txn

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// tmpPattern is the prefix of staging files created next to write targets.
// Crash recovery removes leftovers matching it.
const tmpPattern = ".fylane-tmp-*"

// AtomicWrite writes data to path via a temp file in the same directory,
// fsyncs it, and renames it into place so readers never observe a partial
// file and a failure never corrupts the original.
// The target's existing permission bits are preserved on replace; mode is
// used when creating a new file.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = "" // renamed; nothing left to clean up
	return nil
}

// copyFileDurable copies src to dst (creating parent directories), preserves
// the permission bits, and fsyncs the destination. Used for backups, which
// must be durable before the final disk commit.
func copyFileDurable(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTreeDurable copies the directory tree at src into dst. Symlinks inside
// the tree are skipped — they are never followed, and restoring one could
// re-introduce an escape.
func copyTreeDurable(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			return nil
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			return copyFileDurable(p, target)
		default:
			return fmt.Errorf("unsupported special file %s", rel)
		}
	})
}
