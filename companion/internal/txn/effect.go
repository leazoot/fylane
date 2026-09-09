package txn

import (
	"errors"
	"os"
)

// Effect is what a change set actually did to one path when it did not
// complete. It is the per-path half of the answer whose whole-result half is
// nextstep.Reobserve.
//
// Until this existed, a failed change set said one thing about every path it
// touched: the disk state is not known from the result. That was true of the
// *result* and not of the *world* — a Companion holds the before hash of
// every path it planned against, the intended after hash, and a backup, so
// for most paths it can simply look and say. The three values below are what
// looking can conclude, and the third one is the reason the other two are
// trustworthy: where the engine cannot tell, it says so rather than
// averaging.
//
// The vocabulary is closed and there is no "probably". A caller reading
// StateChanged has to be able to act on it without a second confirmation, and
// that is only true if the value is never used to mean "likely".
type Effect string

const (
	// NotStarted: the path is exactly as it was before the change set ran.
	// Either the operation was never reached, or it was applied and the
	// restore put it back. Both are the same fact about the world, and it is
	// the world the caller is asking about.
	NotStarted Effect = "not_started"
	// StateChanged: the operation landed and is still there. A failed change
	// set can leave this behind when the restore itself failed.
	StateChanged Effect = "state_changed"
	// OutcomeUnknown: the path is in neither the before state nor the after
	// state, or could not be read at all. This is the honest answer, not a
	// fallback: a directory that was deleted and partially restored is
	// genuinely in neither state, and no amount of hashing will make it one.
	OutcomeUnknown Effect = "outcome_unknown"
)

// Valid reports whether e is one of the three. The empty Effect is not: a
// path the engine forgot to reconcile and a path it reconciled as unknown are
// different failures and must not print the same.
func (e Effect) Valid() bool {
	switch e {
	case NotStarted, StateChanged, OutcomeUnknown:
		return true
	}
	return false
}

// ErrEffectUnset is returned rather than serialized when an effect was never
// decided. It is deliberately an error and not an omission: a missing entry
// reads as "nothing to report", which is the one thing an undecided path does
// not mean.
var ErrEffectUnset = errors.New("an operation's effect was never decided")

// OpEffect pairs a path with what happened to it.
type OpEffect struct {
	Path   string `json:"path"`
	Effect Effect `json:"effect" jsonschema:"not_started | state_changed | outcome_unknown"`
}

// reconcile reads the disk and decides, for every operation in the plan, which
// of the three happened. It runs after a change set has failed, when the undo
// chain has already done whatever it could.
//
// Every branch below assigns a value, so an operation cannot come out of here
// without one; the caller checks Valid anyway, because "cannot happen" is how
// F29 got in.
func reconcile(plan []*plannedOp) []OpEffect {
	effects := make([]OpEffect, 0, len(plan))
	for _, p := range plan {
		effects = append(effects, OpEffect{Path: p.resultPath(), Effect: effectOf(p)})
	}
	return effects
}

// notStartedEffects is reconcile's answer for a change set that failed before
// touching the disk at all. It is stated rather than measured because there
// is nothing to measure: no operation ran.
func notStartedEffects(plan []*plannedOp) []OpEffect {
	effects := make([]OpEffect, 0, len(plan))
	for _, p := range plan {
		effects = append(effects, OpEffect{Path: p.resultPath(), Effect: NotStarted})
	}
	return effects
}

func effectOf(p *plannedOp) Effect {
	switch p.op.Type {
	case OpCreate:
		// Before: absent. After: present at afterSHA.
		switch sum, state := hashNow(p.abs); state {
		case absent:
			return NotStarted
		case readable:
			if sum == p.afterSHA {
				return StateChanged
			}
			return OutcomeUnknown
		default:
			return OutcomeUnknown
		}

	case OpUpdate:
		sum, state := hashNow(p.abs)
		if state != readable {
			return OutcomeUnknown
		}
		switch sum {
		case p.beforeSHA:
			return NotStarted
		case p.afterSHA:
			return StateChanged
		}
		return OutcomeUnknown

	case OpMove:
		fromSum, fromState := hashNow(p.abs)
		toSum, toState := hashNow(p.toAbs)
		if fromState == readable && fromSum == p.beforeSHA && toState == absent {
			return NotStarted
		}
		if fromState == absent && toState == readable && toSum == p.afterSHA {
			return StateChanged
		}
		return OutcomeUnknown

	case OpDelete:
		if p.isDir {
			// A directory delete is restored by copying a tree back, and a
			// tree that is present again is not thereby proved complete.
			// Absent is decidable; present is not, and saying NotStarted
			// here would be exactly the confident wrong answer this type
			// exists to prevent.
			// Lstat, not hashNow: reading a directory fails for a reason
			// that is not "missing", and this branch must not depend on
			// which error that happens to be.
			if _, err := os.Lstat(p.abs); os.IsNotExist(err) {
				return StateChanged
			}
			return OutcomeUnknown
		}
		sum, state := hashNow(p.abs)
		if state == absent {
			return StateChanged
		}
		if state == readable && sum == p.beforeSHA {
			return NotStarted
		}
		return OutcomeUnknown
	}
	// An operation type this function has not been taught about. Reporting
	// unknown is the only answer that is not a guess.
	return OutcomeUnknown
}

// pathState separates the two things hashFile folds into an empty string.
// "It is not there" and "I could not read it" are opposite conclusions for a
// caller deciding whether a delete landed.
type pathState int

const (
	absent pathState = iota
	readable
	unreadable
)

func hashNow(path string) (string, pathState) {
	data, err := os.ReadFile(path)
	if err == nil {
		return hashBytes(data), readable
	}
	if os.IsNotExist(err) {
		return "", absent
	}
	return "", unreadable
}
