//go:build darwin

package workspace

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestClassifyDarwin(t *testing.T) {
	cases := []struct {
		name   string
		fstype string
		flags  uint32
		want   string
	}{
		{"apfs", "apfs", unix.MNT_LOCAL, ""},
		{"hfs", "hfs", unix.MNT_LOCAL, ""},
		{"exfat on a stick is still local storage", "exfat", unix.MNT_LOCAL, ""},
		{"nfs", "nfs", 0, "nfs (network)"},
		{"smb share", "smbfs", 0, "smbfs (network)"},
		{"afp share", "afpfs", 0, "afpfs (network)"},
		{"sshfs over macfuse", "macfuse", 0, "macfuse (FUSE)"},
		{"older macfuse name", "osxfuse", 0, "osxfuse (FUSE)"},
		// The case that earns the second question: `-o local` makes a FUSE
		// volume claim MNT_LOCAL for Finder's benefit. It is still a
		// userspace filesystem and still cannot promise atomic rename.
		{"fuse mounted -o local", "macfuse", unix.MNT_LOCAL, "macfuse (FUSE)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDarwin(tc.fstype, tc.flags); got != tc.want {
				t.Fatalf("classifyDarwin(%q, %#x) = %q, want %q", tc.fstype, tc.flags, got, tc.want)
			}
		})
	}
}
