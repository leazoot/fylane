package routerule

import (
	"fmt"
	"path"
	"strings"
)

// Route rules decide where a saved answer lands (design: Route rules screen).
// They are user preference, not a tracked entity: they live in config.json
// beside the other settings, and carry no lifecycle, audit trail, or foreign
// keys that would justify a table. Order is priority — the first rule whose
// source and pattern both match wins, and Fylane never reorders them.
//
// The rules only choose a destination directory. They grant nothing: the
// path sandbox still validates every resulting write, so a malformed or
// hostile rule can misfile a save but can never escape the workspace.

// SourceAny matches answers from every provider.
const SourceAny = "ANY"

// Rule is one lane on the Route rules screen.
type Rule struct {
	ID string `json:"id"`
	// Source is a provider name ("chatgpt", "claude", "grok") or SourceAny.
	Source string `json:"source"`
	// Patterns are glob patterns matched against the file's base name; any
	// one of them matching is enough ("*.tsx  *.ts" in the UI).
	Patterns []string `json:"patterns"`
	// Dest is the workspace-relative destination directory, or empty when
	// Action is not ActionRoute.
	Dest string `json:"dest"`
	// Action is "route" (file lands under Dest) or "ask" (never write
	// silently; the save is held for a decision).
	Action string `json:"action"`
	// Uses counts saves this rule has routed. Incremented by the Core when a
	// change set carrying the rule ID is applied.
	Uses int `json:"uses"`
}

// Rule actions.
const (
	ActionRoute = "route"
	ActionAsk   = "ask"
)

// Validate reports whether the rule is usable. It enforces the same
// relative-path shape the sandbox requires, so an invalid destination is
// rejected when the rule is written rather than when a save hits it.
func (r *Rule) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("rule id is required")
	}
	if r.Source == "" {
		return fmt.Errorf("rule %s: source is required", r.ID)
	}
	if len(r.Patterns) == 0 {
		return fmt.Errorf("rule %s: at least one pattern is required", r.ID)
	}
	for _, p := range r.Patterns {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("rule %s: empty pattern", r.ID)
		}
		if _, err := path.Match(p, "probe"); err != nil {
			return fmt.Errorf("rule %s: invalid pattern %q: %w", r.ID, p, err)
		}
	}
	switch r.Action {
	case ActionAsk:
		if r.Dest != "" {
			return fmt.Errorf("rule %s: an ask rule has no destination", r.ID)
		}
	case ActionRoute:
		if err := validateDest(r.Dest); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	default:
		return fmt.Errorf("rule %s: unknown action %q", r.ID, r.Action)
	}
	return nil
}

// validateRuleDest rejects anything the sandbox would reject later: absolute
// paths, traversal, and Windows drive letters or UNC prefixes.
func validateDest(dest string) error {
	if dest == "" {
		return fmt.Errorf("destination is required")
	}
	if strings.ContainsRune(dest, '\\') {
		return fmt.Errorf("destination must use forward slashes")
	}
	if strings.HasPrefix(dest, "/") {
		return fmt.Errorf("destination must be relative to the workspace")
	}
	if len(dest) >= 2 && dest[1] == ':' {
		return fmt.Errorf("destination must be relative to the workspace")
	}
	for _, seg := range strings.Split(dest, "/") {
		if seg == ".." {
			return fmt.Errorf("destination must not leave the workspace")
		}
	}
	return nil
}

// Matches reports whether the rule applies to a file of the given base name
// saved from the given provider.
func (r *Rule) Matches(provider, name string) bool {
	if !strings.EqualFold(r.Source, SourceAny) && !strings.EqualFold(r.Source, provider) {
		return false
	}
	for _, p := range r.Patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// ShadowedBy reports, for each rule, the index of the earlier rule that
// makes it unreachable, or -1. A rule is shadowed when an earlier rule
// accepts the same source and every one of its patterns — the Shadowed
// board's "NEVER MATCHES". Fylane surfaces this and stops there: it never
// reorders or deletes on the user's behalf.
func ShadowedBy(rules []Rule) []int {
	out := make([]int, len(rules))
	for i := range rules {
		out[i] = -1
		for j := 0; j < i; j++ {
			if shadows(&rules[j], &rules[i]) {
				out[i] = j
				break
			}
		}
	}
	return out
}

func shadows(earlier, later *Rule) bool {
	if !strings.EqualFold(earlier.Source, SourceAny) && !strings.EqualFold(earlier.Source, later.Source) {
		return false
	}
	for _, p := range later.Patterns {
		if !patternCovered(earlier.Patterns, p) {
			return false
		}
	}
	return true
}

// patternCovered reports whether one of the earlier patterns swallows the
// later pattern. Exact equality always counts; beyond that only the common
// "*.ext" shape is compared, because deciding glob containment in general is
// undecidable and a wrong "never matches" badge is worse than a missing one.
func patternCovered(earlier []string, later string) bool {
	for _, e := range earlier {
		if e == later {
			return true
		}
		if e == "*" {
			return true
		}
		if strings.HasPrefix(e, "*.") && !strings.ContainsAny(e[2:], "*?[") &&
			strings.HasPrefix(later, "*.") && e == later {
			return true
		}
	}
	return false
}

// Outcome is what the rule table decides about one file of a save.
type Outcome struct {
	// Path is where the file should land, workspace-relative. Unchanged
	// when no rule routed it.
	Path string
	// Ask forces local confirmation even where the policy would have let
	// the write through. It never grants the opposite.
	Ask bool
	// RuleID is the rule that matched, empty when none did. Carried so the
	// caller can credit the rule's use count once the change set lands.
	RuleID string
}

// Apply runs the table against one requested path and returns where the file
// goes. The first matching rule wins and matching stops there — the order on
// the Route rules screen is the priority, and Fylane never reorders it.
//
// A route rule replaces the *directory* and keeps the file's own name: the
// rules answer "where does this kind of file live", not "what is it called".
// The resulting path is still validated by the sandbox on the way to disk;
// a rule can misfile a save inside the workspace but can never leave it.
func Apply(rules []Rule, provider, requested string) Outcome {
	base := path.Base(requested)
	for i := range rules {
		r := &rules[i]
		if !r.Matches(provider, base) {
			continue
		}
		switch r.Action {
		case ActionAsk:
			return Outcome{Path: requested, Ask: true, RuleID: r.ID}
		case ActionRoute:
			// Validate rejects an escaping destination when the rule is
			// saved. A settings file edited by hand could still carry one,
			// and joining it would produce a path the sandbox has to refuse
			// — so an unusable rule is treated as no match at all and the
			// table carries on below it.
			if validateDest(r.Dest) != nil {
				continue
			}
			return Outcome{Path: path.Join(r.Dest, base), RuleID: r.ID}
		}
	}
	return Outcome{Path: requested}
}

// CountUse credits a rule for one applied save. It returns false when the id
// is unknown, so a caller does not silently persist an unchanged table.
func CountUse(rules []Rule, id string) bool {
	for i := range rules {
		if rules[i].ID == id {
			rules[i].Uses++
			return true
		}
	}
	return false
}
