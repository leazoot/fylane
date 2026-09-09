//go:build darwin

package workspace

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// nonLocalFS names the filesystem kind when path is not on local storage, and
// returns "" when it is.
func nonLocalFS(path string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", fmt.Errorf("statfs %q: %w", path, err)
	}
	return classifyDarwin(unix.ByteSliceToString(st.Fstypename[:]), st.Flags), nil
}

// classifyDarwin asks two questions because neither alone is enough.
// MNT_LOCAL is the kernel's own verdict and covers nfs, smbfs, afpfs and
// webdav without a name list to maintain — but a FUSE volume mounted with
// `-o local` sets it too, and sshfs is exactly the case this check exists
// for. So the fstype name is checked as well.
func classifyDarwin(fstype string, flags uint32) string {
	// osxfuse, macfuse, fusefs.<subtype>
	if strings.Contains(fstype, "fuse") {
		return fstype + " (FUSE)"
	}
	if flags&unix.MNT_LOCAL == 0 {
		return fstype + " (network)"
	}
	return ""
}
