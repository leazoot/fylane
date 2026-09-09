package app

import (
	"fmt"
	"time"

	"github.com/leazoot/fylane/companion/internal/autostart"
	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/readbox"
)

// The settings page's execution preferences. They are stored in config.json
// beside the rest of the user-level settings and read back by the Core, so a
// restart keeps them and any surface — not only the window that set them —
// sees the same answer.
//
// None of them moves the approval line. The rung, the rule table, the sandbox
// and the audit log are unaffected by anything here: a timeout bounds
// how long a command may run, not whether it was allowed to run. The read
// boundary is the exception to "convenience only" and not to that rule — it
// adds a layer above those checks, so turning it off removes the layer and
// lifts none of the checks.

// TaskTimeouts are the choices the settings page offers, in seconds. The page
// offers a fixed set rather than a free number because the question being
// answered is "how patient am I", and a short list covers it.
//
// 50 is here because it is what a machine with no stored preference already
// runs on: cmdexec.DefaultTimeout is 50s, chosen to sit under the tightest
// platform budget for a synchronous tool call, and taskTimeoutOf falls back to
// it. Leaving it out made the reader and the writer of one field disagree
// about that field's domain — the Core reported 50 and would have refused 50 —
// so the settings page showed a control with no current value and no way to
// return to the one in force.
//
// Widening the list is the only reading of that inconsistency that changes no
// running behaviour: no command's timeout moves, the value already in use
// simply becomes expressible. Moving DefaultTimeout onto 60 instead would put
// the default *at* the platform budget rather than under it. That trade is
// recorded as a decision; if it lands the other way, this
// slice and TIMEOUTS in desktop/frontend/src/lib/settings.ts are the two
// places to change, and prefs_wire_test.go fails if only one of them moves.
var TaskTimeouts = []int{30, 50, 60, 300}

// prefs implements ctlapi.PrefStore against the data directory.
type prefs struct {
	dataDir string
	// autostartCfg names the executable and arguments the login entry has to
	// reproduce. Empty fields fall back to this process and `serve`.
	autostartCfg autostart.Config
	// box is the running boundary, not a copy of the setting. Reading the
	// state from it rather than from the file is what lets this page say
	// "absent" on a machine that cannot enforce one — the file only ever
	// records what the user asked for.
	box *readbox.Box
}

func newPrefs(dataDir string, box *readbox.Box) *prefs {
	// The Core is what starts at login, and it has to come back on the same
	// data directory: a Companion serving /Volumes/Work must not reboot into
	// an empty default one.
	args := []string{"serve"}
	if def, err := DefaultDataDir(); err != nil || def != dataDir {
		args = append(args, "-data-dir", dataDir)
	}
	return &prefs{dataDir: dataDir, autostartCfg: autostart.Config{Args: args}, box: box}
}

func (p *prefs) Prefs() ctlapi.PrefDoc {
	s, err := loadSettings(p.dataDir)
	if err != nil {
		// An unreadable config is not a reason to refuse to draw the page.
		// The defaults are what the Core is actually running on in that case.
		s = settings{}
	}
	state := autostart.Status(p.autostartCfg)
	return ctlapi.PrefDoc{
		TaskTimeoutSeconds: taskTimeoutOf(s),
		AllowStopTasks:     allowStopOf(s),
		Autostart: ctlapi.AutostartDoc{
			Supported: state.Supported,
			Enabled:   state.Enabled,
			Detail:    state.Detail,
		},
		ReadBoundary: ctlapi.ReadBoundaryDoc{
			State:  string(p.box.State()),
			Detail: p.box.Why(),
		},
	}
}

func (p *prefs) SetPrefs(patch ctlapi.PrefPatch) (ctlapi.PrefDoc, error) {
	s, err := loadSettings(p.dataDir)
	if err != nil {
		return ctlapi.PrefDoc{}, err
	}
	if patch.TaskTimeoutSeconds != nil {
		secs := *patch.TaskTimeoutSeconds
		if !validTimeout(secs) {
			return ctlapi.PrefDoc{}, fmt.Errorf("task timeout must be one of %v seconds", TaskTimeouts)
		}
		s.TaskTimeoutSeconds = secs
	}
	if patch.AllowStopTasks != nil {
		v := *patch.AllowStopTasks
		s.AllowStopTasks = &v
	}
	if patch.Autostart != nil {
		// The login entry is written before the file, so a failure to
		// register does not leave config.json claiming a state the machine
		// is not in. Nothing in config.json records it: the OS is the record.
		if err := autostart.Set(p.autostartCfg, *patch.Autostart); err != nil {
			return ctlapi.PrefDoc{}, err
		}
	}
	if patch.ReadBoundary != nil {
		v := *patch.ReadBoundary
		s.ReadBoundary = &v
		// Applied to the running Core, not only to the file. A machine with
		// no boundary to apply absorbs this without complaint: the answer the
		// page reads back is still "absent", which is the truth, and refusing
		// the write would make a setting fail on the one platform where it
		// changes nothing.
		p.box.SetEnabled(v)
	}
	if err := saveSettings(p.dataDir, s); err != nil {
		return ctlapi.PrefDoc{}, err
	}
	return p.Prefs(), nil
}

func validTimeout(secs int) bool {
	for _, v := range TaskTimeouts {
		if v == secs {
			return true
		}
	}
	return false
}

func taskTimeoutOf(s settings) int {
	if validTimeout(s.TaskTimeoutSeconds) {
		return s.TaskTimeoutSeconds
	}
	return int(cmdexec.DefaultTimeout / time.Second)
}

func allowStopOf(s settings) bool {
	if s.AllowStopTasks == nil {
		return true
	}
	return *s.AllowStopTasks
}

// TaskTimeout reads the user's ceiling as a duration, for the command tools.
// It is read per call rather than captured at startup so a change on the
// settings page applies to the next command, not to the next restart.
func TaskTimeout(dataDir string) time.Duration {
	s, err := loadSettings(dataDir)
	if err != nil {
		return cmdexec.DefaultTimeout
	}
	return time.Duration(taskTimeoutOf(s)) * time.Second
}
