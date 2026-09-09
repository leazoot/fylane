//go:build !windows

package codeagent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCodex writes its own environment where codex would write its closing
// message, so a test can read exactly what the child was handed.
func fakeCodex(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nout=\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"-o\" ]; then out=\"$2\"; fi\n  shift\ndone\nenv > \"$out\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFakeCodex(t *testing.T, agent *Codex) string {
	t.Helper()
	res, err := agent.Run(context.Background(), Task{Prompt: "do the thing", Root: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res.Summary
}

// The finding this replaced os.Environ() for: a delegated agent used to be
// handed every variable this process had, including Fylane's own relay
// credentials, and then went off to do work Fylane cannot watch.
func TestADelegatedAgentDoesNotInheritFylanesEnvironment(t *testing.T) {
	t.Setenv("FYLANE_TEST_RELAY_TOKEN", "tk-not-for-the-agent")
	env := runFakeCodex(t, &Codex{Binary: fakeCodex(t)})

	if strings.Contains(env, "tk-not-for-the-agent") {
		t.Fatal("the agent was handed a variable nobody listed for it")
	}
	// The base set still arrives, or the agent cannot find its own config.
	if !strings.Contains(env, "PATH=") || !strings.Contains(env, "HOME=") {
		t.Fatalf("the base environment is missing:\n%s", env)
	}
}

func TestADelegatedAgentGetsTheVariablesTheUserNamed(t *testing.T) {
	t.Setenv("FYLANE_TEST_AGENT_KEY", "sk-for-the-agent")
	env := runFakeCodex(t, &Codex{
		Binary:         fakeCodex(t),
		EnvPassthrough: []string{"FYLANE_TEST_AGENT_KEY"},
	})

	if !strings.Contains(env, "FYLANE_TEST_AGENT_KEY=sk-for-the-agent") {
		t.Fatalf("the named variable did not reach the agent:\n%s", env)
	}
}
