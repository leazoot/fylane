import { describe, expect, it } from "vitest";
import { pollCore, type CorePollers } from "./poll";
import type {
  Approval,
  ChangeSet,
  MachineInfo,
  RemoteCore,
  Source,
  TaskInfo,
  Workspace,
} from "./core";

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

function pollers(over: Partial<CorePollers> = {}): {
  deps: CorePollers;
  asked: string[];
} {
  const asked: string[] = [];
  const note = <T>(name: string, value: T) => {
    asked.push(name);
    return Promise.resolve(value);
  };
  const deps: CorePollers = {
    status: () =>
      note("status", {
        version: "1",
        pending_approvals: 0,
        tunnel: "connected" as const,
        approval_mode: "safe" as const,
      }),
    workspaces: () =>
      note("workspaces", { workspaces: [WS], currentWorkspaceID: "ws_1" }),
    approvals: () => note("approvals", []),
    tasks: () => note("tasks", []),
    commandSettings: () =>
      note("commands", { rung: "workspace" as const, grants: [] }),
    prefs: () =>
      note("prefs", {
        task_timeout_seconds: 50,
        allow_stop_tasks: true,
        autostart: { supported: true, enabled: false },
        read_boundary: {
          state: "enforced",
          detail: "subprocess reads are bounded",
        },
      }),
    changeSets: () => note("changeSets", [CHANGE_SET]),
    sources: () => note("sources", SOURCES),
    machines: () => note("machines", { machines: [], current: "" }),
    remote: () => {
      throw new Error("no remote machines in this test");
    },
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
    const connected = poll.snapshot.sources
      .filter((s) => s.connected)
      .map((s) => s.provider);
    expect(connected).toEqual(["chatgpt"]);
  });

  it("reads every part of the window in one pass", async () => {
    const { deps, asked } = pollers();
    await pollCore(deps);
    expect(asked.sort()).toEqual([
      "approvals",
      "changeSets",
      "commands",
      "machines",
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
      workspaces: async () => ({
        workspaces: [other],
        currentWorkspaceID: "ws_gone",
      }),
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

  const VPS: MachineInfo = {
    id: "m_1",
    name: "vps-1",
    host: "vps.example",
    state: "online",
    since: "2026-09-12T08:00:00",
  };
  const REMOTE_WS: Workspace = {
    ...WS,
    id: "ws_r1",
    name: "api",
    root_path: "/home/deploy/api",
  };
  const REMOTE_TASK: TaskInfo = {
    task_id: "task_r1",
    state: "running",
    label: "go test ./...",
    exit_code: 0,
    provider: "claude",
    started_at: "2026-09-12T09:00:00",
    duration: 3,
  };
  const REMOTE_APPROVAL: Approval = {
    change_set_id: "chg_r1",
    workspace_id: "ws_r1",
    workspace_name: "api",
    provider: "claude",
    summary: "run go test",
    created_at: "2026-09-12T09:00:00",
    operations: null,
    kind: "command",
    command: ["go", "test"],
  };
  const remote = (over: Partial<RemoteCore> = {}): RemoteCore => ({
    status: async () => ({
      version: "1",
      pending_approvals: 0,
      tunnel: "disabled" as const,
      approval_mode: "safe" as const,
    }),
    workspaces: async () => ({
      workspaces: [REMOTE_WS],
      currentWorkspaceID: "ws_r1",
    }),
    approvals: async () => [REMOTE_APPROVAL],
    tasks: async () => [REMOTE_TASK],
    changeSets: async () => [
      { ...CHANGE_SET, id: "chg_r0", workspace_id: "ws_r1" },
    ],
    resolveApproval: async () => {},
    addWorkspace: async () => REMOTE_WS,
    selectWorkspace: async () => {},
    pauseWorkspace: async () => {},
    resumeWorkspace: async () => {},
    cancelTask: async () => [],
    acceptChangeSet: async () => CHANGE_SET,
    rollbackChangeSet: async () => ({ status: "rolled_back" }),
    commandSettings: async () => ({ rung: "workspace", grants: [] }),
    revokeGrant: async () => ({ rung: "workspace", grants: [] }),
    setNetwork: async () => ({ workspaces: [], currentWorkspaceID: "" }),
    ...over,
  });

  it("stamps what an online machine reports with where it came from", async () => {
    const { deps } = pollers({
      machines: async () => ({ machines: [VPS], current: "" }),
      remote: () => remote(),
      tasks: async () => [
        {
          ...REMOTE_TASK,
          task_id: "task_local",
          started_at: "2026-09-12T08:30:00",
        },
      ],
    });
    const poll = await pollCore(deps);
    expect(poll.machines).toHaveLength(1);
    expect(poll.machines[0].reachable).toBe(true);
    expect(poll.machines[0].workspaces.map((w) => w.id)).toEqual(["ws_r1"]);
    expect(poll.machines[0].currentWorkspaceID).toBe("ws_r1");
    // Newest first across machines, the remote one stamped, the local one not.
    expect(poll.tasks.map((t) => [t.task_id, t.machine ?? ""])).toEqual([
      ["task_r1", "vps-1"],
      ["task_local", ""],
    ]);
    expect(poll.snapshot.approvals.map((a) => a.machine_id)).toEqual(["m_1"]);
    expect(poll.snapshot.changeSets.map((c) => c.id)).toEqual([
      "chg_1",
      "chg_r0",
    ]);
    // The rail's own anchor is still this computer's folder.
    expect(poll.snapshot.workspace?.id).toBe("ws_1");
  });

  it("does not ask a machine that is not online, and survives one that does not answer", async () => {
    let asked = 0;
    const { deps } = pollers({
      machines: async () => ({
        machines: [
          { ...VPS, id: "m_off", state: "missing" },
          { ...VPS, id: "m_gone" },
        ],
        current: "",
      }),
      remote: (id) => {
        asked++;
        return remote({
          workspaces: async () => {
            throw new Error(`${id} did not answer`);
          },
        });
      },
    });
    const poll = await pollCore(deps);
    expect(asked).toBe(1);
    expect(poll.machines.map((m) => [m.info.id, m.reachable])).toEqual([
      ["m_off", false],
      ["m_gone", false],
    ]);
    expect(poll.snapshot.online).toBe(true);
    expect(poll.snapshot.approvals).toEqual([]);
  });
});
