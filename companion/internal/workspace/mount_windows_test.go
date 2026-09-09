//go:build windows

package workspace

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyWindows(t *testing.T) {
	fixed := func(*uint16) uint32 { return windows.DRIVE_FIXED }
	remote := func(*uint16) uint32 { return windows.DRIVE_REMOTE }

	cases := []struct {
		name      string
		path      string
		driveType func(*uint16) uint32
		want      string
		wantErr   bool
	}{
		{name: "local disk", path: `C:\Users\dev\proj`, driveType: fixed, want: ""},
		{name: "mapped drive", path: `Z:\proj`, driveType: remote, want: "mapped network drive"},
		// The drive-type lookup must not even be consulted: a UNC path has
		// no drive letter for it to answer about.
		{name: "unc share", path: `\\server\share\proj`, driveType: fixed, want: "UNC network path"},
		{name: "no volume", path: `\proj`, driveType: fixed, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyWindows(tc.path, tc.driveType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("classifyWindows(%q) = %q, want error", tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyWindows(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("classifyWindows(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
