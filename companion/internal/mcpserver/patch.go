package mcpserver

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// applyUnified applies a unified diff to before and returns the patched
// content. It is strict: context and deletion lines must match the current
// content exactly, hunks must be in order, and any mismatch fails with a
// descriptive error instead of guessing. (go-udiff generates diffs for us
// but its ApplyUnified cannot consume context lines, so application is
// implemented here.)
func applyUnified(patch, before string) (string, error) {
	src := splitKeepNL(before)
	var out []string
	cursor := 0
	sawHunk := false
	lastKind := byte(0)

	lines := strings.Split(patch, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ "):
			continue
		case strings.HasPrefix(l, "@@"):
			m := hunkHeaderRe.FindStringSubmatch(l)
			if m == nil {
				return "", fmt.Errorf("malformed hunk header %q", l)
			}
			fromLine, err := strconv.Atoi(m[1])
			if err != nil {
				return "", fmt.Errorf("malformed hunk header %q", l)
			}
			// "@@ -0,0" marks an insertion before the first line.
			start := max(fromLine-1, 0)
			if start < cursor {
				return "", errors.New("hunks overlap or are out of order")
			}
			for cursor < start {
				if cursor >= len(src) {
					return "", fmt.Errorf("hunk starts at line %d beyond end of file (%d lines)", fromLine, len(src))
				}
				out = append(out, src[cursor])
				cursor++
			}
			sawHunk = true
			lastKind = 0
		case l == "":
			// Blank line: at the end it is a split artifact; inside a hunk
			// it is an empty context line.
			if i == len(lines)-1 {
				continue
			}
			if !sawHunk {
				continue
			}
			if cursor >= len(src) || trimNL(src[cursor]) != "" {
				return "", fmt.Errorf("context mismatch at line %d: expected empty line", cursor+1)
			}
			out = append(out, src[cursor])
			cursor++
			lastKind = ' '
		case l[0] == ' ':
			if !sawHunk {
				return "", fmt.Errorf("content line %q before any hunk header", l)
			}
			if cursor >= len(src) || trimNL(src[cursor]) != l[1:] {
				return "", contextError(src, cursor, l[1:])
			}
			out = append(out, src[cursor])
			cursor++
			lastKind = ' '
		case l[0] == '-':
			if !sawHunk {
				return "", fmt.Errorf("content line %q before any hunk header", l)
			}
			if cursor >= len(src) || trimNL(src[cursor]) != l[1:] {
				return "", contextError(src, cursor, l[1:])
			}
			cursor++
			lastKind = '-'
		case l[0] == '+':
			if !sawHunk {
				return "", fmt.Errorf("content line %q before any hunk header", l)
			}
			out = append(out, l[1:]+"\n")
			lastKind = '+'
		case l[0] == '\\':
			// "\ No newline at end of file": after +/context it applies to
			// the produced line; after - it described the consumed line.
			if (lastKind == '+' || lastKind == ' ') && len(out) > 0 {
				out[len(out)-1] = trimNL(out[len(out)-1])
			}
		default:
			return "", fmt.Errorf("unexpected patch line %q", l)
		}
	}
	if !sawHunk {
		return "", errors.New("patch contains no hunks")
	}
	for cursor < len(src) {
		out = append(out, src[cursor])
		cursor++
	}
	return strings.Join(out, ""), nil
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+\d+(?:,\d+)? @@`)

func contextError(src []string, cursor int, want string) error {
	if cursor >= len(src) {
		return fmt.Errorf("patch expects line %d (%q) but the file ends at line %d", cursor+1, want, len(src))
	}
	return fmt.Errorf("patch does not match file at line %d: file has %q, patch expects %q",
		cursor+1, trimNL(src[cursor]), want)
}

// splitKeepNL splits into lines that keep their trailing newline (the last
// line may lack one).
func splitKeepNL(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func trimNL(s string) string { return strings.TrimSuffix(s, "\n") }
