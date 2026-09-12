package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// A conversation on a platform starts from nothing: the model has no idea
// what it, or another platform, did in this workspace yesterday. The
// records this Companion already keeps — every change set, every command —
// say exactly that, so workspace_info answers with where things were left.
//
// The answer is bounded by construction, not by trimming afterwards: a
// fixed number of entries, each field cut at a fixed length, so it costs
// the same in the first conversation and the thousandth.

const (
	// activityEntries is how many of the newest records are listed.
	activityEntries = 6
	// activityFetch is how many of each kind are read to pick those from,
	// and the window the one-sentence summary counts over.
	activityFetch = 10
	// activitySummaryBytes bounds the caller-written summary of a change
	// and the argv of a command.
	activitySummaryBytes = 120
	// activityPaths is how many of a change's paths are named.
	activityPaths = 5
	// activityPathBytes bounds one path.
	activityPathBytes = 120
)

// ActivityJournal reads the records behind workspace_info's last-activity
// answer. Implemented by *store.Store.
type ActivityJournal interface {
	ListChangeSets(ctx context.Context, workspaceID string, limit int) ([]*store.ChangeSet, error)
	ListExecEventsFor(ctx context.Context, workspaceID string, limit int) ([]*store.ExecEvent, error)
}

// activityNow is the clock the "ago" words are measured against.
var activityNow = time.Now

type lastActivity struct {
	At      time.Time       `json:"at"`
	Ago     string          `json:"ago" jsonschema:"How long ago the newest record is, in words."`
	Summary string          `json:"summary" jsonschema:"One sentence: what happened last in this workspace, and how the most recent changes and commands went."`
	Recent  []activityEntry `json:"recent" jsonschema:"The newest changes and commands, newest first. Bounded."`
}

type activityEntry struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind" jsonschema:"change | command"`
	Status   string    `json:"status" jsonschema:"For a change: applied | rolled_back | pending_approval | denied | failed | conflict. For a command: ok | failed | timeout | canceled | refused | interrupted."`
	Summary  string    `json:"summary" jsonschema:"The change as its author described it, or the command line."`
	Paths    []string  `json:"paths,omitempty" jsonschema:"Workspace-relative paths a change touched; sensitive files are not named."`
	Provider string    `json:"provider,omitempty" jsonschema:"Which platform asked for it."`
}

// activity builds the last-activity answer for one local workspace. No
// records means nil: a fresh workspace has nothing to say, and says so by
// omission rather than with an empty object.
func (t *toolset) activity(ctx context.Context, ws *workspace.Workspace) (*lastActivity, error) {
	if t.activityLog == nil {
		return nil, nil
	}
	changes, err := t.activityLog.ListChangeSets(ctx, ws.ID(), activityFetch)
	if err != nil {
		return nil, fmt.Errorf("reading recent changes: %w", err)
	}
	commands, err := t.activityLog.ListExecEventsFor(ctx, ws.ID(), activityFetch)
	if err != nil {
		return nil, fmt.Errorf("reading recent commands: %w", err)
	}
	if len(changes) == 0 && len(commands) == 0 {
		return nil, nil
	}
	entries := make([]activityEntry, 0, len(changes)+len(commands))
	for _, c := range changes {
		entries = append(entries, changeEntry(ws, c))
	}
	for _, e := range commands {
		entries = append(entries, commandEntry(e))
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	out := &lastActivity{At: entries[0].At, Ago: ago(activityNow().Sub(entries[0].At))}
	out.Summary = activitySentence(out.Ago, entries[0], changes, commands)
	if len(entries) > activityEntries {
		entries = entries[:activityEntries]
	}
	out.Recent = entries
	return out, nil
}

func changeEntry(ws *workspace.Workspace, c *store.ChangeSet) activityEntry {
	e := activityEntry{At: c.CreatedAt, Kind: "change", Status: c.Status, Provider: c.Provider,
		Summary: truncateUTF8(strings.TrimSpace(c.Summary), activitySummaryBytes)}
	if !c.AppliedAt.IsZero() {
		e.At = c.AppliedAt
	}
	paths := make([]string, 0, len(c.AfterHashes)+len(c.BeforeHashes))
	seen := map[string]bool{}
	for _, m := range []map[string]string{c.AfterHashes, c.BeforeHashes} {
		for p := range m {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)
	hidden := 0
	for _, p := range paths {
		if ws.Sensitive(p) {
			hidden++
			continue
		}
		if len(e.Paths) < activityPaths {
			e.Paths = append(e.Paths, truncateUTF8(p, activityPathBytes))
		}
	}
	if hidden > 0 {
		e.Paths = append(e.Paths, fmt.Sprintf("(%d sensitive file(s))", hidden))
	}
	if more := len(paths) - hidden - activityPaths; more > 0 {
		e.Paths = append(e.Paths, fmt.Sprintf("(+%d more)", more))
	}
	return e
}

func commandEntry(e *store.ExecEvent) activityEntry {
	return activityEntry{At: e.CreatedAt, Kind: "command", Status: e.Outcome, Provider: e.Provider,
		Summary: truncateUTF8(strings.Join(e.Argv, " "), activitySummaryBytes)}
}

// activitySentence is the one line a model reads even when it reads
// nothing else: when, who, and how the recent work went.
func activitySentence(agoWords string, newest activityEntry, changes []*store.ChangeSet, commands []*store.ExecEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Last activity %s ago", agoWords)
	if newest.Provider != "" {
		fmt.Fprintf(&b, " (%s)", newest.Provider)
	}
	b.WriteString(".")
	if len(changes) > 0 {
		applied, undone, waiting := 0, 0, 0
		for _, c := range changes {
			switch c.Status {
			case txn.StatusApplied:
				applied++
			case txn.StatusRolledBack:
				undone++
			case txn.StatusPending:
				waiting++
			}
		}
		parts := []string{}
		if applied > 0 {
			parts = append(parts, fmt.Sprintf("%d applied", applied))
		}
		if undone > 0 {
			parts = append(parts, fmt.Sprintf("%d rolled back", undone))
		}
		if waiting > 0 {
			parts = append(parts, fmt.Sprintf("%d awaiting approval", waiting))
		}
		if other := len(changes) - applied - undone - waiting; other > 0 {
			parts = append(parts, fmt.Sprintf("%d not applied", other))
		}
		fmt.Fprintf(&b, " Recent changes: %s.", strings.Join(parts, ", "))
	}
	if len(commands) > 0 {
		last := commands[0]
		fmt.Fprintf(&b, " Last command `%s` %s.", truncateUTF8(strings.Join(last.Argv, " "), 60), outcomeWords(last))
	}
	return b.String()
}

func outcomeWords(e *store.ExecEvent) string {
	switch e.Outcome {
	case cmdexec.OutcomeOK:
		return "succeeded"
	case cmdexec.OutcomeFailed:
		return fmt.Sprintf("failed (exit %d)", e.ExitCode)
	case cmdexec.OutcomeTimeout:
		return "timed out"
	case cmdexec.OutcomeCanceled:
		return "was canceled"
	case cmdexec.OutcomeRefused:
		return "was refused"
	case store.OutcomeInterrupted:
		return "was interrupted by a restart"
	}
	return e.Outcome
}

// ago says a duration the way a person would, in one unit.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
