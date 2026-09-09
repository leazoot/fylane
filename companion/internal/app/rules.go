package app

import (
	"fmt"

	"github.com/leazoot/fylane/companion/internal/routerule"
)

// Route rules are user preference, so they live in config.json beside the
// other settings rather than in a table (see internal/routerule for the
// matching semantics).

// LoadRules returns the stored route rules in priority order.
func LoadRules(dataDir string) ([]routerule.Rule, error) {
	s, err := loadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	return s.Rules, nil
}

// SaveRules replaces the whole rule list. Replacing wholesale (rather than
// patching one rule) keeps order — which is the priority — under the
// caller's control, and makes reordering a single atomic write.
func SaveRules(dataDir string, rules []routerule.Rule) error {
	seen := map[string]bool{}
	for i := range rules {
		if err := rules[i].Validate(); err != nil {
			return err
		}
		if seen[rules[i].ID] {
			return fmt.Errorf("duplicate rule id %s", rules[i].ID)
		}
		seen[rules[i].ID] = true
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	s.Rules = rules
	return saveSettings(dataDir, s)
}

// CountRuleUse increments the routed-save counter for one rule. A missing
// rule is not an error: the rule may have been deleted between the save
// being composed and the change set landing.
func CountRuleUse(dataDir, ruleID string) error {
	if ruleID == "" {
		return nil
	}
	s, err := loadSettings(dataDir)
	if err != nil {
		return err
	}
	for i := range s.Rules {
		if s.Rules[i].ID == ruleID {
			s.Rules[i].Uses++
			return saveSettings(dataDir, s)
		}
	}
	return nil
}

// RuleStore adapts the settings file to the control API's rule interface.
type RuleStore struct{ DataDir string }

func (r RuleStore) Load() ([]routerule.Rule, error) { return LoadRules(r.DataDir) }

func (r RuleStore) Save(rules []routerule.Rule) error { return SaveRules(r.DataDir, rules) }
