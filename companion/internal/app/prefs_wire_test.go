package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/ctlapi"
)

// The settings page offers a fixed set of timeouts and the Core refuses
// anything outside it. The two lists are written in different languages on
// opposite sides of the control API, so nothing but this test makes them agree
// — and when they disagree the failure is silent from the desktop's side: the
// page offers a value, the user picks it, and the save is rejected by a Core
// that never explains which values it would have taken.
//
// This is the same shape as the defect the V3 walkthrough turned up (a panel
// reading a connection off a setting neither side had agreed on), which is why
// it is worth a test rather than a comment.

var tsBoundaryStates = regexp.MustCompile(`(?m)^export type ReadBoundaryState = ([^;]+);`)

var tsTimeouts = regexp.MustCompile(`(?m)^export const TIMEOUTS = \[([0-9,\s]+)\];`)

func TestTheSettingsPageOffersExactlyWhatTheCoreAccepts(t *testing.T) {
	path := filepath.Join("..", "..", "..", "desktop", "frontend", "src", "lib", "settings.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		// The desktop is part of this repository; a missing file is a moved
		// file, and this test is the thing that would silently stop guarding.
		t.Fatalf("reading the desktop's timeout list: %v", err)
	}

	m := tsTimeouts.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no `export const TIMEOUTS = [...]` in %s — if it was renamed, rename it here too", path)
	}

	var got []int
	for _, field := range strings.Split(string(m[1]), ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("timeout %q in %s is not a number", field, path)
		}
		got = append(got, n)
	}

	if fmt.Sprint(got) != fmt.Sprint(TaskTimeouts) {
		t.Errorf("the settings page offers %v, the Core accepts %v — picking one the Core refuses fails with nothing on screen to explain it", got, TaskTimeouts)
	}
}

// The default is not one of the offered values (50s, chosen to fit the
// tightest platform synchronous-call budget). That is a real inconsistency and
// it is written up as a decision rather than settled here.
// What this test pins is the part that is not in question: whatever the Core
// falls back to, it has to be something the desktop can display, and the
// desktop has to be able to move away from it in both directions.
func TestTheFallbackTimeoutIsReachableFromTheSettingsPage(t *testing.T) {
	fallback := taskTimeoutOf(settings{})
	if fallback <= 0 {
		t.Fatalf("fallback timeout %d is not a duration anyone can be shown", fallback)
	}
	var below, above bool
	for _, v := range TaskTimeouts {
		if v < fallback {
			below = true
		}
		if v > fallback {
			above = true
		}
	}
	if validTimeout(fallback) {
		// Once the decision lands this way, the two directions below stop
		// mattering: the fallback is simply one of the offered values.
		return
	}
	if !below || !above {
		t.Errorf("fallback %ds is outside %v with no neighbour on one side, so the stepper would dead-end on it", fallback, TaskTimeouts)
	}
}

// The inconsistency this batch found: the Core reported a fallback timeout it
// would itself refuse, so the settings page had a control with no current
// value and no way back to the value in force. Whichever way the trade is
// finally recorded, a machine has to be able to stay on what it is running.
func TestWhatTheCoreFallsBackToCanAlsoBeChosen(t *testing.T) {
	p := testPrefs(t)
	fallback := taskTimeoutOf(settings{})
	if _, err := p.SetPrefs(ctlapi.PrefPatch{TaskTimeoutSeconds: &fallback}); err != nil {
		t.Errorf("the Core runs on %ds with nothing stored but refuses to store %ds: %v", fallback, fallback, err)
	}
	if got := TaskTimeout(p.dataDir); got != time.Duration(fallback)*time.Second {
		t.Errorf("TaskTimeout = %v after choosing the fallback; want %ds", got, fallback)
	}
}

// The read boundary's three states are written twice as well: once as Go
// constants and once as a TypeScript union. They fail the same silent way the
// timeouts do, one step worse — a state word the page has never heard of
// renders as neither branch, so a machine that cannot enforce a boundary would
// draw the row belonging to one that can.
func TestThePageKnowsEveryStateTheBoundaryCanReport(t *testing.T) {
	path := filepath.Join("..", "..", "..", "desktop", "frontend", "src", "lib", "core.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the desktop's boundary states: %v", err)
	}
	m := tsBoundaryStates.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no `export type ReadBoundaryState = ...` in %s — if it was renamed, rename it here too", path)
	}

	got := map[string]bool{}
	for _, field := range strings.Split(string(m[1]), "|") {
		got[strings.Trim(strings.TrimSpace(field), `"`)] = true
	}
	want := declaredConsts(t, filepath.Join("..", "readbox", "readbox.go"), "Enforced")
	for _, state := range want {
		if !got[state] {
			t.Errorf("the Core can report %q and the settings page has no branch for it", state)
		}
		delete(got, state)
	}
	for state := range got {
		t.Errorf("the settings page draws %q, which the Core never reports", state)
	}
}
