//go:build !darwin && !linux && !windows

package workspace

// nonLocalFS has no way to ask on this platform, so it does not guess.
// Fylane ships darwin, linux and windows; a fourth platform needs a real
// implementation here rather than an inherited "looks local to me".
func nonLocalFS(string) (string, error) { return "", nil }
