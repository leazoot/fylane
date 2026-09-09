// Package nextstep is the closed vocabulary a tool result uses to say what
// the caller should do next.
//
// Every result already carries an `action` sentence. A sentence is the right
// thing for a person reading the transcript and the wrong thing for a model
// deciding a branch: "call the same tool again with the same change_set_id"
// and "do not retry it without being asked to" are one token apart in tone
// and opposite in meaning. This package adds the machine-decidable half. The
// sentence stays — it explains, this decides.
//
// The vocabulary is closed and deliberately small, and the value it does NOT
// have is the point:
//
// There is no "retry". Every case where re-issuing a Fylane call is safe is
// already named by another value — AskUser is "call again with the same key",
// which is idempotent by change set id or task key, and Wait is "poll". A
// bare retry would be the one value in the table that invites a model to
// replay a side effect, and no result we produce needs it. Comparable tools
// do ship one — a `recovery_kind` with `retry_same` in it — and then have to
// write, in prose, that an unknown outcome is not retry permission and that
// retrying must not be read as an ordinary repeat of an effect. A contract
// that needs a paragraph defending it against its own vocabulary is one value
// too large.
// Leaving the value out is enforced by the compiler; a paragraph is not.
package nextstep

// Step is what the caller should do next. The zero value means "nothing to
// do" and is omitted from the wire — the common case is success, and success
// does not need instructions.
type Step string

const (
	// Wait: the work is still running. Poll task_status with the task id.
	Wait Step = "wait"
	// AskUser: blocked on a local human decision. Calling the same tool
	// again with the same key collects the answer; it does not ask twice.
	AskUser Step = "ask_user"
	// Stop: refused, by the rule table or by the user. Nothing about
	// repeating the call changes the answer.
	Stop Step = "stop"
	// FixInput: the request itself is wrong. Change it and call again.
	FixInput Step = "fix_input"
	// Reconcile: the world moved under the request — the file is no longer
	// at the hash it was planned against. Re-read and regenerate.
	Reconcile Step = "reconcile"
	// Reobserve: the effect is unknown. Something may or may not have
	// landed, so look at the world before deciding anything at all. This is
	// the value that has to exist for a caller to be told the truth, and the
	// reason there is no retry to reach for instead.
	Reobserve Step = "reobserve"
)

// All returns the vocabulary. Order is the wire documentation order.
func All() []Step { return []Step{Wait, AskUser, Stop, FixInput, Reconcile, Reobserve} }

// Valid reports whether s is in the vocabulary. The empty step is not: absent
// and "some value we do not recognise" are different problems.
func (s Step) Valid() bool {
	switch s {
	case Wait, AskUser, Stop, FixInput, Reconcile, Reobserve:
		return true
	}
	return false
}

// EffectKnown reports whether a result carrying this step is telling the
// caller that the disk state is settled. Only Reobserve says it is not.
func (s Step) EffectKnown() bool { return s != Reobserve }

// Schema is the jsonschema description shared by every tool that emits the
// field, so the vocabulary is documented once rather than per tool.
const Schema = "What to do next, as a fixed value: " +
	"wait (still running, poll task_status) | " +
	"ask_user (waiting on a local decision; call again with the same id to collect it) | " +
	"stop (refused; repeating will not change the answer) | " +
	"fix_input (the request is wrong; change it) | " +
	"reconcile (the file changed; re-read it and regenerate) | " +
	"reobserve (the effect is unknown; observe before acting). " +
	"Absent means the call succeeded and nothing further is needed."
