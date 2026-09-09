// Package sandbox validates workspace-relative paths supplied by remote
// callers. It is the first gate of the Fylane path sandbox.
//
// This package performs lexical validation only: separators, traversal,
// reserved names, deceptive characters, and mixed confusable alphabets are
// decided from the string alone. Filesystem-level checks — real-path
// resolution and symlink/junction escape detection — are layered on top by
// the workspace resolver.
//
// Unicode arrives here as two unrelated problems, and only one of them is
// this package's. A name that mixes Latin with Cyrillic or Greek letters is
// refused below: the filesystem would keep "src" and "sгc" as two genuinely
// different directories while a monospaced approval face renders them
// identically, and local approval is the only final authority.
// The other problem — the same file reachable as both "café" and "café" —
// is not decided here at all, because whether those are one file depends on
// the filesystem; it is folded at comparison time in workspace.MatchesAny.
package sandbox

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reasons a path is rejected. Exposed as errors so callers can map them to
// stable tool-facing error messages without string matching.
var (
	ErrEmptyPath      = errors.New("path is empty")
	ErrAbsolutePath   = errors.New("absolute paths are not allowed; use a workspace-relative path")
	ErrPathTraversal  = errors.New("path escapes the workspace root")
	ErrInvalidChar    = errors.New("path contains an invalid character")
	ErrWorkspaceRoot  = errors.New("operations on the workspace root itself are not allowed")
	ErrPathTooLong    = errors.New("path is too long")
	ErrReservedName   = errors.New("path contains a Windows reserved file name")
	ErrSymlink        = errors.New("path goes through a symbolic link or junction")
	ErrSpecialFile    = errors.New("path refers to a device or special file")
	ErrConfusableName = errors.New("path component mixes visually confusable alphabets")
	ErrProtectedPath  = errors.New("writes to this path are not allowed")
	ErrNotDirectory   = errors.New("a path component is not a directory")
)

// maxPathLen bounds the accepted relative path length. Kept well under
// common OS limits (Windows MAX_PATH 260 applies to the joined absolute
// path, checked again at resolve time).
const maxPathLen = 1024

// CleanPath validates a caller-supplied workspace-relative path and returns
// its canonical slash-separated form (e.g. "src/app/main.go").
//
// It rejects absolute paths (POSIX, Windows drive letters, UNC), any form of
// `..` traversal that would leave the workspace root, NUL bytes, and the
// workspace root itself ("." / ""). Backslashes are treated as separators so
// a Windows-style relative path cannot smuggle traversal segments.
func CleanPath(p string) (string, error) {
	if p == "" {
		return "", ErrEmptyPath
	}
	if len(p) > maxPathLen {
		return "", ErrPathTooLong
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: NUL byte", ErrInvalidChar)
	}

	// Normalize Windows separators before any structural checks so that
	// `..\..\x` and `\\server\share` are seen for what they are.
	slashed := strings.ReplaceAll(p, `\`, "/")

	if strings.HasPrefix(slashed, "/") {
		// Covers POSIX absolute paths and UNC paths (`//server/share`).
		return "", ErrAbsolutePath
	}
	if isWindowsDrivePath(slashed) {
		return "", ErrAbsolutePath
	}
	// A colon anywhere else means a Windows drive-relative path or an NTFS
	// alternate data stream; neither has a legitimate cross-platform use.
	if strings.ContainsRune(slashed, ':') {
		return "", fmt.Errorf("%w: ':'", ErrInvalidChar)
	}

	cleaned := path.Clean(slashed)
	if cleaned == "." {
		return "", ErrWorkspaceRoot
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrPathTraversal
	}
	for _, comp := range strings.Split(cleaned, "/") {
		if err := validateComponent(comp); err != nil {
			return "", err
		}
	}
	return cleaned, nil
}

