package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/readbox"
)

// handshakeTimeout bounds initialization. A server that never answers
// `initialize` is not going to answer anything else, and the caller is waiting
// on a tool call with its own budget.
const handshakeTimeout = 45 * time.Second

// shutdownGrace is how long a server gets to leave politely before the process
// group is taken down.
const shutdownGrace = 3 * time.Second

// position is LSP's own: zero-based line, and a character offset in UTF-16
// code units. The units are why nothing outside this file handles one.
type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

// symbolNode covers both shapes a server may answer documentSymbol with: the
// hierarchical DocumentSymbol, and the flat SymbolInformation whose position
// lives under `location`. Servers disagree about which they send, so both are
// read rather than one being declared and hoped for.
type symbolNode struct {
	Name           string       `json:"name"`
	Kind           int          `json:"kind"`
	Detail         string       `json:"detail,omitempty"`
	Range          lspRange     `json:"range"`
	SelectionRange lspRange     `json:"selectionRange"`
	Location       *location    `json:"location,omitempty"`
	Children       []symbolNode `json:"children,omitempty"`
}

func (n symbolNode) start() position {
	if n.Location != nil {
		return n.Location.Range.Start
	}
	if n.SelectionRange != (lspRange{}) {
		return n.SelectionRange.Start
	}
	return n.Range.Start
}

// client is one running language server, serving one workspace.
type client struct {
	server Server
	root   string

	cmd   *osexec.Cmd
	group *cmdexec.ProcessGroup
	conn  *conn
	box   *readbox.Box

	mu       sync.Mutex
	open     map[string]bool
	lastUsed time.Time
	dead     bool
}

func newClient(server Server, root string, box *readbox.Box) *client {
	return &client{server: server, root: root, box: box,
		open: map[string]bool{}, lastUsed: time.Now()}
}

// start launches the server and completes the handshake. The child gets the
// workspace as its working directory and a whitelisted environment, the same
// two bounds every other program this Core starts is held to.
func (c *client) start(ctx context.Context) error {
	prog, err := c.server.resolve()
	if err != nil {
		return err
	}
	cmd := osexec.Command(prog, c.server.Command[1:]...)
	cmd.Dir = c.root
	cmd.Env = cmdexec.ChildEnv(c.server.EnvPassthrough...)
	// The server's stderr is discarded rather than captured. It carries
	// snippets of the code being analysed, and nothing in this product may
	// write file contents to a log (backend.md).
	cmd.Stderr = io.Discard

	// A language server reads the whole workspace by design, which is exactly
	// why the boundary matters: what it must not read is everything else.
	// Network allowed: BL-8 bounds the outbound traffic of the work a caller
	// sets in motion — commands and delegated agents. A language server is
	// started by Fylane on its own terms to answer read-only questions, and
	// denying it the network would degrade a navigation answer (an unresolved
	// module) rather than close a path a caller controls.
	if err := c.box.Wrap(cmd, readbox.WorkspacePolicy(c.root, true)); err != nil {
		return err
	}

	group := cmdexec.NewProcessGroup()
	group.Prepare(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("starting %s: %w", c.server.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("starting %s: %w", c.server.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", c.server.Name, err)
	}
	if err := group.Adopt(cmd); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("starting %s: %w", c.server.Name, err)
	}
	c.cmd, c.group, c.conn = cmd, group, newConn(stdin, stdout)

	hs, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := c.handshake(hs); err != nil {
		c.stop()
		return err
	}
	return nil
}

func (c *client) handshake(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"clientInfo": map[string]any{
			"name":    "Fylane",
			"version": "1",
		},
		"rootUri": pathToURI(c.root),
		"workspaceFolders": []map[string]any{
			{"uri": pathToURI(c.root), "name": filepath.Base(c.root)},
		},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": false},
				// linkSupport off keeps definition answers to plain Locations
				// rather than LocationLinks, which is one shape less to read.
				"definition":     map[string]any{"linkSupport": false},
				"references":     map[string]any{},
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
			"workspace": map[string]any{"workspaceFolders": true},
		},
	}
	if _, err := c.conn.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("%s did not start: %w", c.server.Name, err)
	}
	if err := c.conn.notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("%s did not start: %w", c.server.Name, err)
	}
	return nil
}

