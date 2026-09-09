package mcpserver

import (
	"strings"
	"testing"
)

func runSearch(t *testing.T, args map[string]any) searchFilesOutput {
	t.Helper()
	session, root := startSession(t)
	writeTree(t, root, map[string]string{
		"src/main.go":         "package main\n\nfunc main() { println(\"Hello Fylane\") }\n",
		"src/util/helper.go":  "package util\n// TODO(#12): helper\nfunc Helper() {}\n",
		"docs/readme.md":      "# Fylane\nhello world\n",
		"node_modules/x/a.js": "hello from excluded\n",
		".env":                "HELLO=secret\n",
		"data.bin":            "hello\x00world",
	})
	var out searchFilesOutput
	structured(t, callTool(t, session, "search_files", args), &out)
	return out
}

func matchPaths(out searchFilesOutput) string {
	var b []string
	for _, m := range out.Matches {
		b = append(b, m.Path)
	}
	return strings.Join(b, ",")
}

func TestSearchContent(t *testing.T) {
	// Case-insensitive by default; excluded, sensitive, and binary files
	// never produce matches.
	out := runSearch(t, map[string]any{"query": "hello"})
	paths := matchPaths(out)
	if !strings.Contains(paths, "src/main.go") || !strings.Contains(paths, "docs/readme.md") {
		t.Fatalf("matches = %v", out.Matches)
	}
	for _, banned := range []string{"node_modules", ".env", "data.bin"} {
		if strings.Contains(paths, banned) {
			t.Errorf("search leaked %s: %v", banned, out.Matches)
		}
	}
	for _, m := range out.Matches {
		if m.Line == 0 || m.Text == "" {
			t.Errorf("content match missing line info: %+v", m)
		}
	}
}

func TestSearchCaseSensitive(t *testing.T) {
	out := runSearch(t, map[string]any{"query": "hello", "case_sensitive": true})
	if paths := matchPaths(out); strings.Contains(paths, "src/main.go") || !strings.Contains(paths, "docs/readme.md") {
		t.Fatalf("case-sensitive matches = %v", out.Matches)
	}
}

func TestSearchRegexAndContext(t *testing.T) {
	out := runSearch(t, map[string]any{"query": `TODO\(#\d+\)`, "regex": true, "context_lines": 1})
	if len(out.Matches) != 1 || out.Matches[0].Path != "src/util/helper.go" || out.Matches[0].Line != 2 {
		t.Fatalf("regex matches = %+v", out.Matches)
	}
	if len(out.Matches[0].Context) != 2 {
		t.Fatalf("context = %v, want the surrounding two lines", out.Matches[0].Context)
	}
}

func TestSearchFilenameGlob(t *testing.T) {
	out := runSearch(t, map[string]any{"glob": "*.go"})
	paths := matchPaths(out)
	if !strings.Contains(paths, "src/main.go") || !strings.Contains(paths, "src/util/helper.go") {
		t.Fatalf("glob matches = %v", out.Matches)
	}
	if strings.Contains(paths, ".md") {
		t.Fatalf("glob over-matched: %v", out.Matches)
	}

	out = runSearch(t, map[string]any{"glob": "src/**/*.go"})
	if paths := matchPaths(out); !strings.Contains(paths, "src/util/helper.go") || !strings.Contains(paths, "src/main.go") {
		t.Fatalf("doublestar matches = %v", out.Matches)
	}

	// Glob also narrows content search.
	out = runSearch(t, map[string]any{"query": "hello", "glob": "*.md"})
	if paths := matchPaths(out); paths != "docs/readme.md" {
		t.Fatalf("glob+query matches = %v", out.Matches)
	}
}

func TestSearchLimitsAndErrors(t *testing.T) {
	out := runSearch(t, map[string]any{"query": "e", "max_results": 1})
	if len(out.Matches) != 1 || !out.Truncated {
		t.Fatalf("max_results=1: %d matches, truncated=%v", len(out.Matches), out.Truncated)
	}

	session, _ := startSession(t)
	if res := callTool(t, session, "search_files", map[string]any{}); !res.IsError {
		t.Error("search with neither query nor glob must fail")
	}
	if res := callTool(t, session, "search_files", map[string]any{"query": "(", "regex": true}); !res.IsError {
		t.Error("invalid regex must fail")
	}
	if res := callTool(t, session, "search_files", map[string]any{"query": "x", "max_results": 100000}); !res.IsError {
		t.Error("excessive max_results must fail")
	}
	if res := callTool(t, session, "search_files", map[string]any{"query": "x", "context_lines": 50}); !res.IsError {
		t.Error("excessive context_lines must fail")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"*.go", "src/main.go", true},
		{"*.go", "main.go", true},
		{"*.go", "docs/readme.md", false},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/util/helper.go", false},
		{"src/**/*.go", "src/util/helper.go", true},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "docs/x.go", false},
		{"**/*.md", "docs/readme.md", true},
		{"**/*.md", "readme.md", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.rel); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}
