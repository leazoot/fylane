//go:build windows

package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// nonLocalFS names the filesystem kind when path is not on local storage, and
// returns "" when it is.
func nonLocalFS(path string) (string, error) {
	return classifyWindows(path, windows.GetDriveType)
}

// classifyWindows takes the drive-type lookup as an argument so the UNC and
// malformed-path branches can be tested without a mapped network drive.
func classifyWindows(path string, driveType func(*uint16) uint32) (string, error) {
	// A UNC root (\\server\share) is remote by construction, and has no
	// drive letter for GetDriveType to answer about. Checked first because
	// filepath.VolumeName returns the whole \\server\share for it.
	if strings.HasPrefix(path, `\\`) {
		return "UNC network path", nil
	}
	vol := filepath.VolumeName(path)
	if vol == "" {
		return "", fmt.Errorf("no volume in path %q", path)
	}
	root, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return "", fmt.Errorf("volume root for %q: %w", path, err)
	}
	if driveType(root) == windows.DRIVE_REMOTE {
		return "mapped network drive", nil
	}
	return "", nil
}
