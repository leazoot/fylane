// Package redact masks credential-shaped text in command output before that
// output leaves this machine.
//
// It exists because of a real incident: a model ran
// `ps -eo pid,etime,stat,comm,args` — a command that changes nothing, so the
// rule table allowed it — and 944 lines of process table went to a platform,
// carrying another process's Cloudflare tunnel token in its command line.
// Secrets on command lines and in environment dumps are ordinary practice, so
// any command that reports machine state can carry one.
//
// What this package is: a last line of defence on the way out. What it is
// not: a guarantee. Pattern matching cannot recognise an unknown secret
// format, and a credential split across two polling windows is only ever seen
// half at a time. The rule table asking first (cmdrule.Disclose) is the
// control that matters; this catches what slips through it.
//
// Masking is deliberately visible. Replacing a token with something
// plausible-looking would let a model reason about a value that is not real;
// `[redacted]` tells it plainly that something was withheld.
package redact

import (
	"regexp"
	"strings"
)

// Placeholder replaces every masked value. One fixed string, so a reader
// never has to wonder whether a difference in the marker means something.
const Placeholder = "[redacted]"

// secretShapes are matched in order. Each pattern must anchor on a prefix
// distinctive enough that ordinary prose cannot trip it: the cost of a false
// positive here is a developer staring at `[redacted]` where their own build
// output should be, which erodes trust in the whole audit surface.
var secretShapes = []*regexp.Regexp{
	// PEM private key blocks, including the body: the header alone is not
	// the secret, and leaving the body would defeat the point.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// Base64-encoded JSON objects ("eyJ..."): JWTs and, as in the incident
	// that prompted this package, Cloudflare tunnel tokens.
	regexp.MustCompile(`eyJ[A-Za-z0-9+/=_-]{20,}`),
	// Provider key formats with unambiguous prefixes.
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bsk-(?:ant-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`),
	// Authorization headers echoed into logs.
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),
}

// namedSecretFlag matches an option whose *name* says it carries a secret,
// together with the value that follows it — `--token abc`, `--token=abc`,
// `--password=abc`. The name is the signal, so this catches formats no
// prefix pattern knows about, which is most of them.
var namedSecretFlag = regexp.MustCompile(
	`(?i)(--?[a-z0-9-]*(?:token|password|passwd|secret|api[-_]?key|apikey|credential|auth)[a-z0-9-]*)([=\s]+)(\S+)`)

// assignedSecret matches KEY=value where the key names a secret, which is
// how an environment dump spells the same thing.
var assignedSecret = regexp.MustCompile(
	`(?i)\b([A-Z0-9_]*(?:TOKEN|PASSWORD|PASSWD|SECRET|API_?KEY|APIKEY|CREDENTIALS?|PRIVATE_KEY)[A-Z0-9_]*)(=)([^\s]+)`)

// Text returns s with credential-shaped substrings replaced by Placeholder.
// It is safe to call on text that contains no secrets and on empty input.
func Text(s string) string {
	if s == "" {
		return s
	}
	for _, re := range secretShapes {
		s = re.ReplaceAllString(s, Placeholder)
	}
	s = namedSecretFlag.ReplaceAllString(s, "${1}${2}"+Placeholder)
	s = assignedSecret.ReplaceAllString(s, "${1}="+Placeholder)
	return s
}

// Argv masks the same shapes in a command line. The audit log keeps what was
// actually run, so this is for argv that is about to be shown to a caller
// rather than stored locally.
func Argv(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = Text(a)
	}
	// A value in its own argument — `--token`, then `abc` — has no delimiter
	// for the text patterns to anchor on, so it is handled positionally.
	for i := 0; i+1 < len(out); i++ {
		if isSecretFlagName(argv[i]) && !strings.HasPrefix(argv[i+1], "-") {
			out[i+1] = Placeholder
		}
	}
	return out
}

var secretFlagName = regexp.MustCompile(
	`(?i)^--?[a-z0-9-]*(?:token|password|passwd|secret|api[-_]?key|apikey|credential|auth)[a-z0-9-]*$`)

func isSecretFlagName(arg string) bool { return secretFlagName.MatchString(arg) }
