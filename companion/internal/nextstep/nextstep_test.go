package nextstep

import "testing"

// The vocabulary is pinned by value, not by count. Adding a step is a
// deliberate act that has to edit this list, which is the point: the table is
// a contract with every caller, and it must not be possible to grow it while
// reading only the package it lives in.
func TestTheVocabularyIsExactlyThese(t *testing.T) {
	want := []Step{"wait", "ask_user", "stop", "fix_input", "reconcile", "reobserve"}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("vocabulary = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The absence of a retry value is a decision, not an oversight. A future
// reader adding one has to delete this test, and deleting it is a decision
// too — which is exactly the difference between this and writing the reason
// in a doc comment nobody re-reads.
func TestThereIsNoValueThatTellsACallerToRepeatAnEffect(t *testing.T) {
	for _, s := range All() {
		switch s {
		case "retry", "retry_same", "repeat", "again":
			t.Fatalf("%q is in the vocabulary; a caller holding a result whose "+
				"effect is unknown must have no value to reach for that means "+
				"'do it again'", s)
		}
	}
}

func TestOnlyTheVocabularyIsValid(t *testing.T) {
	for _, s := range All() {
		if !s.Valid() {
			t.Errorf("%q is in All but not Valid", s)
		}
	}
	// The empty step is "no instruction", which is a real state on the wire
	// and still not a member: absent and unrecognised are different problems
	// and a caller that conflates them cannot tell success from a Companion
	// too old to answer.
	for _, s := range []Step{"", "RETRY", "Wait", "reobserve ", "unknown"} {
		if s.Valid() {
			t.Errorf("%q reported valid", s)
		}
	}
}

// EffectKnown is the whole safety property in one line: exactly one step says
// the disk state is unknown, and it is the one a caller must not act on
// blindly.
func TestReobserveIsTheOnlyStepThatDisclaimsTheEffect(t *testing.T) {
	for _, s := range All() {
		want := s != Reobserve
		if s.EffectKnown() != want {
			t.Errorf("%q.EffectKnown() = %v, want %v", s, s.EffectKnown(), want)
		}
	}
}

// The schema string is what every platform actually reads, so a step that
// exists but is undocumented is invisible in practice.
func TestEveryStepAppearsInTheSchemaDescription(t *testing.T) {
	for _, s := range All() {
		if !contains(Schema, string(s)) {
			t.Errorf("%q is in the vocabulary but not described in Schema", s)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
