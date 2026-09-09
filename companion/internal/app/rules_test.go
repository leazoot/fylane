package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leazoot/fylane/companion/internal/routerule"
)

func rule(id, dest string) routerule.Rule {
	return routerule.Rule{
		ID: id, Source: routerule.SourceAny, Patterns: []string{"*.md"},
		Dest: dest, Action: routerule.ActionRoute,
	}
}

func TestSaveRulesRoundTripAndRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	rules := []routerule.Rule{
		{ID: "r1", Source: "chatgpt", Patterns: []string{"*.tsx", "*.ts"}, Dest: "src/", Action: routerule.ActionRoute},
		{ID: "r2", Source: routerule.SourceAny, Patterns: []string{"*.bak"}, Action: routerule.ActionAsk},
	}
	if err := SaveRules(dir, rules); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "r1" || got[1].Action != routerule.ActionAsk {
		t.Fatalf("round trip lost data: %+v", got)
	}

	if err := SaveRules(dir, []routerule.Rule{rules[0], rules[0]}); err == nil {
		t.Error("duplicate ids accepted")
	}
	if err := SaveRules(dir, []routerule.Rule{rule("bad", "../x")}); err == nil {
		t.Error("escaping destination accepted")
	}
	// A rejected write must not clobber the stored list.
	after, err := LoadRules(dir)
	if err != nil || len(after) != 2 {
		t.Fatalf("stored rules changed after a rejected write: %+v (%v)", after, err)
	}
}

func TestSaveRulesKeepsOtherSettings(t *testing.T) {
	dir := t.TempDir()
	if err := SaveApprovalMode(dir, "balanced"); err != nil {
		t.Fatal(err)
	}
	if err := SaveRules(dir, []routerule.Rule{rule("r", "x/")}); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.ApprovalMode != "balanced" || len(s.Rules) != 1 {
		t.Fatalf("settings = %+v", s)
	}
	// config.json holds preferences only and must stay owner-only.
	info, err := os.Stat(filepath.Join(dir, settingsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		// Windows has no mode bits and no ACL is set here.
		t.Errorf("config.json mode = %o, want 600", perm)
	}
}

func TestCountRuleUse(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRules(dir, []routerule.Rule{rule("r1", "x/")}); err != nil {
		t.Fatal(err)
	}
	if err := CountRuleUse(dir, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := CountRuleUse(dir, "gone"); err != nil {
		t.Fatalf("deleted rule must not error: %v", err)
	}
	got, err := LoadRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Uses != 1 {
		t.Fatalf("uses = %d, want 1", got[0].Uses)
	}
}
