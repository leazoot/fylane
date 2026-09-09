package codeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultOpenCodeURL is where `opencode serve` listens unless told otherwise.
const DefaultOpenCodeURL = "http://127.0.0.1:4096"

// OpenCode delegates to a running `opencode serve`.
//
// Unlike Codex this adapter talks to a server the user starts, not a process
// Fylane spawns: opencode is built as a server with clients, and pretending
// otherwise would mean owning its lifecycle for no benefit. If it is not
// running, that is what the user is told.
//
// Progress is not streamed. The blocking message endpoint is one request with
// one shape to get right, whereas following the SSE bus means modelling
// opencode's event taxonomy — and this adapter was written against the
// documented contract without a copy of opencode to check it. The task view
// still shows the task as running; what it does not show is what the agent is
// doing minute by minute. That is a known gap, not an oversight.
type OpenCode struct {
	// BaseURL is where the server listens; empty uses DefaultOpenCodeURL.
	BaseURL string
	// Client overrides the HTTP client (tests).
	Client *http.Client
	// PasswordEnv names the environment variable holding the server
	// password; empty uses OPENCODE_SERVER_PASSWORD. The password itself is
	// never stored here or written to config — it is read at call time.
	PasswordEnv string
}

func (o *OpenCode) Name() string { return "opencode" }

func (o *OpenCode) baseURL() string {
	if o.BaseURL != "" {
		return strings.TrimRight(o.BaseURL, "/")
	}
	return DefaultOpenCodeURL
}

func (o *OpenCode) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	// No overall timeout: a delegated task legitimately takes minutes, and
	// the caller's context is what bounds it.
	return &http.Client{}
}

// Available probes the server rather than looking for a binary: what matters
// is whether something is listening, not whether opencode is installed.
func (o *OpenCode) Available() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL()+"/session", nil)
	if err != nil {
		return err
	}
	o.authorize(req)
	resp, err := o.client().Do(req)
	if err != nil {
		return fmt.Errorf("opencode is not reachable at %s: start it with `opencode serve`", o.baseURL())
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("opencode rejected the password: set %s to the value opencode serve was started with", o.passwordEnv())
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("opencode answered %s at %s", resp.Status, o.baseURL())
	}
	return nil
}

func (o *OpenCode) passwordEnv() string {
	if o.PasswordEnv != "" {
		return o.PasswordEnv
	}
	return "OPENCODE_SERVER_PASSWORD"
}

func (o *OpenCode) authorize(req *http.Request) {
	if pw := os.Getenv(o.passwordEnv()); pw != "" {
		req.SetBasicAuth("opencode", pw)
	}
}

type openCodeSession struct {
	ID string `json:"id"`
}

type openCodePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type openCodeMessage struct {
	Parts []openCodePart `json:"parts"`
}

func (o *OpenCode) Run(ctx context.Context, task Task, out io.Writer) (Result, error) {
	if strings.TrimSpace(task.Prompt) == "" {
		return Result{}, fmt.Errorf("a delegated task needs a prompt")
	}
	dir, err := workDir(task)
	if err != nil {
		return Result{}, err
	}
	if err := o.Available(); err != nil {
		return Result{}, err
	}

	var session openCodeSession
	if err := o.post(ctx, "/session", map[string]any{
		"title": "Fylane: " + firstLine(task.Prompt),
	}, &session); err != nil {
		return Result{}, fmt.Errorf("creating an opencode session: %w", err)
	}
	if session.ID == "" {
		return Result{}, fmt.Errorf("opencode returned a session with no id")
	}

	scrub := newScrubber(task.Root)
	fmt.Fprintf(scrubbingWriter{scrub: scrub, to: out},
		"delegated to opencode (session %s) in %s\n", session.ID, dir)

	body := map[string]any{
		"parts": []map[string]any{{"type": "text", "text": task.Prompt}},
	}
	if task.Model != "" {
		body["model"] = task.Model
	}
	var reply openCodeMessage
	if err := o.post(ctx, "/session/"+session.ID+"/message", body, &reply); err != nil {
		return Result{}, fmt.Errorf("running the opencode task: %w", err)
	}

	var text strings.Builder
	for _, p := range reply.Parts {
		if p.Type == "text" && p.Text != "" {
			text.WriteString(p.Text)
			text.WriteString("\n")
		}
	}
	summary := scrub.clean(strings.TrimSpace(text.String()))
	if summary != "" {
		fmt.Fprintln(out, summary)
	}
	return Result{Summary: summary}, nil
}

func (o *OpenCode) post(ctx context.Context, path string, body any, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL()+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	o.authorize(req)

	resp, err := o.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// The body can hold the agent's own error text; cap it so a runaway
		// response cannot become the error message.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("opencode answered %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
