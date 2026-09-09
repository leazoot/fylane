package mcpserver

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/cmdgate"
	"github.com/leazoot/fylane/companion/internal/mcpgate"
	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/redact"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// The provider these tests proxy to is this test binary, re-executed with
// gatewayFixtureEnv set: a real process on the far end of a real stdio
// transport, with nothing fetched to get one.
const gatewayFixtureEnv = "FYLANE_TEST_GATEWAY_PROVIDER"

func TestMain(m *testing.M) {
	if os.Getenv(gatewayFixtureEnv) != "" {
		runGatewayFixture()
		return
	}
	os.Exit(m.Run())
}

type fixtureIn struct {
	Text string `json:"text,omitempty"`
}

type fixtureOut struct {
	Echo string `json:"echo,omitempty"`
}

func runGatewayFixture() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "returns what it was given"},
		func(_ context.Context, _ *mcp.CallToolRequest, in fixtureIn) (*mcp.CallToolResult, fixtureOut, error) {
			if in.Text == "boom" {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "the provider says no"}},
				}, fixtureOut{}, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: in.Text}},
			}, fixtureOut{Echo: in.Text}, nil
		})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

// withProvider wires the gateway onto an exec fixture at the given trust.
func withProvider(t *testing.T, f *execFixture, trust mcpgate.Trust) {
	t.Helper()
	t.Setenv(gatewayFixtureEnv, "1")
	reg, errs := mcpgate.NewRegistry([]mcpgate.Provider{{
		Name:           "fixture",
		Command:        []string{os.Args[0]},
		EnvPassthrough: []string{gatewayFixtureEnv},
		Trust:          trust,
	}})
	if len(errs) > 0 {
		t.Fatalf("registry: %v", errs)
	}
	f.tools.providers = reg
}

func (f *execFixture) gateway(t *testing.T, in gatewayInput) gatewayOutput {
	t.Helper()
	_, out, err := f.tools.mcpGateway(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("mcp_gateway(%s): %v", in.Action, err)
	}
	return out
}

func callFixture(t *testing.T, f *execFixture, text string) gatewayOutput {
	t.Helper()
	return f.gateway(t, gatewayInput{
		Action: actionCallTool, Provider: "fixture", Tool: "echo",
		Arguments: map[string]any{"text": text},
	})
}

// The open rung is a statement about not being interrupted while work happens
// in a workspace. A proxied tool is not that work: nothing here knows what it
// does, so nothing here can claim the user already agreed to it.
func TestAProxiedCallAsksOnTheOpenRung(t *testing.T) {
	f := newExecFixtureAt(t, txn.Decision{Approved: true}, cmdgate.Open)
	withProvider(t, f, mcpgate.Ask)

	out := callFixture(t, f, "hello")
	if out.Status != "completed" {
		t.Fatalf("status = %q, want the call to have gone through", out.Status)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 {
		t.Fatalf("the open rung waived a proxied call: %d approvals raised", len(reqs))
	}
	if reqs[0].Kind != txn.KindProxy {
		t.Errorf("kind = %q, want %q: none of the other four questions can be asked honestly here",
			reqs[0].Kind, txn.KindProxy)
	}
	if reqs[0].Grant {
		t.Error("the prompt offered to grant the workspace for an opaque tool")
	}
}

// The workspace grant covers ordinary commands in a folder. It was never
// consent for a tool whose effect nobody can name.
func TestAWorkspaceGrantDoesNotCoverAProxiedCall(t *testing.T) {
	// newExecFixture grants the workspace at the workspace rung.
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Ask)

	callFixture(t, f, "hello")
	if n := len(f.approver.requests()); n != 1 {
		t.Fatalf("a workspace grant covered a proxied call: %d approvals raised", n)
	}
}

// The one way out is the user classifying a named provider by hand. It has to
// work, or the honest default becomes the unusable default.
func TestAClassifiedProviderFollowsTheRung(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Workspace)

	out := callFixture(t, f, "hello")
	if out.Status != "completed" {
		t.Fatalf("status = %q, want completed", out.Status)
	}
	if n := len(f.approver.requests()); n != 0 {
		t.Fatalf("a classified provider still asked %d times", n)
	}
}

