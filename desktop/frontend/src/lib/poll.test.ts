import { describe, expect, it } from "vitest";
import { pollCore, type CorePollers } from "./poll";
import type { ChangeSet, Source, Workspace } from "./core";

const WS: Workspace = {
  id: "ws_1",
  name: "ai-workspace",
  mode: "read_write",
  exclude_rules: [],
  sensitive_rules: [],
  status: "active",
  created_at: "2026-07-01T09:00:00",
  root_path: "/Users/you/Projects/ai-workspace",
  availability: "available",
};

const SOURCES: Source[] = [
  { provider: "chatgpt", connected: true, lanes_carried: 3 },
  { provider: "claude", connected: false, lanes_carried: 0 },
  { provider: "grok", connected: false, lanes_carried: 0 },
];

const CHANGE_SET: ChangeSet = {
  id: "chg_1",
  workspace_id: "ws_1",
  provider: "chatgpt",
  summary: "Write notes",
  operations: [{ path: "notes/a.md", status: "applied" }],
  status: "applied",
  created_at: "2026-08-13T10:00:00",
};

function pollers(over: Partial<CorePollers> = {}): { deps: CorePollers; asked: string[] } {
  const asked: string[] = [];
  const note = <T>(name: string, value: T) => {
    asked.push(name);
    return Promise.resolve(value);
  };
  const deps: CorePollers = {
    status: () => note("status", { version: "1", pending_approvals: 0, tunnel: "connected" as const, approval_mode: "safe" as const }),
    workspaces: () => note("workspaces", { workspaces: [WS], currentWorkspaceID: "ws_1" }),
    approvals: () => note("approvals", []),
    tasks: () => note("tasks", []),
    commandSettings: () => note("commands", { rung: "workspace" as const, grants: [] }),
    prefs: () =>
      note("prefs", {
        task_timeout_seconds: 50,
        allow_stop_tasks: true,
        autostart: { supported: true, enabled: false },
        read_boundary: { state: "enforced", detail: "subprocess reads are bounded" },
      }),
    changeSets: () => note("changeSets", [CHANGE_SET]),
    sources: () => note("sources", SOURCES),
    ...over,
  };
  return { deps, asked };
}

describe("pollCore", () => {
  it("hands the Lane the connection state the Core reported", async () => {
    // Regression: the Sources screen came down and the
    // snapshot was left with a literal `sources: []`, which type-checks
    // perfectly and makes the Lane draw every platform as disconnected.
    const { deps } = pollers();
    const poll = await pollCore(deps);
    expect(poll.snapshot.sources).toEqual(SOURCES);
    const connected = poll.snapshot.sources.filter((s) => s.connected).map((s) => s.provider);
    expect(connected).toEqual(["chatgpt"]);
  });

  it("reads every part of the window in one pass", async () => {
    const { deps, asked } = pollers();
    await pollCore(deps);
    expect(asked.sort()).toEqual([
      "approvals",
      "changeSets",
      "commands",
      "prefs",
      "sources",
      "status",
      "tasks",
      "workspaces",
    ]);
  });

  it("picks the current workspace, falling back to the first granted one", async () => {
    const other: Workspace = { ...WS, id: "ws_2", name: "notes" };
    const { deps } = pollers({
      // What the Core answers after the current workspace was revoked.
      workspaces: async () => ({ workspaces: [other], currentWorkspaceID: "ws_gone" }),
    });
    const poll = await pollCore(deps);
    expect(poll.snapshot.workspace?.id).toBe("ws_2");
  });

  it("does not ask for change sets when no folder is granted", async () => {
    const { deps, asked } = pollers({
      workspaces: async () => ({ workspaces: [], currentWorkspaceID: "" }),
    });
    const poll = await pollCore(deps);
    expect(asked).not.toContain("changeSets");
    expect(poll.snapshot.workspace).toBeNull();
    expect(poll.snapshot.changeSets).toEqual([]);
  });

  it("lets an unreachable Core surface as a failure, rather than as empty data", async () => {
    // A poll that swallowed this would draw an online window with nothing in
    // it — the one shape that must never be confused with "nothing yet".
    const { deps } = pollers({
      status: async () => {
        throw new Error("core is not running");
      },
    });
    await expect(pollCore(deps)).rejects.toThrow("core is not running");
  });
});
