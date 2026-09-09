package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/workspace"
)

const (
	// searchTimeout bounds one search_files call (time budget).
	searchTimeout = 10 * time.Second
	// maxSearchFiles bounds how many files one search may scan.
	maxSearchFiles = 20000
	// maxSearchFileBytes skips content search in files larger than this.
	maxSearchFileBytes = 4 << 20 // 4 MiB
	// maxSearchResults is the hard cap on max_results.
	maxSearchResults = 1000
	// defaultSearchResults applies when max_results is omitted.
	defaultSearchResults = 100
	// maxContextLines bounds the context_lines option.
	maxContextLines = 5
	// maxMatchLineLen truncates very long matched lines in results.
	maxMatchLineLen = 500
)

type searchFilesInput struct {
	WorkspaceID   string `json:"workspace_id,omitempty" jsonschema:"Opaque workspace identifier from workspace_info."`
	Query         string `json:"query,omitempty" jsonschema:"Text to search for inside files. Omit to search file names only (glob required)."`
	Glob          string `json:"glob,omitempty" jsonschema:"Path pattern filter, e.g. *.go or src/**/*.ts. Without a slash it matches file names at any depth."`
	Regex         bool   `json:"regex,omitempty" jsonschema:"Treat query as a Go regular expression."`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"Match case exactly. Default false."`
	MaxResults    int    `json:"max_results,omitempty" jsonschema:"Maximum results to return, 1-1000. Default 100."`
	ContextLines  int    `json:"context_lines,omitempty" jsonschema:"Lines of context around each content match, 0-5. Default 0."`
}

type searchMatch struct {
	Path string `json:"path"`
	// Line and Text are set for content matches only.
	Line    int      `json:"line,omitempty" jsonschema:"1-based line number of the match."`
	Text    string   `json:"text,omitempty" jsonschema:"The matched line, truncated when very long."`
	Context []string `json:"context,omitempty" jsonschema:"Surrounding lines when context_lines was requested."`
}

type searchFilesOutput struct {
	Matches      []searchMatch `json:"matches"`
	Truncated    bool          `json:"truncated" jsonschema:"True when the result, file, or time budget cut the search short."`
	FilesScanned int           `json:"files_scanned"`
	ElapsedMS    int64         `json:"elapsed_ms"`
}

func (t *toolset) searchFiles(ctx context.Context, _ *mcp.CallToolRequest, in searchFilesInput) (*mcp.CallToolResult, searchFilesOutput, error) {
	var zero searchFilesOutput
	if in.Query == "" && in.Glob == "" {
		return nil, zero, fmt.Errorf("provide query (content search) and/or glob (file name search)")
	}
	if in.MaxResults < 0 || in.MaxResults > maxSearchResults {
		return nil, zero, fmt.Errorf("max_results must be between 1 and %d", maxSearchResults)
	}
	if in.MaxResults == 0 {
		in.MaxResults = defaultSearchResults
	}
	if in.ContextLines < 0 || in.ContextLines > maxContextLines {
		return nil, zero, fmt.Errorf("context_lines must be between 0 and %d", maxContextLines)
	}
	if in.Glob != "" {
		if _, err := path.Match(strings.ReplaceAll(in.Glob, "**", "*"), "probe"); err != nil {
			return nil, zero, fmt.Errorf("invalid glob %q", in.Glob)
		}
	}

	matcher, err := newLineMatcher(in.Query, in.Regex, in.CaseSensitive)
	if err != nil {
		return nil, zero, err
	}

	ws, err := t.open(ctx, in.WorkspaceID)
	if err != nil {
		return nil, zero, err
	}

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	s := &searcher{
		ws:      ws,
		in:      in,
		matcher: matcher,
		out:     searchFilesOutput{Matches: []searchMatch{}},
		started: time.Now(),
	}
	err = s.walk(ctx, ws.Root(), "")
	s.out.ElapsedMS = time.Since(s.started).Milliseconds()
	if err != nil && ctx.Err() == nil {
		return nil, zero, err
	}
	if ctx.Err() != nil {
		// Budget exhausted: return what was found so far, flagged truncated.
		s.out.Truncated = true
	}
	return nil, s.out, nil
}

// lineMatcher matches a single line of text.
type lineMatcher func(line string) bool

func newLineMatcher(query string, regex, caseSensitive bool) (lineMatcher, error) {
	if query == "" {
		return nil, nil
	}
	if regex {
		expr := query
		if !caseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %v", err)
		}
		return re.MatchString, nil
	}
	if caseSensitive {
		return func(line string) bool { return strings.Contains(line, query) }, nil
	}
	lower := strings.ToLower(query)
	return func(line string) bool { return strings.Contains(strings.ToLower(line), lower) }, nil
}

type searcher struct {
	ws      *workspace.Workspace
	in      searchFilesInput
	matcher lineMatcher
	out     searchFilesOutput
	started time.Time
}

func (s *searcher) budgetLeft() bool {
	return len(s.out.Matches) < s.in.MaxResults && s.out.FilesScanned < maxSearchFiles
}

// walk scans absDir (relative prefix relDir) depth-first, honoring exclude
// and sensitive rules, skipping symlinks, and stopping when any budget runs
// out.
func (s *searcher) walk(ctx context.Context, absDir, relDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		// Unreadable directories are skipped, not fatal: search is
		// best-effort over what is accessible.
		return nil
	}
	for _, e := range entries {
		if !s.budgetLeft() {
			s.out.Truncated = true
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := path.Join(relDir, e.Name())
		if s.ws.Excluded(rel, e.IsDir()) || s.ws.Sensitive(rel) {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			if err := s.walk(ctx, filepath.Join(absDir, e.Name()), rel); err != nil {
				return err
			}
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		if s.in.Glob != "" && !globMatch(s.in.Glob, rel) {
			continue
		}
		if s.matcher == nil {
			// File-name search: the glob match itself is the result.
			s.out.Matches = append(s.out.Matches, searchMatch{Path: rel})
			continue
		}
		s.searchFile(filepath.Join(absDir, e.Name()), rel)
	}
	return nil
}

func (s *searcher) searchFile(abs, rel string) {
	info, err := os.Stat(abs)
	if err != nil || info.Size() > maxSearchFileBytes {
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		return
	}
	defer f.Close()
	s.out.FilesScanned++

	// Binary sniff: NUL in the first block means skip (binary files).
	head := make([]byte, 8192)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return
	}
	if _, err := f.Seek(0, 0); err != nil {
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var lines []string
	lineNo := 0
	var pending []int
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		lines = append(lines, line)
		if s.matcher(line) {
			pending = append(pending, lineNo)
			if len(s.out.Matches)+len(pending) >= s.in.MaxResults {
				break
			}
		}
	}
	for _, no := range pending {
		if len(s.out.Matches) >= s.in.MaxResults {
			s.out.Truncated = true
			return
		}
		m := searchMatch{Path: rel, Line: no, Text: truncateUTF8(lines[no-1], maxMatchLineLen)}
		if s.in.ContextLines > 0 {
			lo := max(no-1-s.in.ContextLines, 0)
			hi := min(no+s.in.ContextLines, len(lines))
			for i := lo; i < hi; i++ {
				if i != no-1 {
					m.Context = append(m.Context, truncateUTF8(lines[i], maxMatchLineLen))
				}
			}
		}
		s.out.Matches = append(s.out.Matches, m)
	}
}

// globMatch matches a slash-separated relative path against pattern.
// Patterns without a slash match the file name at any depth; patterns with
// slashes match the whole path, where `**` spans any number of segments.
func globMatch(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, path.Base(rel))
		return err == nil && ok
	}
	return segsMatch(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func segsMatch(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if segsMatch(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return segsMatch(pat[1:], segs[1:])
}
