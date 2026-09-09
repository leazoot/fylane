package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leazoot/fylane/companion/internal/store"
)

// wsWithRules builds a handle over dir with exactly the rules given, so a
// test states what it is testing instead of inheriting the defaults.
func wsWithRules(t *testing.T, dir string, exclude, sensitive []string) *Workspace {
	t.Helper()
	ws, err := FromRecord(&store.Workspace{
		ID: "ws_test", Name: "test", RootPath: dir, Mode: store.ModeReadWrite,
		ExcludeRules: exclude, SensitiveRules: sensitive,
	})
	if err != nil {
		t.Fatalf("FromRecord: %v", err)
	}
	return ws
}

func writeIgnore(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitIgnorePatternForms(t *testing.T) {
	root := t.TempDir()
	writeIgnore(t, root, strings.Join([]string{
		"# a comment",
		"",
		"*.log",            // any depth, by name
		"tmp/",             // directory only, any depth
		"/bin/",            // anchored to the root
		"docs/**/draft.md", // ** spans any number of directories
		"weird\\#name",     // an escaped # is a literal, not a comment
		"trailing  ",       // unescaped trailing spaces are not part of it
	}, "\n"))
	ws := wsWithRules(t, root, nil, nil)

	for _, tc := range []struct {
		path  string
		isDir bool
		want  bool
		why   string
	}{
		{"debug.log", false, true, "a bare name matches at the root"},
		{"src/deep/debug.log", false, true, "and at any depth"},
		{"debug.log.keep", false, false, "the glob is not a substring search"},
		{"tmp", true, true, "a directory rule matches the directory"},
		{"tmp/a.txt", false, true, "and everything under it"},
		{"src/tmp/a.txt", false, true, "at any depth, because it is not anchored"},
		{"tmp", false, false, "but not a file that happens to share the name"},
		{"bin", true, true, "a leading slash anchors to the root"},
		{"src/bin", true, false, "so a deeper bin is untouched"},
		{"docs/draft.md", false, true, "** spans zero directories"},
		{"docs/a/b/draft.md", false, true, "and several"},
		{"docs/draft.txt", false, false, "the last segment still has to match"},
		{"weird#name", false, true, "an escaped # is a literal"},
		{"trailing", false, true, "trailing spaces were dropped"},
		{"src/main.go", false, false, "an ordinary file is untouched"},
	} {
		if got := ws.Excluded(tc.path, tc.isDir); got != tc.want {
			t.Errorf("Excluded(%q, isDir=%v) = %v, want %v — %s",
				tc.path, tc.isDir, got, tc.want, tc.why)
		}
	}
}

func TestGitIgnoreAppliesBelowTheFileThatDeclaresIt(t *testing.T) {
	root := t.TempDir()
	writeIgnore(t, root, "root-only.txt\n")
	writeIgnore(t, filepath.Join(root, "web"), "dist/\nlocal.txt\n")
	ws := wsWithRules(t, root, nil, nil)

	for _, tc := range []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"root-only.txt", false, true},
		{"web/root-only.txt", false, true}, // the root's rules reach everywhere
		{"web/dist", true, true},
		{"web/dist/app.js", false, true},
		{"web/local.txt", false, true},
		{"web/inner/local.txt", false, true}, // unanchored, so any depth below web/
		{"dist", true, false},                // but web/.gitignore does not reach up
		{"local.txt", false, false},
	} {
		if got := ws.Excluded(tc.path, tc.isDir); got != tc.want {
			t.Errorf("Excluded(%q, isDir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestGitIgnoreNegationWorksAndStopsAtTheWorkspaceRules(t *testing.T) {
	root := t.TempDir()
	writeIgnore(t, root, strings.Join([]string{
		"*.log",
		"!keep.log", // ordinary negation: a later line wins
		"secret/",
		"!secret/public.txt", // git does not descend into an ignored directory
		"!private",           // must not reach the workspace's own rules
		"!.env",              // must not reach the sensitive rules
		"!vendor",            // nor a stored exclude rule
	}, "\n"))
	ws := wsWithRules(t, root, []string{"private/", "vendor/"}, []string{".env"})

	if ws.Excluded("keep.log", false) {
		t.Error("a ! line did not re-include a path its own rules had ignored")
	}
	if !ws.Excluded("other.log", false) {
		t.Error("the negation leaked onto a path it does not name")
	}
	if !ws.Excluded("secret/public.txt", false) {
		t.Error("a ! below an ignored directory brought a file back; git never " +
			"descends into an ignored directory and neither may this")
	}

	// The three claims that make an ignore file safe to read at all. A
	// .gitignore is workspace content, so a model can write one; what it must
	// not be able to do is widen what it can see.
	if !ws.Excluded("private/notes.md", false) {
		t.Error("a ! line reached a workspace exclude rule")
	}
	if !ws.Excluded("vendor/dep.go", false) {
		t.Error("a ! line reached a stored exclude rule")
	}
	if !ws.Sensitive(".env") {
		t.Error("a ! line reached the sensitive-file rules")
	}
}

func TestGitIgnoreFoldsCaseAndNormalizationLikeTheRules(t *testing.T) {
	// Same reason as workspace.MatchesAny: on a filesystem that folds
	// these, both spellings open the same file, so a rule that knows only one
	// spelling is not a rule.
	//
	// The two constants below look identical and are not: nfc carries U+00E9,
	// nfd a plain e followed by the combining acute accent U+0301, eight bytes
	// against nine. Since nothing on screen can show that, the guard right
	// after them fails the test outright if an editor ever folds one into the
	// other — at which point the assertions would be comparing a string with
	// itself and passing for the wrong reason.
	const nfc = "données"
	const nfd = "données"
	if nfc == nfd {
		t.Fatal("the two spellings are the same bytes; this test cannot show anything")
	}

	root := t.TempDir()
	writeIgnore(t, root, "Build/\n"+nfc+"/\n")
	ws := wsWithRules(t, root, nil, nil)

	if !ws.Excluded("build/out.o", false) {
		t.Error("case-folding is not applied to ignore rules")
	}
	if !ws.Excluded(nfd+"/x.txt", false) {
		t.Error("the NFD spelling of an NFC rule slipped past; this is the " +
			"the bypass reappearing in a second matcher")
	}
}

func TestGitIgnoreDegradesQuietly(t *testing.T) {
	root := t.TempDir()
	// A workspace with no ignore file at all must behave exactly as before.
	plain := wsWithRules(t, root, []string{"vendor/"}, nil)
	if plain.Excluded("src/main.go", false) || !plain.Excluded("vendor/x.go", false) {
		t.Error("a workspace without a .gitignore did not fall back to its own rules")
	}

	// One unparseable line must not take the rest of the file with it, and
	// must not fail a listing: a bad rule matches nothing.
	writeIgnore(t, root, "[unclosed\n*.log\n")
	ws := wsWithRules(t, root, nil, nil)
	if ws.Excluded("[unclosed", false) {
		t.Error("a malformed pattern was treated as matching something")
	}
	if !ws.Excluded("debug.log", false) {
		t.Error("a malformed line stopped the rest of the file from being read")
	}
}

func TestGitIgnoreIsBounded(t *testing.T) {
	root := t.TempDir()
	// A .gitignore is workspace content and a model can write one, so the
	// reader must not be a way to make the Companion do unbounded work.
	var b strings.Builder
	for i := 0; i < maxIgnorePatterns*3; i++ {
		b.WriteString("pattern")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\n")
	}
	b.WriteString("late.log\n")
	writeIgnore(t, root, b.String())
	ws := wsWithRules(t, root, nil, nil)

	if ws.Excluded("late.log", false) {
		t.Error("a pattern past the cap was still applied; the cap is not in force")
	}
	if got := len(ws.ignore.rulesFor(nil).patterns); got != maxIgnorePatterns {
		t.Errorf("kept %d patterns, want the cap of %d", got, maxIgnorePatterns)
	}
}