// Classifying a provider changes who is asked, never whether the record is
// written. That is the invariant, restated for this surface.
func TestEveryProxiedCallIsAuditedAtEveryTrust(t *testing.T) {
	for _, trust := range []mcpgate.Trust{mcpgate.Ask, mcpgate.Workspace} {
		f := newExecFixture(t, txn.Decision{Approved: true})
		withProvider(t, f, trust)
		callFixture(t, f, "hello")

		var found bool
		for _, rec := range f.audit.all() {
			if strings.Join(rec.Argv, " ") == "mcp:fixture echo" {
				found = true
				if rec.Outcome != cmdexec.OutcomeOK {
					t.Errorf("trust %q: outcome = %q, want %q", trust, rec.Outcome, cmdexec.OutcomeOK)
				}
				if rec.Provider != "claude" {
					t.Errorf("trust %q: the record does not say which platform called", trust)
				}
			}
		}
		if !found {
			t.Errorf("trust %q: the call left no audit record", trust)
		}
	}
}

func TestADeclinedProxyCallStopsAndIsRecorded(t *testing.T) {
	f := newExecFixtureAt(t, txn.Decision{Reason: "user_rejected"}, cmdgate.Open)
	withProvider(t, f, mcpgate.Ask)

	out := callFixture(t, f, "hello")
	if out.Status != "refused" {
		t.Fatalf("status = %q, want refused", out.Status)
	}
	if out.Next != nextstep.Stop {
		t.Errorf("next = %q, want %q: retrying will not change the user's answer", out.Next, nextstep.Stop)
	}
	if out.Content != "" {
		t.Errorf("content = %q, want nothing: the call never ran", out.Content)
	}
	var refused bool
	for _, rec := range f.audit.all() {
		if rec.Outcome == cmdexec.OutcomeRefused {
			refused = true
		}
	}
	if !refused {
		t.Error("a declined proxied call left no record of having been asked for")
	}
}

func TestAnUndecidedProxyCallAsksTheUserAndKeepsTheKey(t *testing.T) {
	f := newExecFixtureAt(t, txn.Decision{Pending: true}, cmdgate.Open)
	withProvider(t, f, mcpgate.Ask)

	out := callFixture(t, f, "hello")
	if out.Status != "pending_approval" {
		t.Fatalf("status = %q, want pending_approval", out.Status)
	}
	if out.Next != nextstep.AskUser {
		t.Errorf("next = %q, want %q", out.Next, nextstep.AskUser)
	}
	// Two calls, one prompt: the retry has to re-attach rather than raise a
	// second question about the same thing.
	callFixture(t, f, "hello")
	reqs := f.approver.requests()
	if len(reqs) != 2 || reqs[0].ChangeSetID != reqs[1].ChangeSetID {
		t.Errorf("retry raised a different prompt: %q then %q", reqs[0].ChangeSetID, reqs[1].ChangeSetID)
	}
}

// The provider's own refusal is not Fylane's. Reporting it as `stop` would
// tell a model its arguments were fine and the user said no.
func TestAProvidersOwnErrorAsksForBetterInputNotSilence(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Workspace)

	out := callFixture(t, f, "boom")
	if !out.ToolError {
		t.Fatal("the provider's error flag was lost on the way out")
	}
	if out.Status != "completed" {
		t.Errorf("status = %q: Fylane forwarded the call, so it completed", out.Status)
	}
	if out.Next != nextstep.FixInput {
		t.Errorf("next = %q, want %q", out.Next, nextstep.FixInput)
	}
}

// Redaction happens where the answer leaves the machine, exactly as it does
// for command output. A proxied answer is output from a program
// Fylane knows even less about.
func TestAProxiedAnswerIsRedactedOnItsWayOut(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Workspace)

	secret := "ghp_" + strings.Repeat("a", 30)
	out := callFixture(t, f, "token is "+secret)
	if strings.Contains(out.Content, secret) {
		t.Fatalf("content = %q, carried a credential-shaped value off the machine", out.Content)
	}
	if !strings.Contains(out.Content, redact.Placeholder) {
		t.Errorf("content = %q, want the value visibly withheld", out.Content)
	}
}

