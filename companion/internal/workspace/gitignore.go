package workspace

import (
	"bufio"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Git ignore rules as a source of *exclusion*, never as the source of the
// listing.
//
// The comparison this came from lists a workspace out of the git index, so a
// file that is not tracked yet — the one the user wrote a minute ago, or
// anything in a directory that is not a repository — does not exist as far as
// the model is concerned. That is a worse failure than the one it fixes. So
// the walk stays a filesystem walk and every real file keeps showing up;
// what the repository's own ignore rules decide is only what to hide.
//
// Two properties hold this in place, and both are asserted in the tests:
//
//   - Ignore rules can only ever hide more, never reveal more. The workspace's
//     own exclude rules and the sensitive-file rules are evaluated separately
//     and OR'd with this answer, so a "!" line in a .gitignore cannot bring
//     back a path either of them holds. This matters because a .gitignore is
//     ordinary workspace content that a model can write.
//   - Comparison folds case and Unicode normalization exactly as MatchesAny
//     does (foldForMatch), for the reason recorded there: on a filesystem that
//     folds them, two spellings open the same file, and a rule that only knows
//     one spelling is not a rule.
//
// What is deliberately not read: nothing outside the workspace. No global
// core.excludesFile, no ~/.config/git/ignore, no .git/info/exclude — the first
// two live outside the root and the third is inside .git, which is excluded.
// A workspace's rules come from the workspace.

// ignoreFileName is the only file consulted, at every directory level.
const ignoreFileName = ".gitignore"

// Bounds on what one file may contribute. A .gitignore is workspace content,
// so it is read defensively rather than trusted to be small.
const (
	maxIgnoreFileBytes = 256 << 10
	maxIgnorePatterns  = 2000
)

// ignorePattern is one parsed line.
type ignorePattern struct {
	// segs is the pattern split on "/", with "**" kept as its own segment.
	// A pattern that was not anchored gets a leading "**" during parsing, so
	// matching never has to ask again where the pattern came from.
	segs []string
	// negate marks a "!" line: a later match un-ignores what an earlier one
	// ignored, within this same directory's rules.
	negate bool
	// dirOnly marks a trailing "/": the pattern names a directory.
	dirOnly bool
}

// ignoreRules is the parsed content of one .gitignore, in file order.
// Later patterns win, which is git's rule.
type ignoreRules struct {
	patterns []ignorePattern
}

// ignoreCache reads and remembers the .gitignore files of one workspace.
//
// A Workspace handle is built per call (Manager.Open constructs a new one
// every time), so the cache lives exactly as long as one listing or search
// and there is nothing to invalidate: an edit to a .gitignore is picked up by
// the next call. The mutex is there because the handle is reachable from more
// than one goroutine, not because the walk is concurrent.
type ignoreCache struct {
	root string

	mu sync.Mutex
	// dirs holds the parsed .gitignore of each directory, dirVerdict whether
	// each directory is itself ignored.
	dirs       map[string]*ignoreRules
	dirVerdict map[string]bool
}

func newIgnoreCache(root string) *ignoreCache {
	return &ignoreCache{root: root,
		dirs: map[string]*ignoreRules{}, dirVerdict: map[string]bool{}}
}

// ignored reports whether the repository's own ignore rules hide rel.
//
// rel is a canonical slash-separated workspace-relative path; isDir says
// whether it names a directory, which is the only thing that separates a
// "build/" rule from a file called "build".
func (c *ignoreCache) ignored(rel string, isDir bool) bool {
	if c == nil || rel == "" {
		return false
	}
	// The parent first. Git does not descend into an ignored directory, so
	// nothing below one can be brought back by a "!" further down, and
	// asking about the parent separately is what makes that true here rather
	// than an accident of pattern order.
	//
	// It recurses through the parent rather than looping over every ancestor
	// because the answer for a directory is memoized: a search walks the same
	// directory once for every file in it, and the loop would re-derive the
	// same chain each time.
	if i := strings.LastIndex(rel, "/"); i > 0 && c.dirIgnored(rel[:i]) {
		return true
	}
	// Patterns are folded at parse time; the path has to be folded the same
	// way to be compared with them. The raw segments are kept alongside
	// because they, not the folded ones, are what opens the .gitignore files
	// on disk — lowercasing a path is a comparison device, never a way to
	// name a file (the reason is spelled out in foldForMatch).
	return c.verdict(strings.Split(rel, "/"), strings.Split(foldForMatch(rel), "/"), isDir)
}

// dirIgnored is ignored() for a directory, remembered. Nothing invalidates it
// because the handle it hangs on lives for one call.
func (c *ignoreCache) dirIgnored(rel string) bool {
	c.mu.Lock()
	v, ok := c.dirVerdict[rel]
	c.mu.Unlock()
	if ok {
		return v
	}
	// Computed outside the lock: ignored() recurses back into this method
	// for the parent, and holding the lock across that would deadlock.
	v = c.ignored(rel, true)
	c.mu.Lock()
	c.dirVerdict[rel] = v
	c.mu.Unlock()
	return v
}

// verdict applies every .gitignore from the root down to the directory
// holding segs. Later files and later lines win.
func (c *ignoreCache) verdict(segs, folded []string, isDir bool) bool {
	ignored := false
	for depth := 0; depth < len(segs); depth++ {
		rules := c.rulesFor(segs[:depth])
		if len(rules.patterns) == 0 {
			continue
		}
		rest := folded[depth:]
		for _, p := range rules.patterns {
			if p.matches(rest, isDir) {
				ignored = !p.negate
			}
		}
	}
	return ignored
}

// matches reports whether the pattern names rest, which is already relative
// to the directory the pattern was written in.
func (p ignorePattern) matches(rest []string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return matchSegments(p.segs, rest)
}

// matchSegments matches a segment-wise glob, where "**" spans any number of
// segments (including none) and every other segment is matched by path.Match,
// which never lets "*" or "?" cross a separator.
func matchSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	// A malformed pattern is treated as matching nothing rather than as an
	// error: one bad line in a .gitignore must not stop a listing.
	if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], name[1:])
}

