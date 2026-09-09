package mcpserver

import (
	"strings"
	"testing"

	"github.com/aymanbagabas/go-udiff"
)

func TestApplyUnified(t *testing.T) {
	cases := []struct {
		name, before, patch, want string
	}{
		{
			name:   "replace with context",
			before: "one\ntwo\nthree\n",
			patch:  "--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n three\n",
			want:   "one\n2\nthree\n",
		},
		{
			name:   "insert at end",
			before: "a\nb\n",
			patch:  "@@ -2,1 +2,2 @@\n b\n+c\n",
			want:   "a\nb\nc\n",
		},
		{
			name:   "delete line",
			before: "a\nb\nc\n",
			patch:  "@@ -1,3 +1,2 @@\n a\n-b\n c\n",
			want:   "a\nc\n",
		},
		{
			name:   "two hunks",
			before: "1\n2\n3\n4\n5\n6\n7\n8\n",
			patch:  "@@ -1,2 +1,2 @@\n 1\n-2\n+two\n@@ -7,2 +7,2 @@\n 7\n-8\n+eight\n",
			want:   "1\ntwo\n3\n4\n5\n6\n7\neight\n",
		},
		{
			name:   "create from empty",
			before: "",
			patch:  "@@ -0,0 +1,2 @@\n+hello\n+world\n",
			want:   "hello\nworld\n",
		},
		{
			name:   "no trailing newline result",
			before: "a\n",
			patch:  "@@ -1,1 +1,1 @@\n-a\n+b\n\\ No newline at end of file\n",
			want:   "b",
		},
		{
			name:   "empty context lines",
			before: "a\n\nb\n",
			patch:  "@@ -1,3 +1,3 @@\n a\n\n-b\n+c\n",
			want:   "a\n\nc\n",
		},
	}
	for _, c := range cases {
		got, err := applyUnified(c.patch, c.before)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestApplyUnifiedRejects(t *testing.T) {
	cases := []struct {
		name, before, patch, wantErr string
	}{
		{"context mismatch", "one\ntwo\n", "@@ -1,2 +1,2 @@\n one\n-TWO\n+2\n", "does not match"},
		{"delete mismatch", "a\n", "@@ -1,1 +1,0 @@\n-b\n", "does not match"},
		{"no hunks", "a\n", "just some text", "unexpected patch line"},
		{"empty patch", "a\n", "", "no hunks"},
		{"overlapping hunks", "1\n2\n3\n", "@@ -2,1 +2,1 @@\n-2\n+x\n@@ -1,1 +1,1 @@\n-1\n+y\n", "out of order"},
		{"beyond eof", "a\n", "@@ -5,1 +5,1 @@\n-x\n+y\n", "beyond end of file"},
		{"malformed header", "a\n", "@@ nonsense @@\n-a\n+b\n", "malformed hunk header"},
	}
	for _, c := range cases {
		_, err := applyUnified(c.patch, c.before)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

// Round trip: whatever go-udiff generates, applyUnified must apply.
func TestApplyUnifiedRoundTrip(t *testing.T) {
	cases := [][2]string{
		{"one\ntwo\nthree\n", "one\n2\nthree\nfour\n"},
		{"", "created\ncontent\n"},
		{"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\n", "a\nB\nc\nd\ne\nf\ng\nh\ni\nJ\nk\n"},
		{"same\n", "same\n"},
		{"trailing", "trailing\nmore"},
		{"x\n", ""},
	}
	for _, c := range cases {
		patch := udiff.Unified("a/f", "b/f", c[0], c[1])
		if patch == "" {
			continue // identical inputs produce no diff
		}
		got, err := applyUnified(patch, c[0])
		if err != nil {
			t.Errorf("round trip %q→%q: %v\npatch:\n%s", c[0], c[1], err, patch)
			continue
		}
		if got != c[1] {
			t.Errorf("round trip %q→%q: got %q\npatch:\n%s", c[0], c[1], got, patch)
		}
	}
}
