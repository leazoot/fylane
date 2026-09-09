package cmdexec

import (
	"os"
	"runtime"
)

// allowedEnv is the complete set of variables a command inherits.
//
// It is a whitelist and must stay one. The Companion's own environment can
// hold relay credentials, keychain hints, and whatever the user exported into
// the shell that launched the desktop app — none of which is a build tool's
// business. A denylist would have to predict every name worth hiding; this
// only has to name the ones a toolchain cannot start without.
var allowedEnv = []string{
	// POSIX. HOME matters more than it looks: npm, go, cargo, and pip all
	// put their caches under it, and losing it turns every run into a cold
	// build.
	"HOME", "LANG", "LC_ALL", "LC_CTYPE", "PATH", "TMPDIR", "TZ",

	// Windows cannot start a process without SystemRoot, and its toolchains
	// resolve almost everything else through these.
	"APPDATA", "COMSPEC", "HOMEDRIVE", "HOMEPATH", "LOCALAPPDATA",
	"NUMBER_OF_PROCESSORS", "OS", "PATHEXT", "ProgramData", "ProgramFiles",
	"ProgramFiles(x86)", "SystemDrive", "SystemRoot", "TEMP", "TMP",
	"USERPROFILE", "windir",
}

// fixedEnv is set regardless of what the parent had. Captured output is read
// by a model, and ANSI escapes are noise it has to pay tokens for; these two
// stop most toolchains from emitting them.
var fixedEnv = [][2]string{
	{"TERM", "dumb"},
	{"NO_COLOR", "1"},
}

// ChildEnv assembles the environment for a child process Fylane starts
// outside the command engine — currently a proxied MCP server (mcpgate).
// passthrough adds variable *names* the user explicitly configured, which is
// how a server that needs an API key gets one without that key ever being
// written into a settings file.
func ChildEnv(passthrough ...string) []string {
	return buildEnvWith(os.LookupEnv, passthrough)
}

// buildEnv assembles the child environment. lookup is injected so tests can
// drive it without mutating the test process's own environment.
func buildEnv(lookup func(string) (string, bool)) []string {
	return buildEnvWith(lookup, nil)
}

func buildEnvWith(lookup func(string) (string, bool), passthrough []string) []string {
	names := allowedEnv
	if len(passthrough) > 0 {
		names = append(append([]string{}, allowedEnv...), passthrough...)
	}
	env := make([]string, 0, len(names)+len(fixedEnv))
	for _, name := range names {
		if runtime.GOOS != "windows" && isWindowsOnly(name) {
			continue
		}
		if v, ok := lookup(name); ok {
			env = append(env, name+"="+v)
		}
	}
	for _, kv := range fixedEnv {
		env = append(env, kv[0]+"="+kv[1])
	}
	return env
}

// windowsOnly names would only ever be set on Windows; skipping them
// elsewhere keeps a Unix child's environment to what it can actually use.
var windowsOnly = map[string]bool{
	"APPDATA": true, "COMSPEC": true, "HOMEDRIVE": true, "HOMEPATH": true,
	"LOCALAPPDATA": true, "NUMBER_OF_PROCESSORS": true, "OS": true,
	"PATHEXT": true, "ProgramData": true, "ProgramFiles": true,
	"ProgramFiles(x86)": true, "SystemDrive": true, "SystemRoot": true,
	"TEMP": true, "TMP": true, "USERPROFILE": true, "windir": true,
}

func isWindowsOnly(name string) bool { return windowsOnly[name] }
