package workspace

import (
	"path"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Default rule sets . Copied into each new workspace row so a
// later default change never silently alters an existing workspace's policy.
// Binary/large-file exclusion is size- and content-based, handled by the read
// tools, not expressible as a name pattern.

// DefaultExcludeRules returns paths hidden from listing, search, and read.
func DefaultExcludeRules() []string {
	return []string{
		".git/", "node_modules/", "vendor/", "dist/", "build/",
		".next/", ".cache/", "coverage/", "*.lock",
	}
}

// DefaultSensitiveRules returns file patterns treated as sensitive: hidden in
// listings, skipped in search, explicit confirmation on exact read, high-risk
// warning on write, names never sent through Relay in plaintext.
func DefaultSensitiveRules() []string {
	return []string{
		".env", ".env.*", "*.pem", "*.key", "id_rsa", "id_ed25519",
		"credentials*", "secrets*",
	}
}

// MatchesAny reports whether the canonical slash-separated relative path rel
// matches any rule. Matching is case-insensitive (macOS/Windows filesystems
// are case-insensitive, and a case-mismatched sensitive file must not slip
// through) and normalization-insensitive (see foldForMatch).
//
// Rule forms:
//   - "name/"  — directory rule: matches when any path segment equals name,
//     so ".git/" covers ".git", ".git/config" and "a/.git/hooks".
//   - a pattern containing "/" — path.Match glob against the whole path.
//   - anything else — glob against every path segment, so "*.lock" and
//     ".env.*" match at any depth.
func MatchesAny(rules []string, rel string) bool {
	rel = foldForMatch(rel)
	segments := strings.Split(rel, "/")
	for _, rule := range rules {
		rule = foldForMatch(strings.TrimSpace(rule))
		if rule == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(rule, "/"); ok {
			for _, seg := range segments {
				if seg == dir {
					return true
				}
			}
			continue
		}
		if strings.Contains(rule, "/") {
			if ok, err := path.Match(rule, rel); err == nil && ok {
				return true
			}
			continue
		}
		for _, seg := range segments {
			if ok, err := path.Match(rule, seg); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// foldForMatch reduces a path or a rule to the form the two are compared in.
//
// Normalization is here and not in CleanPath on purpose. The filesystems this
// runs on disagree about whether "café" written as one codepoint and as e plus
// a combining accent are the same file — macOS and Windows fold them, Linux
// does not — so rewriting the caller's path would make a real Linux file
// unreachable to fix a comparison bug. The canonical path handed back to
// callers, stored, and used to open files therefore stays exactly as it
// arrived; only the two sides of a rule comparison are folded.
//
// Without this, a rule containing any non-ASCII character is bypassed by
// respelling the same name in the other normalization form, and on a folding
// filesystem the respelled path opens the very same file. The built-in rule
// sets are all ASCII, so this reaches user-added rules.
func foldForMatch(s string) string {
	return norm.NFC.String(strings.ToLower(s))
}
