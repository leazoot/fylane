package app

import (
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/lsp"
	"github.com/leazoot/fylane/companion/internal/mcpgate"
)

// What the settings page is allowed to learn about a provider: its name and
// whether it asks. Not the program behind it — the desktop has no use for a
// command line, and a local detail that does not need to travel should not.
func TestTheProviderListCarriesNoProgram(t *testing.T) {
	reg, rejected := mcpgate.NewRegistry([]mcpgate.Provider{
		{Name: "docs", Command: []string{"/usr/local/bin/secret-mcp-server", "--db", "/Users/someone/notes.db"}},
		{Name: "sqlite", Command: []string{"sqlite-mcp"}, Trust: mcpgate.Workspace},
	})
	if len(rejected) != 0 {
		t.Fatalf("registry rejected valid providers: %v", rejected)
	}

	got := proxyList{reg}.Proxies()
	if len(got) != 2 {
		t.Fatalf("Proxies = %+v, want both", got)
	}
	trusts := map[string]string{}
	for _, p := range got {
		trusts[p.Name] = p.Trust
	}
	if trusts["docs"] != "ask" {
		t.Errorf("a provider with no trust set reported %q, want the asking default", trusts["docs"])
	}
	if trusts["sqlite"] != "workspace" {
		t.Errorf("sqlite reported %q, want workspace", trusts["sqlite"])
	}

	// Nothing in the result may mention the program or any path it names.
	flat := ""
	for _, p := range got {
		flat += p.Name + " " + p.Trust + " "
	}
	for _, leak := range []string{"secret-mcp-server", "/Users/someone", "notes.db", "sqlite-mcp"} {
		if strings.Contains(flat, leak) {
			t.Errorf("the provider list carries %q", leak)
		}
	}
}

// No registry means no list, not a crash and not a fabricated entry.
func TestAnAbsentRegistryListsNothing(t *testing.T) {
	if got := (proxyList{}).Proxies(); got != nil {
		t.Errorf("Proxies with no registry = %+v, want nil", got)
	}
}

// The same line, drawn for the other list of programs: the settings page
// learns a language server's name and what it reads, never the command behind
// it and never which folder it is indexing.
func TestTheLanguageServerListCarriesNoProgramAndNoWorkspace(t *testing.T) {
	reg, rejected := lsp.NewRegistry([]lsp.Server{{
		Name:       "fixture",
		Command:    []string{"echo", "--root", "/Users/someone/private-repo"},
		Extensions: []string{".zz"},
		LanguageID: "zz",
	}})
	if len(rejected) != 0 {
		t.Fatalf("registry rejected a valid server: %v", rejected)
	}
	sup := lsp.NewSupervisor(reg, time.Minute, nil)
	defer sup.Close()

	var got ctlapi.LanguageServer
	for _, s := range (serverList{sup}).LanguageServers() {
		if s.Name == "fixture" {
			got = s
		}
	}
	if got.Name == "" {
		t.Fatalf("the installed server is missing from %+v", (serverList{sup}).LanguageServers())
	}
	if len(got.Extensions) != 1 || got.Extensions[0] != ".zz" {
		t.Errorf("extensions = %v, want what it answers for", got.Extensions)
	}
	// Installed and never started: listed, and honestly not running. A list
	// that only showed the started ones would answer a different question
	// from the one it is labelled with.
	if got.Running {
		t.Error("a server nothing has called reported itself running")
	}

	flat := got.Name + " " + strings.Join(got.Extensions, " ")
	for _, leak := range []string{"echo", "/Users/someone", "private-repo"} {
		if strings.Contains(flat, leak) {
			t.Errorf("the language server list carries %q", leak)
		}
	}
}

// No supervisor means no list, not a crash and not a fabricated entry.
func TestAnAbsentSupervisorListsNothing(t *testing.T) {
	if got := (serverList{}).LanguageServers(); got != nil {
		t.Errorf("LanguageServers with no supervisor = %+v, want nil", got)
	}
}
