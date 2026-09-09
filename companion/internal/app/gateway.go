package app

import (
	"strings"

	"github.com/leazoot/fylane/companion/internal/ctlapi"
	"github.com/leazoot/fylane/companion/internal/lsp"
	"github.com/leazoot/fylane/companion/internal/mcpgate"
	"github.com/leazoot/fylane/companion/internal/readbox"
)

// mcpProviders builds the local MCP gateway's registry from the settings file.
//
// Two things are said out loud at startup rather than left to a file nobody
// reads. A rejected entry is named with its reason, because the likeliest
// reason is a provider spelled as a downloader and a silently missing
// gateway looks like a bug. And a provider the user moved off the asking
// default is warned about every start, for the same reason the open approval
// rung is: an authorization in force is only safe while it is visible.
func (a *App) mcpProviders() (*mcpgate.Registry, error) {
	configured, err := LoadMCPProviders(a.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	reg, rejected := mcpgate.NewRegistry(configured)
	for _, err := range rejected {
		a.log.Error("local MCP provider not configured", "error", err)
	}
	var trusted []string
	for _, p := range reg.List() {
		if p.Trusted() == mcpgate.Workspace {
			trusted = append(trusted, p.Name)
		}
	}
	if names := reg.Names(); len(names) > 0 {
		a.log.Info("local MCP providers available through the gateway", "providers", strings.Join(names, ","))
	}
	if len(trusted) > 0 {
		a.log.Warn("these MCP providers do not ask before every call; their tools run under the workspace grant",
			"providers", strings.Join(trusted, ","))
	}
	return reg, nil
}

// proxyList adapts the gateway registry to what the control API shows. It
// carries the name and the trust and nothing else: the desktop has no use for
// the program behind a provider, and a command line is a local detail that
// should not travel even as far as the settings page.
type proxyList struct{ reg *mcpgate.Registry }

func (p proxyList) Proxies() []ctlapi.ProxyProvider {
	if p.reg == nil {
		return nil
	}
	out := []ctlapi.ProxyProvider{}
	for _, prov := range p.reg.List() {
		out = append(out, ctlapi.ProxyProvider{Name: prov.Name, Trust: string(prov.Trusted())})
	}
	return out
}

// serverList adapts the supervisor to what the control API shows. It carries
// the name, the suffixes and whether one is up — not the command behind it and
// not the workspace it is indexing, on the same grounds proxyList carries
// neither: a command line is a local detail, and which folder a server is
// reading is not what this list is answering.
type serverList struct{ sup *lsp.Supervisor }

func (l serverList) LanguageServers() []ctlapi.LanguageServer {
	if l.sup == nil {
		return nil
	}
	running := map[string]bool{}
	for _, name := range l.sup.RunningServers() {
		running[name] = true
	}
	out := []ctlapi.LanguageServer{}
	for _, srv := range l.sup.Registry().List() {
		out = append(out, ctlapi.LanguageServer{
			Name:       srv.Name,
			Extensions: srv.Extensions,
			Running:    running[srv.Name],
		})
	}
	return out
}

// languageServers builds the code_navigate supervisor from the built-in table
// and the settings file. It is in this file because it answers the same
// question the gateway does — which locally installed programs may this
// Companion start — and gets the same two answers said out loud: a rejected
// entry is named, and what ended up available is logged, because a navigation
// tool that is quietly absent looks like a bug in the tool.
func (a *App) languageServers(box *readbox.Box) (*lsp.Supervisor, error) {
	configured, err := LoadLanguageServers(a.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	reg, rejected := lsp.NewRegistry(configured)
	for _, err := range rejected {
		a.log.Error("language server not configured", "error", err)
	}
	if names := reg.Names(); len(names) > 0 {
		a.log.Info("language servers available for code navigation", "servers", strings.Join(names, ","))
	}
	return lsp.NewSupervisor(reg, 0, box), nil
}
