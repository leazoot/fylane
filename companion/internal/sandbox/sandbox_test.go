package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanPathAccepts(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a.txt", "a.txt"},
		{"src/app/main.go", "src/app/main.go"},
		{"./a.txt", "a.txt"},
		{"src//app///main.go", "src/app/main.go"},
		{"src/./app/main.go", "src/app/main.go"},
		{"a/../b.txt", "b.txt"}, // stays inside the root after cleaning
		{"docs/读我.md", "docs/读我.md"},
		{"src\\app\\main.go", "src/app/main.go"}, // Windows-style separators normalized
		{"trailing/dir/", "trailing/dir"},
		{"a..b//x", "a..b/x"}, // interior dots that are not a traversal segment are legal
		// A name written wholly in one alphabet is a name, not an attack. The
		// Latin extension these normally carry is why the confusable check
		// runs per dot-separated part: judging "файл.txt" whole would reject
		// the ordinary filenames of everyone who does not write in Latin.
		{"исходник/файл.txt", "исходник/файл.txt"},
		{"Ωμέγα.md", "Ωμέγα.md"},
		{"docs/读我.md", "docs/读我.md"},
		{"数据/report-2026.csv", "数据/report-2026.csv"},
		// Both normalization forms are accepted and handed back untouched:
		// which of them is "the" name is the filesystem's business, and
		// rewriting it here would make a real Linux file unreachable. Written
		// as escapes because a literal would be normalized by whatever edits
		// this file, which is the very distinction under test.
		{"caf\u00e9.txt", "caf\u00e9.txt"},   // NFC
		{"cafe\u0301.txt", "cafe\u0301.txt"}, // NFD
	}
	for _, c := range cases {
		got, err := CleanPath(c.in)
		if err != nil {
			t.Errorf("CleanPath(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("CleanPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanPathRejects(t *testing.T) {
	cases := []struct {
		in      string
		wantErr error
	}{
		{"", ErrEmptyPath},
		{".", ErrWorkspaceRoot},
		{"./", ErrWorkspaceRoot},
		{"a/..", ErrWorkspaceRoot},
		{"..", ErrPathTraversal},
		{"../x.txt", ErrPathTraversal},
		{"a/../../x.txt", ErrPathTraversal},
		{"..\\x.txt", ErrPathTraversal},
		{"a\\..\\..\\x.txt", ErrPathTraversal},
		{"/etc/passwd", ErrAbsolutePath},
		{"//server/share/x", ErrAbsolutePath},
		{"\\\\server\\share\\x", ErrAbsolutePath},
		{"C:/Windows/system32", ErrAbsolutePath},
		{"c:\\temp\\x", ErrAbsolutePath},
		{"C:relative", ErrAbsolutePath},
		{"a/b:stream", ErrInvalidChar},
		{"a\x00b", ErrInvalidChar},
		{strings.Repeat("a/", 600) + "x", ErrPathTooLong},
		// Windows reserved device names, any case and extension.
		{"con", ErrReservedName},
		{"CON.txt", ErrReservedName},
		{"docs/aux.tar.gz", ErrReservedName},
		{"COM1", ErrReservedName},
		{"lpt9.log", ErrReservedName},
		// Names Windows silently rewrites (trailing dot/space).
		{"secret.txt.", ErrInvalidChar},
		{"secret.txt ", ErrInvalidChar},
		{"dir./x", ErrInvalidChar},
		// Unicode spoofing: bidi override, zero-width, BOM, control chars,
		// invalid UTF-8 bytes.
		{"invoice\u202etxt.exe", ErrInvalidChar},
		{"src/\u200bmain.go", ErrInvalidChar},
		{"a\u2066b.txt", ErrInvalidChar},
		{"bad\u0007name", ErrInvalidChar},
		{"bom\ufeff.txt", ErrInvalidChar},
		{string([]byte{0xff, 0xfe}) + ".txt", ErrInvalidChar},
		// Homoglyphs: a foreign letter hidden inside Latin text. The
		// filesystem keeps these as genuinely different entries while a
		// monospaced approval face renders them identically to the real
		// path, and local approval is the only final authority.
		{"s\u0433c/app.ts", ErrConfusableName},     // Cyrillic ghe inside "src"
		{".\u0435nv", ErrConfusableName},           // Cyrillic ie inside ".env"
		{"m\u0430in.go", ErrConfusableName},        // Cyrillic a inside "main"
		{"main.t\u0445t", ErrConfusableName},       // hidden in the extension
		{"\u03bfrders.json", ErrConfusableName},    // Greek omicron opening "orders"
		{"src/\u0441onfig.yml", ErrConfusableName}, // Cyrillic es inside "config"
	}
	for _, c := range cases {
		_, err := CleanPath(c.in)
		if err == nil {
			t.Errorf("CleanPath(%q): expected error, got nil", c.in)
			continue
		}
		if !errors.Is(err, c.wantErr) {
			t.Errorf("CleanPath(%q) error = %v, want %v", c.in, err, c.wantErr)
		}
	}
}