// rulesFor returns the parsed .gitignore of one directory, reading it at most
// once. A directory without one caches an empty result so it is not stat'd
// again for every entry in it.
func (c *ignoreCache) rulesFor(dir []string) *ignoreRules {
	key := strings.Join(dir, "/")
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.dirs[key]; ok {
		return r
	}
	r := readIgnoreFile(filepath.Join(append([]string{c.root}, dir...)...))
	c.dirs[key] = r
	return r
}

// readIgnoreFile parses the .gitignore in absDir, if there is one.
func readIgnoreFile(absDir string) *ignoreRules {
	f, err := os.Open(filepath.Join(absDir, ignoreFileName))
	if err != nil {
		return &ignoreRules{}
	}
	defer f.Close()

	rules := &ignoreRules{}
	scanner := bufio.NewScanner(io.LimitReader(f, maxIgnoreFileBytes))
	for scanner.Scan() {
		if len(rules.patterns) >= maxIgnorePatterns {
			break
		}
		if p, ok := parseIgnoreLine(scanner.Text()); ok {
			rules.patterns = append(rules.patterns, p)
		}
	}
	return rules
}

// parseIgnoreLine turns one line into a pattern, reporting false for blank
// lines and comments.
func parseIgnoreLine(line string) (ignorePattern, bool) {
	// Trailing whitespace is not part of a pattern unless it was escaped.
	line = trimUnescapedTrailingSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignorePattern{}, false
	}

	var p ignorePattern
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}
	// "\#" and "\!" are the literal characters, not a comment or a negation.
	if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	}
	if line == "" {
		return ignorePattern{}, false
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return ignorePattern{}, false
	}

	// A pattern with a separator anywhere in it is anchored to the directory
	// its .gitignore sits in; one without is matched at any depth below it,
	// which is the same thing as a leading "**".
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	p.segs = strings.Split(foldForMatch(line), "/")
	if !anchored {
		p.segs = append([]string{"**"}, p.segs...)
	}
	return p, true
}

// trimUnescapedTrailingSpace drops trailing spaces that were not written as
// "\ ", and unescapes the ones that were.
func trimUnescapedTrailingSpace(line string) string {
	end := len(line)
	for end > 0 && line[end-1] == ' ' {
		// A space preceded by an odd number of backslashes is escaped, so
		// this and everything before it stays.
		slashes := 0
		for i := end - 2; i >= 0 && line[i] == '\\'; i-- {
			slashes++
		}
		if slashes%2 == 1 {
			break
		}
		end--
	}
	return strings.ReplaceAll(line[:end], `\ `, " ")
}
