package routerule

import (
	"strings"
	"testing"
)

func TestRuleValidateRejectsEscapingDestinations(t *testing.T) {
	base := Rule{ID: "r1", Source: "claude", Patterns: []string{"*.ts"}, Action: ActionRoute}
	for _, dest := range []string{
		"/etc",
		"../outside",
		"docs/../../outside",
		`C:\Windows`,
		`docs\notes`,
		"",
	} {
		r := base
		r.Dest = dest
		if err := r.Validate(); err == nil {
			t.Errorf("dest %q accepted, want rejection", dest)
		}
	}
	ok := base
	ok.Dest = "ai-workspace/src/"
	if err := ok.Validate(); err != nil {
		t.Errorf("valid dest rejected: %v", err)
	}
}

func TestRuleValidateChecksActionAndPatterns(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want bool // want error
	}{
		{"no id", Rule{Source: "any", Patterns: []string{"*"}, Action: ActionAsk}, true},
		{"no source", Rule{ID: "r", Patterns: []string{"*"}, Action: ActionAsk}, true},
		{"no patterns", Rule{ID: "r", Source: "any", Action: ActionAsk}, true},
		{"blank pattern", Rule{ID: "r", Source: "any", Patterns: []string{" "}, Action: ActionAsk}, true},
		{"bad glob", Rule{ID: "r", Source: "any", Patterns: []string{"[a-"}, Action: ActionAsk}, true},
		{"unknown action", Rule{ID: "r", Source: "any", Patterns: []string{"*"}, Action: "delete"}, true},
		{"ask with dest", Rule{ID: "r", Source: "any", Patterns: []string{"*"}, Action: ActionAsk, Dest: "x/"}, true},
		{"ask", Rule{ID: "r", Source: "any", Patterns: []string{"*.bak"}, Action: ActionAsk}, false},
	}
	for _, c := range cases {
		err := c.rule.Validate()
		if (err != nil) != c.want {
			t.Errorf("%s: err = %v, want error = %v", c.name, err, c.want)
		}
	}
}

func TestRuleMatches(t *testing.T) {
	r := Rule{ID: "r", Source: "claude", Patterns: []string{"*.tsx", "*.ts"}, Dest: "src/", Action: ActionRoute}
	if !r.Matches("claude", "auth.ts") {
		t.Error("claude/auth.ts should match")
	}
	if r.Matches("chatgpt", "auth.ts") {
		t.Error("other provider should not match")
	}
	if r.Matches("claude", "notes.md") {
		t.Error("other extension should not match")
	}
	any := Rule{ID: "a", Source: SourceAny, Patterns: []string{"*.md"}, Dest: "notes/", Action: ActionRoute}
	if !any.Matches("grok", "PRD.md") {
		t.Error("ANY should match every provider")
	}
}

func TestShadowedBy(t *testing.T) {
	rules := []Rule{
		{ID: "1", Source: SourceAny, Patterns: []string{"*.tsx", "*.ts", "*.json"}, Dest: "src/", Action: ActionRoute},
		{ID: "2", Source: "grok", Patterns: []string{"*.png"}, Dest: "assets/", Action: ActionRoute},
		{ID: "3", Source: "claude", Patterns: []string{"*.json"}, Dest: "config/", Action: ActionRoute},
	}
	got := ShadowedBy(rules)
	if got[0] != -1 || got[1] != -1 {
		t.Fatalf("independent rules reported shadowed: %v", got)
	}
	if got[2] != 0 {
		t.Fatalf("rule 3 should be shadowed by rule 1, got %v", got)
	}

	// A narrower earlier rule must not be reported as shadowing.
	narrow := []Rule{
		{ID: "1", Source: "claude", Patterns: []string{"*.ts"}, Dest: "src/", Action: ActionRoute},
		{ID: "2", Source: SourceAny, Patterns: []string{"*.ts"}, Dest: "other/", Action: ActionRoute},
	}
	if ShadowedBy(narrow)[1] != -1 {
		t.Error("provider-specific rule must not shadow an ANY rule")
	}
}

func TestApplyFirstMatchWins(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Source: "claude", Patterns: []string{"*.ts"}, Dest: "src/", Action: ActionRoute},
		{ID: "r2", Source: SourceAny, Patterns: []string{"*.ts"}, Dest: "elsewhere/", Action: ActionRoute},
		{ID: "r3", Source: SourceAny, Patterns: []string{"*.md"}, Dest: "docs/notes/", Action: ActionRoute},
		{ID: "r4", Source: SourceAny, Patterns: []string{"*.env", "secrets*"}, Action: ActionAsk},
	}

	// The rule the user put first is the one that applies, and the file
	// keeps its own name: a rule chooses the folder, not the file name.
	got := Apply(rules, "claude", "ai-answers/auth.ts")
	if got.Path != "src/auth.ts" || got.RuleID != "r1" || got.Ask {
		t.Fatalf("claude/auth.ts = %+v", got)
	}
	// Same file from another source falls to the ANY rule below it.
	if got := Apply(rules, "chatgpt", "ai-answers/auth.ts"); got.Path != "elsewhere/auth.ts" || got.RuleID != "r2" {
		t.Fatalf("chatgpt/auth.ts = %+v", got)
	}
	if got := Apply(rules, "grok", "ai-answers/plan.md"); got.Path != "docs/notes/plan.md" {
		t.Fatalf("plan.md = %+v", got)
	}
	// An ask rule relocates nothing; it only stops the write for a decision.
	if got := Apply(rules, "grok", "config/.env"); got.Path != "config/.env" || !got.Ask || got.RuleID != "r4" {
		t.Fatalf(".env = %+v", got)
	}
	// Nothing matched: the requested path is left exactly as it came.
	if got := Apply(rules, "grok", "ai-answers/photo.png"); got.Path != "ai-answers/photo.png" || got.Ask || got.RuleID != "" {
		t.Fatalf("photo.png = %+v", got)
	}
	if got := Apply(nil, "grok", "a/b.md"); got.Path != "a/b.md" {
		t.Fatalf("empty table = %+v", got)
	}
}

func TestApplyCannotLeaveTheWorkspace(t *testing.T) {
	// Validate rejects an escaping destination when the rule is saved, so
	// this is the second line: even if one were smuggled into the settings
	// file by hand, joining keeps the result inside the workspace.
	rules := []Rule{{ID: "r", Source: SourceAny, Patterns: []string{"*"}, Dest: "../../etc", Action: ActionRoute}}
	got := Apply(rules, "claude", "notes.md")
	if got.Path != "notes.md" || got.RuleID != "" {
		t.Fatalf("an unusable rule must not route: %+v", got)
	}
	if strings.Contains(got.Path, "..") {
		t.Fatalf("path escaped: %q", got.Path)
	}
	if err := (&rules[0]).Validate(); err == nil {
		t.Fatal("an escaping destination must not validate")
	}
}

func TestCountUse(t *testing.T) {
	rules := []Rule{{ID: "r1"}, {ID: "r2"}}
	if !CountUse(rules, "r2") || rules[1].Uses != 1 {
		t.Fatalf("r2 not credited: %+v", rules)
	}
	if CountUse(rules, "gone") {
		t.Error("an unknown id must report that nothing changed")
	}
	if rules[0].Uses != 0 {
		t.Error("crediting one rule must not touch another")
	}
}
