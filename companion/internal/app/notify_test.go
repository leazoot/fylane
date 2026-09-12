package app

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testNotifier(lang string) (*notifier, func() []string) {
	var mu sync.Mutex
	var posted []string
	n := newNotifier(slog.Default(), func() string { return lang })
	n.window = 20 * time.Millisecond
	n.post = func(text string) {
		mu.Lock()
		defer mu.Unlock()
		posted = append(posted, text)
	}
	return n, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), posted...)
	}
}

func TestNotifierSaysABurstInOneSentence(t *testing.T) {
	n, posted := testNotifier("en")
	n.Approval("chatgpt")
	n.Approval("chatgpt")
	n.Approval("chatgpt")
	time.Sleep(60 * time.Millisecond)
	if got := posted(); len(got) != 1 || got[0] != "ChatGPT has 3 requests waiting for your approval" {
		t.Fatalf("posted %q", got)
	}
	// The next burst is its own sentence.
	n.Approval("Some IDE")
	time.Sleep(60 * time.Millisecond)
	if got := posted(); len(got) != 2 || got[1] != "Some IDE is waiting for your approval" {
		t.Fatalf("posted %q", got)
	}
	n.Approval("claude")
	n.Approval("grok")
	time.Sleep(60 * time.Millisecond)
	if got := posted(); len(got) != 3 || got[2] != "2 requests are waiting for your approval" {
		t.Fatalf("posted %q", got)
	}
}

func TestNotifierSpeaksTheWindowsLanguageAndNeverSaysUnknown(t *testing.T) {
	n, posted := testNotifier("zh")
	n.Approval("unknown")
	time.Sleep(60 * time.Millisecond)
	n.Approval("gemini")
	n.Approval("gemini")
	time.Sleep(60 * time.Millisecond)
	n.Pairing(`Cursor"; do shell script "id`)
	got := posted()
	want := []string{
		"有个平台 在等你批准",
		"Gemini 有 2 条请求在等你批准",
		"Cursor do shell script id 请求连接这台设备",
	}
	if len(got) != len(want) {
		t.Fatalf("posted %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}
