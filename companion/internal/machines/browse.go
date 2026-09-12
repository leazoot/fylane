package machines

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
)

// Listing is what one directory on a machine holds, so the folder sheet can
// walk to a folder instead of asking for its path typed blind. Only
// directories are named: a workspace is a folder, and nothing here opens a
// file. The listing runs over ssh like the probe does, so it works before
// Fylane is installed there and it never goes through the remote Companion's
// sandbox — that sandbox starts at the folder this listing helps choose.
type Listing struct {
	// Path is the directory as the machine resolved it, absolute.
	Path string `json:"path,omitempty"`
	// Parent is the directory above; empty at the root.
	Parent string `json:"parent,omitempty"`
	// Home is the login user's home directory, the sheet's way back.
	Home    string  `json:"home,omitempty"`
	Entries []Entry `json:"entries"`
	// Reason and Detail say why there is no listing: "nodir", "denied", or
	// one of the link's own reason codes when ssh did not get in.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Entry is one directory inside a Listing.
type Entry struct {
	Name string `json:"name"`
	// Repo is whether the directory has a .git in it — where the projects are.
	Repo bool `json:"repo,omitempty"`
	// Hidden is a dot-directory, shown only on request.
	Hidden bool `json:"hidden,omitempty"`
}

const (
	ReasonNoDir  = "nodir"
	ReasonDenied = "denied"

	browseTimeout = 20 * time.Second
	// pathTerminator closes the quoted heredoc that carries the path, so no
	// character in the path is ever read by the shell.
	pathTerminator = "FYLANE_PATH"
)

var errBadPath = errors.New("path: must be a single line")

// Browse lists the directories inside path on the machine. An empty path is
// the login home; "~" is expanded there. A path that is not a directory or
// cannot be entered comes back as a Listing with a Reason, as does a machine
// that ssh cannot reach; only an unknown machine or an impossible path is an
// error.
func (m *Manager) Browse(ctx context.Context, id, dir string) (Listing, error) {
	if strings.ContainsAny(dir, "\x00\r\n") || dir == pathTerminator {
		return Listing{}, errBadPath
	}
	m.mu.Lock()
	l, ok := m.links[id]
	var mc Machine
	if ok {
		mc = l.m
	}
	m.mu.Unlock()
	if !ok {
		return Listing{}, ErrUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, browseTimeout)
	defer cancel()
	out, stderr, err := m.dial.Run(ctx, mc, browseScript(dir))
	if err != nil {
		err = explain(err, stderr)
		return Listing{Reason: reasonOf(err), Detail: err.Error()}, nil
	}
	return parseListing(out), nil
}

// browseScript is what runs there. The path travels in a quoted heredoc
// into `read -r`, never through command substitution, so quoting in it is
// inert; `cd` decides whether it is a directory and `pwd` says where it
// really is. Directory names holding a newline are dropped rather than
// sent broken.
func browseScript(dir string) string {
	return `IFS= read -r p <<'` + pathTerminator + `'
` + dir + `
` + pathTerminator + `
[ -n "$p" ] || p="$HOME"
case "$p" in "~") p="$HOME";; "~/"*) p="$HOME${p#\~}";; esac
if [ ! -d "$p" ]; then echo "error ` + ReasonNoDir + `"; exit 0; fi
cd -- "$p" 2>/dev/null || { echo "error ` + ReasonDenied + `"; exit 0; }
echo "home $HOME"
echo "path $(pwd)"
nl='
'
for d in .* *; do
  [ -d "$d" ] || continue
  case "$d" in .|..|*"$nl"*) continue;; esac
  if [ -d "$d/.git" ]; then echo "r $d"; else echo "d $d"; fi
done
`
}

func parseListing(out string) Listing {
	ls := Listing{Entries: []Entry{}}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "error "):
			ls.Reason = strings.TrimPrefix(line, "error ")
			return Listing{Reason: ls.Reason, Entries: []Entry{}}
		case strings.HasPrefix(line, "home "):
			ls.Home = strings.TrimPrefix(line, "home ")
		case strings.HasPrefix(line, "path "):
			ls.Path = strings.TrimPrefix(line, "path ")
		case strings.HasPrefix(line, "r "), strings.HasPrefix(line, "d "):
			name := line[2:]
			ls.Entries = append(ls.Entries, Entry{
				Name:   name,
				Repo:   line[0] == 'r',
				Hidden: strings.HasPrefix(name, "."),
			})
		}
	}
	if ls.Path != "" && ls.Path != "/" {
		ls.Parent = path.Dir(ls.Path)
	}
	sort.SliceStable(ls.Entries, func(i, j int) bool {
		return strings.ToLower(ls.Entries[i].Name) < strings.ToLower(ls.Entries[j].Name)
	})
	return ls
}
