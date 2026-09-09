package workspace

import "testing"

func TestDefaultExcludeRules(t *testing.T) {
	rules := DefaultExcludeRules()
	cases := []struct {
		rel  string
		want bool
	}{
		{".git", true},
		{".git/config", true},
		{"sub/module/.git/hooks/pre-commit", true},
		{"node_modules/lodash/index.js", true},
		{"packages/app/node_modules/react/index.js", true},
		{"vendor/pkg/a.go", true},
		{"dist/bundle.js", true},
		{"build/out.o", true},
		{".next/cache/x", true},
		{".cache/v1", true},
		{"coverage/lcov.info", true},
		{"pnpm-lock.lock", true},
		{"sub/yarn.lock", true},
		{"Cargo.LOCK", true}, // case-insensitive
		{"src/main.go", false},
		{"builder/main.go", false},       // "build/" must not match "builder"
		{"distribution/notes.md", false}, // nor "dist/" match "distribution"
		{"src/gitignore-parser.ts", false},
		{"locksmith.go", false},
	}
	for _, c := range cases {
		if got := MatchesAny(rules, c.rel); got != c.want {
			t.Errorf("MatchesAny(exclude, %q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestDefaultSensitiveRules(t *testing.T) {
	rules := DefaultSensitiveRules()
	cases := []struct {
		rel  string
		want bool
	}{
		{".env", true},
		{".env.local", true},
		{"config/.env", true},
		{"config/.env.production", true},
		{"certs/server.pem", true},
		{"keys/private.key", true},
		{".ssh/id_rsa", true},
		{".ssh/id_ed25519", true},
		{"credentials.json", true},
		{"aws/credentials", true},
		{"secrets.yaml", true},
		{"k8s/secrets-prod.yaml", true},
		{"Secrets.YAML", true}, // case-insensitive
		{".ENV", true},
		{"src/environment.ts", false},
		{"envelope.txt", false},
		{"monkey.tsx", false},
		{"docs/keyboard.md", false},
		{"src/rsa.go", false},
	}
	for _, c := range cases {
		if got := MatchesAny(rules, c.rel); got != c.want {
			t.Errorf("MatchesAny(sensitive, %q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestMatchesAnyPathGlob(t *testing.T) {
	if !MatchesAny([]string{"docs/*.md"}, "docs/readme.md") {
		t.Error("path glob docs/*.md should match docs/readme.md")
	}
	if MatchesAny([]string{"docs/*.md"}, "src/readme.md") {
		t.Error("path glob docs/*.md must not match src/readme.md")
	}
	if MatchesAny(nil, "anything") {
		t.Error("empty rule set must match nothing")
	}
	if MatchesAny([]string{"", "  "}, "anything") {
		t.Error("blank rules must match nothing")
	}
}

// The bypass this fold exists to close, reproduced. Measured on a real APFS
// volume before the fix: a rule written in one normalization form did not
// match the same name spelled in the other, and opening the respelled path
// returned the very same file's contents. macOS and Windows fold the two
// forms; Linux does not, which is why the fold lives here at comparison time
// and not in CleanPath — rewriting the caller's path would make a genuinely
// NFD-named Linux file unreachable.
func TestMatchesAnyFoldsNormalizationForms(t *testing.T) {
	const (
		nfcRule = "donn\u00e9es/"
		nfcPath = "donn\u00e9es/secret.txt"
		nfdPath = "donne\u0301es/secret.txt"
		nfdRule = "donne\u0301es/"
	)
	if !MatchesAny([]string{nfcRule}, nfcPath) {
		t.Fatal("NFC rule must match the NFC path; the test itself is wrong")
	}
	if !MatchesAny([]string{nfcRule}, nfdPath) {
		t.Error("NFC rule did not match the NFD spelling of the same name: " +
			"on a folding filesystem that path opens the same file, so the rule is bypassed")
	}
	if !MatchesAny([]string{nfdRule}, nfcPath) {
		t.Error("NFD rule did not match the NFC spelling; the fold must work both ways")
	}
}

// Folding must not start matching names that are merely similar. Only the two
// encodings of one name collapse; different letters stay different.
func TestMatchesAnyStillSeparatesDifferentNames(t *testing.T) {
	cases := []struct {
		rule, path string
	}{
		{".env", ".e\u0301nv"},         // e-with-acute is not e
		{"donn\u00e9es/", "donnees/x"}, // dropping the accent is a different name
		{"secret*", "public.txt"},
	}
	for _, c := range cases {
		if MatchesAny([]string{c.rule}, c.path) {
			t.Errorf("rule %q must not match %q", c.rule, c.path)
		}
	}
}
