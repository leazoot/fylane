package cmdexec

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// The launcher rule, in one place because more than one surface needs it.
//
// The Core never fetches an executable at runtime
// except the pinned tunnel binary. A program whose whole job is to download
// another program and run it is that same thing with a step in between, so it
// is refused wherever a locally configured program is named — the MCP gateway
// and the language servers behind code_navigate both.

// ProgramName is the comparable name of a command: directory dropped, case
// folded, and any Windows executable suffix removed. `C:\tools\NPX.CMD` and
// `npx` are the same program and must not be told apart by spelling.
func ProgramName(arg string) string {
	slashed := strings.ReplaceAll(strings.TrimSpace(arg), `\`, "/")
	base := strings.ToLower(path.Base(filepath.ToSlash(slashed)))
	if ext := path.Ext(base); ext == ".exe" || ext == ".cmd" || ext == ".bat" {
		base = base[:len(base)-len(ext)]
	}
	return base
}

// launchers whose only purpose is to fetch a program and then run it.
var launchers = map[string]string{
	"npx":  "downloads and runs a package",
	"pnpx": "downloads and runs a package",
	"bunx": "downloads and runs a package",
	"uvx":  "downloads and runs a package",
}

// launcherSubcommands are the same thing spelled as a subcommand of a tool
// that also does ordinary work.
var launcherSubcommands = map[string][]string{
	"npm":    {"exec", "x"},
	"pnpm":   {"dlx", "exec"},
	"yarn":   {"dlx"},
	"bun":    {"x"},
	"uv":     {"tool", "run"},
	"pipx":   {"run"},
	"go":     {"run"},
	"deno":   {"run"},
	"cargo":  {"install"},
	"nix":    {"run"},
	"docker": {"run"},
	"podman": {"run"},
}

// FetchesAndRuns reports why a command line downloads its own program, or ""
// when it does not.
func FetchesAndRuns(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	prog := ProgramName(argv[0])
	if why, ok := launchers[prog]; ok {
		return why
	}
	subs, ok := launcherSubcommands[prog]
	if !ok {
		return ""
	}
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		for _, s := range subs {
			if a == s {
				return fmt.Sprintf("runs `%s %s`, which downloads the program it runs", prog, s)
			}
		}
		// Only the first subcommand decides; `go build ./run` is not `go run`.
		return ""
	}
	return ""
}
