package app

import (
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	goruntime "runtime"
	"sync"
	"time"
)

// Approval requests demand the user's attention: the platform is blocked on
// them and times out in tens of seconds. The Core (always resident) posts a
// local OS notification when one arrives; the desktop shell raising its
// window is layered on top when it is running. Nothing here leaves the
// machine.
//
// Requests come in bursts — a platform retrying three calls after a restart
// lands three in two seconds — so the notifier waits a beat and says what
// arrived in one sentence rather than three. It speaks the window's
// language, because it is the one thing the Core says without the window.

// safeWord keeps notification interpolation injection-proof: caller names
// come from the tunnel header and are normalized, but the script string must
// stay safe even if that ever changes.
var safeWord = regexp.MustCompile(`[^a-zA-Z0-9 _-]`)

// coalesceWindow is how long the first request in a burst waits for the
// rest. Short enough that a lone request still feels immediate.
const coalesceWindow = 2 * time.Second

// displayNames are the platforms the product knows by name; any other
// caller is shown under the name its client registered.
var displayNames = map[string]string{
	"chatgpt": "ChatGPT",
	"claude":  "Claude",
	"grok":    "Grok",
	"gemini":  "Gemini",
}

type notifier struct {
	log  *slog.Logger
	lang func() string
	// post delivers one notification. Production is osascript on macOS
	// and nothing elsewhere, where the desktop shell is the only surface.
	post   func(text string)
	window time.Duration

	mu      sync.Mutex
	waiting []string
	timer   *time.Timer
}

func newNotifier(log *slog.Logger, lang func() string) *notifier {
	n := &notifier{log: log, lang: lang, window: coalesceWindow}
	n.post = n.system
	return n
}

// Approval notes that who is waiting. Nothing is said until the window has
// passed; what arrived meanwhile is folded into the same sentence.
func (n *notifier) Approval(who string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.waiting = append(n.waiting, who)
	if n.timer == nil {
		n.timer = time.AfterFunc(n.window, n.flush)
	}
}

// Pairing announces a push-pairing prompt. One is enough to be said at once.
func (n *notifier) Pairing(who string) {
	n.post(n.sentence("pairing", n.caller(who), 1))
}

func (n *notifier) flush() {
	n.mu.Lock()
	waiting := n.waiting
	n.waiting, n.timer = nil, nil
	n.mu.Unlock()
	if len(waiting) == 0 {
		return
	}
	who := n.caller(waiting[0])
	same := true
	for _, w := range waiting[1:] {
		if n.caller(w) != who {
			same = false
			break
		}
	}
	switch {
	case len(waiting) == 1:
		n.post(n.sentence("one", who, 1))
	case same:
		n.post(n.sentence("many", who, len(waiting)))
	default:
		n.post(n.sentence("mixed", "", len(waiting)))
	}
}

// caller is the name a platform is shown under. A caller the Core could not
// name is still somebody, so the sentence names it as a platform rather
// than printing the placeholder.
func (n *notifier) caller(who string) string {
	if name, ok := displayNames[who]; ok {
		return name
	}
	from := safeWord.ReplaceAllString(who, "")
	if from == "" || from == "unknown" {
		if n.lang() == "zh" {
			return "有个平台"
		}
		return "A platform"
	}
	return from
}

func (n *notifier) sentence(kind, who string, count int) string {
	if n.lang() == "zh" {
		switch kind {
		case "one":
			return who + " 在等你批准"
		case "many":
			return fmt.Sprintf("%s 有 %d 条请求在等你批准", who, count)
		case "mixed":
			return fmt.Sprintf("有 %d 条请求在等你批准", count)
		default:
			return who + " 请求连接这台设备"
		}
	}
	switch kind {
	case "one":
		return who + " is waiting for your approval"
	case "many":
		return fmt.Sprintf("%s has %d requests waiting for your approval", who, count)
	case "mixed":
		return fmt.Sprintf("%d requests are waiting for your approval", count)
	default:
		return who + " asks to connect to this device"
	}
}

// system posts through the OS. Best-effort: a failure is logged and never
// blocks the approval path. macOS only; other platforms rely on the shell.
func (n *notifier) system(text string) {
	if goruntime.GOOS != "darwin" {
		return
	}
	script := `display notification "` + text + `" with title "Fylane" sound name "default"`
	go func() {
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			n.log.Debug("notification failed", "error", err)
		}
	}()
}
