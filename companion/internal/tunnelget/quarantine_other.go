//go:build !darwin

package tunnelget

// clearQuarantine has nothing to do off macOS: no other platform Fylane
// supports marks a file as downloaded in a way that blocks execution.
func clearQuarantine(string) error { return nil }
