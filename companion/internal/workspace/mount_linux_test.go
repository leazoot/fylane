//go:build linux

package workspace

import (
	"testing"

	"golang.org/x/sys/unix"
)

// A regression guard, and it is meant to read as one: this whole check exists
// because a root on one of these mounts used to be accepted in silence.
// Dropping an entry restores that, so each one is asserted by name.
func TestClassifyLinuxRefusesRemoteMounts(t *testing.T) {
	cases := []struct {
		name  string
		magic int64
	}{
		{"nfs", unix.NFS_SUPER_MAGIC},
		{"smbfs", unix.SMB_SUPER_MAGIC},
		{"cifs", unix.CIFS_SUPER_MAGIC},
		{"smb2", smb2Magic},
		{"fuse (sshfs)", unix.FUSE_SUPER_MAGIC},
		{"9p", unix.V9FS_MAGIC},
		{"ceph", unix.CEPH_SUPER_MAGIC},
		{"afs", unix.AFS_SUPER_MAGIC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if classifyLinux(tc.magic) == "" {
				t.Fatalf("magic %#x classified as local", tc.magic)
			}
		})
	}
}

// Local filesystems must not be swept up. tmpfs matters in particular:
// t.TempDir() lands on it on many machines, so a false positive here would
// break every workspace test at once.
func TestClassifyLinuxAcceptsLocalFilesystems(t *testing.T) {
	for name, magic := range map[string]int64{
		"ext4":      unix.EXT4_SUPER_MAGIC,
		"btrfs":     unix.BTRFS_SUPER_MAGIC,
		"xfs":       unix.XFS_SUPER_MAGIC,
		"tmpfs":     unix.TMPFS_MAGIC,
		"overlayfs": unix.OVERLAYFS_SUPER_MAGIC,
	} {
		if got := classifyLinux(magic); got != "" {
			t.Fatalf("%s (%#x) classified as %q, want local", name, magic, got)
		}
	}
}