// stop ends the server: the protocol's own goodbye first, then the process
// group. Both halves are needed — a language server asked to shut down flushes
// caches, and one that ignores the request still has to go.
func (c *client) stop() {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return
	}
	c.dead = true
	c.mu.Unlock()

	if c.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		_, _ = c.conn.call(ctx, "shutdown", nil)
		cancel()
		_ = c.conn.notify("exit", nil)
	}

	done := make(chan struct{})
	go func() {
		if c.cmd != nil {
			_ = c.cmd.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		if c.group != nil && c.cmd != nil {
			_ = c.group.Kill(c.cmd)
		}
		<-done
	}
	if c.conn != nil {
		c.conn.close()
	}
	if c.group != nil {
		c.group.Close()
	}
}

// didOpen hands the server the file's current bytes. Servers may read
// unopened files from disk, but they are not required to, and the ones that
// do can be a revision behind whatever the change-set engine just wrote.
func (c *client) didOpen(rel string) error {
	uri := pathToURI(filepath.Join(c.root, filepath.FromSlash(rel)))
	c.mu.Lock()
	already := c.open[uri]
	c.mu.Unlock()
	if already {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(c.root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	if err := c.conn.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": c.server.Language(),
			"version":    1,
			"text":       string(body),
		},
	}); err != nil {
		return err
	}
	c.mu.Lock()
	c.open[uri] = true
	c.mu.Unlock()
	return nil
}

func (c *client) definition(ctx context.Context, rel string, at position) ([]location, error) {
	raw, err := c.conn.call(ctx, "textDocument/definition", c.textDocumentPosition(rel, at))
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

func (c *client) references(ctx context.Context, rel string, at position) ([]location, error) {
	params := c.textDocumentPosition(rel, at)
	params["context"] = map[string]any{"includeDeclaration": true}
	raw, err := c.conn.call(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

func (c *client) symbols(ctx context.Context, rel string) ([]symbolNode, error) {
	raw, err := c.conn.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, filepath.FromSlash(rel)))},
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var nodes []symbolNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, fmt.Errorf("%s sent an unreadable symbol list: %w", c.server.Name, err)
	}
	return nodes, nil
}

func (c *client) textDocumentPosition(rel string, at position) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, filepath.FromSlash(rel)))},
		"position":     at,
	}
}

// decodeLocations reads the two shapes a definition or reference answer comes
// in: a single Location, or an array of them.
func decodeLocations(raw json.RawMessage) ([]location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var many []location
	if err := json.Unmarshal(raw, &many); err == nil {
		return many, nil
	}
	var one location
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("unreadable location in the answer: %w", err)
	}
	return []location{one}, nil
}

func (c *client) touch() {
	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()
}

func (c *client) idleSince() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastUsed
}

// pathToURI builds the file URI for an absolute path. url.URL does the
// escaping: a directory with a space, a `#`, or a `%` in its name produces a
// URI that means the directory, not something else.
func pathToURI(abs string) string {
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		// Windows: C:/src becomes /C:/src, so the path component is rooted.
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed}
	return u.String()
}

// uriToPath is the inverse, and it refuses anything that is not a local file.
// A server is free to answer with a URI of any scheme; a path Fylane is going
// to show has to be a file on this machine.
func uriToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("unreadable location %q", uri)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("location %q is not a file on this machine", uri)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", fmt.Errorf("location %q is on another host", uri)
	}
	p := u.Path
	if runtime.GOOS == "windows" && hasDriveAfterSlash(p) {
		// "/C:/src" is how a drive-rooted path travels in a URI; the slash is
		// the anchor, not part of the path. A drive-less "/tmp/a.go" keeps
		// its slash, or it would come back relative.
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p), nil
}

// hasDriveAfterSlash reports whether p looks like "/X:..." for a drive letter X.
func hasDriveAfterSlash(p string) bool {
	if len(p) < 3 || p[0] != '/' || p[2] != ':' {
		return false
	}
	c := p[1]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// utf16Column converts a byte offset within a line into LSP's UTF-16 code
// units. Getting this wrong is invisible in ASCII and wrong everywhere else.
func utf16Column(line string, byteOffset int) int {
	if byteOffset > len(line) {
		byteOffset = len(line)
	}
	return len(utf16.Encode([]rune(line[:byteOffset])))
}
