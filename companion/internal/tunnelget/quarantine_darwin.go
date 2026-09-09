//go:build darwin

package tunnelget

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// clearQuarantine removes the mark macOS puts on files that arrived from
// elsewhere. Gatekeeper refuses to execute a marked file without a user
// gesture that a background process has no way to make.
//
// Nothing this package writes carries the mark — it is set by the program
// that does the downloading, and only programs that ask LaunchServices for it
// do. So the expected result here is ENOATTR, and that is a success, not a
// failure to report.
func clearQuarantine(path string) error {
	err := unix.Removexattr(path, "com.apple.quarantine")
	switch {
	case err == nil, errors.Is(err, unix.ENOATTR), errors.Is(err, unix.ENOTSUP):
		return nil
	default:
		return fmt.Errorf("cannot clear the quarantine mark: %w", err)
	}
}