// Which binaries are installed here is machine state. The caller's business
// is whether the name works, not what is behind it.
func TestListingProvidersNamesNoProgram(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Ask)

	out := f.gateway(t, gatewayInput{Action: actionListProviders})
	if len(out.Providers) != 1 || out.Providers[0].Name != "fixture" {
		t.Fatalf("providers = %+v", out.Providers)
	}
	if out.Providers[0].Trust != string(mcpgate.Ask) {
		t.Errorf("trust = %q, want the caller told it will be asked about", out.Providers[0].Trust)
	}
	if strings.Contains(out.Providers[0].Reason, os.Args[0]) {
		t.Error("the listing named the program behind the provider")
	}
	if n := len(f.approver.requests()); n != 0 {
		t.Errorf("listing what is configured raised %d approvals", n)
	}
}

// Listing a provider's tools returns their schemas, and does not ask: it is
// the one gateway operation whose effect is known, and a caller without the
// schema can only guess at arguments.
func TestListingToolsReturnsSchemasWithoutAsking(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Ask)

	out := f.gateway(t, gatewayInput{Action: actionListTools, Provider: "fixture"})
	var echo *mcpgate.Tool
	for i := range out.Tools {
		if out.Tools[i].Name == "echo" {
			echo = &out.Tools[i]
		}
	}
	if echo == nil {
		t.Fatalf("tools = %+v, want the provider's echo tool", out.Tools)
	}
	if echo.Schema == nil {
		t.Error("the tool arrived without its input schema")
	}
	if n := len(f.approver.requests()); n != 0 {
		t.Errorf("listing tools raised %d approvals", n)
	}
}

func TestAnUnknownProviderOrActionIsAnError(t *testing.T) {
	f := newExecFixture(t, txn.Decision{Approved: true})
	withProvider(t, f, mcpgate.Ask)

	if _, _, err := f.tools.mcpGateway(context.Background(), nil,
		gatewayInput{Action: "call_everything"}); err == nil {
		t.Error("an unknown action was accepted")
	}
	if _, _, err := f.tools.mcpGateway(context.Background(), nil,
		gatewayInput{Action: actionCallTool, Provider: "nope", Tool: "echo"}); err == nil {
		t.Error("an unconfigured provider was accepted")
	}
	if _, _, err := f.tools.mcpGateway(context.Background(), nil,
		gatewayInput{Action: actionCallTool, Provider: "fixture"}); err == nil {
		t.Error("call_tool without a tool name was accepted")
	}
	if n := len(f.approver.requests()); n != 0 {
		t.Errorf("a malformed request raised %d approvals before being rejected", n)
	}
}

// A Companion nobody configured a provider on must not advertise a gateway:
// an advertised tool that can only fail is a tool a model will keep trying.
// One that has a provider — and the approval chain behind it — must.
func TestTheGatewayIsAdvertisedOnlyWhenItCanBeUsed(t *testing.T) {
	if advertisesGateway(t, func(d Deps) Deps { return d }) {
		t.Error("the gateway is advertised with no provider configured")
	}
	if !advertisesGateway(t, withGatewayChain) {
		t.Error("a configured gateway is not advertised, so no caller can reach it")
	}
	// Every part of the chain is load-bearing: a gateway with no approver
	// would forward opaque calls with nobody able to stop them.
	if advertisesGateway(t, func(d Deps) Deps { d = withGatewayChain(d); d.Approve = nil; return d }) {
		t.Error("the gateway is advertised without an approver")
	}
	if advertisesGateway(t, func(d Deps) Deps { d = withGatewayChain(d); d.ExecAudit = nil; return d }) {
		t.Error("the gateway is advertised without an audit")
	}
}

func withGatewayChain(d Deps) Deps {
	reg, _ := mcpgate.NewRegistry([]mcpgate.Provider{{Name: "fixture", Command: []string{"some-server"}}})
	d.Providers = reg
	d.Approve = &scriptedApprover{decision: txn.Decision{Approved: true}}
	d.Gate = cmdgate.New(nil, string(cmdgate.Workspace), nil)
	d.ExecAudit = &capturingAudit{}
	return d
}

func advertisesGateway(t *testing.T, adjust func(Deps) Deps) bool {
	t.Helper()
	deps := adjust(testDeps(t, t.TempDir()))
	httpServer := httptest.NewServer(Handler(deps, nil))
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "mcp_gateway" {
			return true
		}
	}
	return false
}
