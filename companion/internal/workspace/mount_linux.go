//go:build linux

package workspace

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// smb2Magic has no name in x/sys/unix. Some kernels report it for smb2/smb3
// mounts instead of CIFS_SUPER_MAGIC, and leaving it out would silently
// accept one of the mounts this check exists to refuse.
const smb2Magic = 0xfe534d42

// nonLocalFilesystems maps a statfs magic to the name shown in the error.
// Everything listed is served either over the network or by a userspace
// process — FUSE is where sshfs, rclone mount and s3fs land, and it is the
// entry that matters most here.
var nonLocalFilesystems = map[int64]string{
	unix.NFS_SUPER_MAGIC:  "nfs",
	unix.SMB_SUPER_MAGIC:  "smbfs",
	unix.CIFS_SUPER_MAGIC: "cifs/smb",
	smb2Magic:             "smb2",
	unix.FUSE_SUPER_MAGIC: "fuse (sshfs, rclone mount, …)",
	unix.V9FS_MAGIC:       "9p",
	unix.CEPH_SUPER_MAGIC: "ceph",
	unix.AFS_SUPER_MAGIC:  "afs",
	unix.AFS_FS_MAGIC:     "afs",
}

// nonLocalFS names the filesystem kind when path is not on local storage, and
// returns "" when it is.
func nonLocalFS(path string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", fmt.Errorf("statfs %q: %w", path, err)
	}
	return classifyLinux(int64(st.Type)), nil
}

func classifyLinux(magic int64) string { return nonLocalFilesystems[magic] }
