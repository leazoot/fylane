package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/autostart"
	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/readbox"
)

func testPrefs(t *testing.T) *prefs {
	t.Helper()
	p := newPrefs(t.TempDir(), readbox.New(true))
	// The login entry must land in the test's own home, not the developer's.
	p.autostartCfg = autostart.Config{Home: t.TempDir(), Exe: "/opt/fylane/fylane-companion", Args: []string{"serve"}}
	t.Cleanup(func() { _ = autostart.Set(p.autostartCfg, false) })
	return p
}

func TestPrefsStartAtTheDefaultsTheCoreIsActuallyRunning(t *testing.T) {
	got := testPrefs(t).Prefs()
	if want := int(cmdexec.DefaultTimeout / time.Second); got.TaskTimeoutSeconds != want {
		t.Errorf("task timeout %d, want the engine default %d", got.TaskTimeoutSeconds, want)
	}
	// Being able to stop something you started is the default; the setting
	// exists to turn it off, not to turn it on.
	if !got.AllowStopTasks {
		t.Error("stopping a running task is off before the user chose anything")
	}
	if got.Autostart.Enabled {
		t.Error("a fresh profile already starts Fylane at login")
	}
}

func TestPrefsChangeOnlyWhatThePatchNames(t *testing.T) {
	p := testPrefs(t)
	off := false
	if _, err := p.SetPrefs(ctlapi.PrefPatch{AllowStopTasks: &off}); err != nil {
		t.Fatal(err)
	}
	secs := 300
	got, err := p.SetPrefs(ctlapi.PrefPatch{TaskTimeoutSeconds: &secs})
	if err != nil {
		t.Fatal(err)
	}
	// A page that flips one switch must not silently restore the others.
	if got.AllowStopTasks {
		t.Error("changing the timeout put the stop button back")
	}
	if got.TaskTimeoutSeconds != 300 {
		t.Errorf("task timeout %d, want 300", got.TaskTimeoutSeconds)
	}
	if reread := p.Prefs(); reread != got {
		t.Errorf("re-reading gave %+v, want %+v", reread, got)
	}
}

func TestPrefsRefuseATimeoutTheSettingsPageDoesNotOffer(t *testing.T) {
	p := testPrefs(t)
	// A free-form number would let a caller pin a process open for as long as
	// it liked while looking like a user preference.
	for _, secs := range []int{0, 5, 3600} {
		v := secs
		if _, err := p.SetPrefs(ctlapi.PrefPatch{TaskTimeoutSeconds: &v}); err == nil {
			t.Errorf("a %d second timeout was accepted", secs)
		}
	}
}

func TestTaskTimeoutReadsWhatWasStored(t *testing.T) {
	p := testPrefs(t)
	secs := 30
	if _, err := p.SetPrefs(ctlapi.PrefPatch{TaskTimeoutSeconds: &secs}); err != nil {
		t.Fatal(err)
	}
	// The tools read this per call, so a change has to be visible without a
	// restart.
	if got := TaskTimeout(p.dataDir); got != 30*time.Second {
		t.Errorf("TaskTimeout = %v, want 30s", got)
	}
}

func TestAutostartRoundTripsThroughTheSettings(t *testing.T) {
	p := testPrefs(t)
	on := true
	got, err := p.SetPrefs(ctlapi.PrefPatch{Autostart: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Autostart.Enabled {
		t.Fatal("turning start-at-login on did not read back as on")
	}
	off := false
	if got, err = p.SetPrefs(ctlapi.PrefPatch{Autostart: &off}); err != nil {
		t.Fatal(err)
	}
	if got.Autostart.Enabled {
		t.Fatal("turning start-at-login off left it on")
	}
}

// The read boundary reads false only when the user wrote false. Everything
// else — no setting, no file, an unreadable file — leaves it on, because the
// safe reading of "we could not tell" is the one that keeps the boundary.
func TestTheReadBoundaryStaysOnUnlessItWasTurnedOff(t *testing.T) {
	cases := []struct {
		name string
		body string // empty means write no file at all
		want bool
	}{
		{"no settings file", "", true},
		{"settings without the field", `{"mode":"relay"}`, true},
		{"turned off", `{"read_boundary":false}`, false},
		{"turned on", `{"read_boundary":true}`, true},
		{"unreadable file", `{ this is not json`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.body != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := ReadBoundaryEnabled(dir); got != tc.want {
				t.Fatalf("ReadBoundaryEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTurningTheReadBoundaryOffReachesTheRunningCoreAndNotOnlyTheFile(t *testing.T) {
	dir := t.TempDir()
	box := readbox.New(true)
	p := newPrefs(dir, box)
	p.autostartCfg = autostart.Config{Home: t.TempDir(), Exe: "/opt/fylane/fylane-companion", Args: []string{"serve"}}

	// The page reports the boundary that is running, never the setting on
	// its own: those two disagree on a machine that cannot enforce one.
	if got, want := p.Prefs().ReadBoundary.State, string(box.State()); got != want {
		t.Fatalf("reported %q while the Core is %q", got, want)
	}

	off := false
	doc, err := p.SetPrefs(ctlapi.PrefPatch{ReadBoundary: &off})
	if err != nil {
		t.Fatal(err)
	}
	// Written down, so a restart keeps it.
	if ReadBoundaryEnabled(dir) {
		t.Error("the setting file still says the boundary is on")
	}
	// And applied, so the next child started is not bounded by a boundary
	// the user just turned off. A patch that only reached the file would
	// leave this box enforcing until the process was restarted.
	if box.Enforcing() {
		t.Error("the running boundary is still enforcing after being turned off")
	}
	if doc.ReadBoundary.State == string(readbox.Enforced) {
		t.Errorf("state = %q after being turned off", doc.ReadBoundary.State)
	}
	if doc.ReadBoundary.Detail == "" {
		t.Error("no sentence saying why; the page has nothing to show")
	}

	// Turning it back on is the same trip in reverse, and it must not need a
	// restart either.
	on := true
	back, err := p.SetPrefs(ctlapi.PrefPatch{ReadBoundary: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !ReadBoundaryEnabled(dir) {
		t.Error("the setting file did not record the boundary going back on")
	}
	if back.ReadBoundary.State != string(box.State()) {
		t.Errorf("reported %q while the Core is %q", back.ReadBoundary.State, box.State())
	}
}

func TestAPrefsPatchThatDoesNotNameTheBoundaryLeavesItAlone(t *testing.T) {
	dir := t.TempDir()
	box := readbox.New(true)
	p := newPrefs(dir, box)
	p.autostartCfg = autostart.Config{Home: t.TempDir(), Exe: "/opt/fylane/fylane-companion", Args: []string{"serve"}}

	before := box.State()
	secs := 300
	if _, err := p.SetPrefs(ctlapi.PrefPatch{TaskTimeoutSeconds: &secs}); err != nil {
		t.Fatal(err)
	}
	// Changing how patient the user is must not touch a defence.
	if box.State() != before {
		t.Errorf("the boundary moved to %s while only the timeout was set", box.State())
	}
	if !ReadBoundaryEnabled(dir) {
		t.Error("setting the timeout turned the boundary off in the file")
	}
}
