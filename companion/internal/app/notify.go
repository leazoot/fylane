package app

import (
	"log/slog"
	"os/exec"
	"regexp"
	goruntime "runtime"
)

// Approval requests demand the user's attention: the platform is blocked on
// them and times out in tens of seconds. The Core (always resident) posts a
// local OS notification the moment one arrives; the desktop shell raising
// its window is layered on top when it is running. Nothing here leaves the
// machine.

// safeWord keeps notification interpolation injection-proof: provider names
// come from the tunnel header and are normalized, but the script string must
// stay safe even if that ever changes.
var safeWord = regexp.MustCompile(`[^a-zA-Z0-9 _-]`)

// notifyApprovalRequested posts a system notification for a pending
// approval. Best-effort: failures are logged and never block the approval
// path. Currently macOS-only; other platforms rely on the desktop shell.
func notifyApprovalRequested(log *slog.Logger, provider string) {
	notify(log, provider, "is waiting for your approval")
}

// notifyPairingRequested announces a push-pairing prompt.
func notifyPairingRequested(log *slog.Logger, clientName string) {
	notify(log, clientName, "asks to connect to this device")
}

func notify(log *slog.Logger, who, what string) {
	if goruntime.GOOS != "darwin" {
		return
	}
	from := safeWord.ReplaceAllString(who, "")
	if from == "" {
		from = "A platform"
	}
	script := `display notification "` + from + ` ` + what + `" with title "Fylane" sound name "default"`
	go func() {
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			log.Debug("notification failed", "error", err)
		}
	}()
}
