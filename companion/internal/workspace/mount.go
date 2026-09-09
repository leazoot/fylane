package workspace

import (
	"errors"
	"fmt"
)

// ErrRemoteRoot is returned when a workspace root does not sit on a local
// filesystem.
//
// The write transaction makes two promises that the local kernel
// keeps on its behalf and a remote one does not. Step 11 replaces a file with
// os.Rename, which is atomic only within a single local filesystem — txn
// already refuses cross-device moves outright for the same reason. Steps 3-4
// resolve the real path and reject symlink escapes with Lstat and
// EvalSymlinks, and on a network or FUSE mount those calls describe what the
// far end chose to say a link points at, not what the sandbox verified.
//
// Refusing the root is blunt on purpose. The alternative — accept it and
// quietly run a transaction that no longer guarantees what it claims — is the
// one outcome security.md rules out. Loosening this (an opt-in that says "I
// know what this mount is") would be a decision, not a config flag.
var ErrRemoteRoot = errors.New("workspace root is not on a local filesystem")

// checkLocalRoot rejects a workspace root on a network or FUSE filesystem.
// real must already be absolute and symlink-resolved.
func checkLocalRoot(real string) error {
	kind, err := nonLocalFS(real)
	if err != nil {
		return fmt.Errorf("checking workspace root filesystem: %w", err)
	}
	if kind == "" {
		return nil
	}
	return fmt.Errorf("%w: %q is on %s, where the atomic replace and symlink "+
		"resolution the write transaction depends on are not guaranteed", ErrRemoteRoot, real, kind)
}