// validateComponent checks a single cleaned path segment for names that are
// unrepresentable, deceptive, or unsafe on any supported platform.
func validateComponent(comp string) error {
	if !utf8.ValidString(comp) {
		return fmt.Errorf("%w: invalid UTF-8", ErrInvalidChar)
	}
	// Windows silently strips trailing dots and spaces, so "secret.txt ."
	// and "secret.txt" would collide; reject the ambiguous form everywhere.
	if strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " ") {
		return fmt.Errorf("%w: name ends with a dot or space", ErrInvalidChar)
	}
	for _, r := range comp {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: control character", ErrInvalidChar)
		}
		if isDeceptiveRune(r) {
			return fmt.Errorf("%w: invisible or bidirectional-override character", ErrInvalidChar)
		}
	}
	if isReservedName(comp) {
		return fmt.Errorf("%w: %q", ErrReservedName, comp)
	}
	// Checked per dot-separated part, not over the whole name. Almost every
	// non-Latin filename carries a Latin extension — "файл.txt" is ordinary,
	// not an attack — so judging the joined string would reject the normal
	// filenames of everyone who does not write in Latin. A homoglyph still
	// has to sit inside some part to do its work: "sгc.txt" fails on "sгc",
	// "main.tхt" fails on "tхt".
	for _, part := range strings.Split(comp, ".") {
		if a, b, mixed := mixedScripts(part); mixed {
			return fmt.Errorf("%w: %q mixes %s and %s letters", ErrConfusableName, part, a, b)
		}
	}
	return nil
}

// scriptRanges are the alphabets whose lowercase letters overlap visually with
// Latin closely enough that a monospaced approval face cannot tell them apart:
// Cyrillic а/с/е/о/р/х and Greek ο/ν/ρ against their Latin twins.
var scriptRanges = []struct {
	name  string
	table *unicode.RangeTable
}{
	{"Latin", unicode.Latin},
	{"Cyrillic", unicode.Cyrillic},
	{"Greek", unicode.Greek},
}

// mixedScripts reports whether s draws its letters from more than one of the
// confusable alphabets, naming the first two found.
//
// This is the whole of the homoglyph defence, and it is deliberately not a
// confusable table. Rejecting the *mix* needs no data to maintain, explains
// itself in one sentence, and leaves single-script names alone — a wholly
// Cyrillic or wholly Greek filename is a legitimate name, not an attack.
// What it refuses is the only shape that lies: Latin text with a foreign
// letter hidden inside it, where "src" and "sгc" render identically and only
// the approval face stands between the two.
//
// Non-letters are ignored, so digits, punctuation, and CJK — which has no
// Latin lookalikes — never trigger it.
func mixedScripts(s string) (string, string, bool) {
	first := ""
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		for _, sc := range scriptRanges {
			if !unicode.Is(sc.table, r) {
				continue
			}
			if first == "" {
				first = sc.name
			} else if first != sc.name {
				return first, sc.name, true
			}
			break
		}
	}
	return "", "", false
}

// isDeceptiveRune reports whether r is invisible or reorders display text —
// the characters used for Unicode path spoofing: zero-width
// characters, soft hyphen, BOM, and bidi override/isolate controls.
func isDeceptiveRune(r rune) bool {
	switch r {
	case '\u00ad', // soft hyphen
		'\u200b', '\u200c', '\u200d', // zero-width space / non-joiner / joiner
		'\u200e', '\u200f', // LRM / RLM
		'\u2028', '\u2029', // line / paragraph separator
		'\ufeff': // BOM / zero-width no-break space
		return true
	}
	return (r >= '\u202a' && r <= '\u202e') || // bidi embedding and override controls
		(r >= '\u2066' && r <= '\u2069') // bidi isolate controls
}

// isReservedName reports whether the segment collides with a Windows device
// name (CON, PRN, AUX, NUL, COM1–9, LPT1–9), which Windows reserves with any
// extension and any case.
func isReservedName(comp string) bool {
	base := comp
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

// isWindowsDrivePath reports whether p starts with a drive designator such
// as "C:" or "c:/x", which is rejected as absolute regardless of host OS.
func isWindowsDrivePath(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
