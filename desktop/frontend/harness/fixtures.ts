import type { MachineView } from "../src/lib/poll";
import type {
  Approval,
  ChangeSet,
  CommandSettingsInfo,
  ConnectInfo,
  CoreStatusInfo,
  MemoryDoc,
  MemoryNote,
  MemorySource,
  PrefsInfo,
  RemoteEntry,
  RemoteListing,
  TaskInfo,
  PairClaim,
  Source,
  Workspace,
} from "../src/lib/core";
import type { LaneSnapshot } from "../src/lib/lane";
import type { SettingsDeps } from "../src/screens/Settings";
import {
  FIRST_BYTES,
  FIRST_FILE,
  type FirstWriteOutcome,
} from "../src/lib/firstwrite";

// Fixtures for the design-fidelity harness (see harness/README.md). They
// mirror the shapes the Core really returns, with the design boards' own
// example content so a screenshot can be diffed against the board.

export const STATUS: CoreStatusInfo = {
  version: "0.0.1",
  pending_approvals: 0,
  tunnel: "connected",
  relay_url: "wss://relay.example/tunnel",
  approval_mode: "safe",
};

export const WORKSPACES: Workspace[] = [
  {
    id: "ws_1",
    name: "ai-workspace",
    mode: "read_write",
    exclude_rules: [],
    sensitive_rules: [],
    status: "active",
    created_at: "2026-07-01T09:00:00Z",
    last_used_at: "2026-08-13T09:58:00Z",
    root_path: "/Users/you/Projects/ai-workspace",
    availability: "available",
  },
  {
    id: "ws_2",
    name: "fylane",
    mode: "read_write",
    exclude_rules: [],
    sensitive_rules: [],
    status: "active",
    created_at: "2026-07-02T09:00:00Z",
    last_used_at: "2026-08-12T10:00:00Z",
    root_path: "/Users/you/Projects/fylane",
    availability: "available",
  },
  {
    id: "ws_3",
    name: "client-site",
    mode: "read_write",
    exclude_rules: [],
    sensitive_rules: [],
    status: "active",
    created_at: "2026-06-02T09:00:00Z",
    last_used_at: "2026-08-10T10:00:00Z",
    root_path: "/Volumes/Work/client-site",
    availability: "unavailable",
  },
  {
    id: "ws_4",
    name: "old-notes",
    mode: "read_write",
    exclude_rules: [],
    sensitive_rules: [],
    status: "active",
    created_at: "2026-05-02T09:00:00Z",
    last_used_at: "2026-07-30T10:00:00Z",
    root_path: "/Users/you/Documents/old-notes",
    availability: "missing",
  },
];

export const SOURCES: Source[] = [
  {
    provider: "chatgpt",
    connected: true,
    last_seen_at: "2026-08-13T09:56:00Z",
    lanes_carried: 68,
  },
  {
    provider: "claude",
    connected: true,
    last_seen_at: "2026-08-13T09:44:00Z",
    lanes_carried: 26,
  },
  { provider: "grok", connected: false, lanes_carried: 0 },
];

const AN_HOUR_AHEAD = "2026-08-13T11:06:00Z";

export const CHANGE_SETS: ChangeSet[] = [
  {
    id: "chg_0001",
    workspace_id: "ws_1",
    provider: "chatgpt",
    summary: "Create FileLane.tsx",
    operations: [{ path: "src/FileLane.tsx", status: "created" }],
    status: "applied",
    created_at: "2026-08-13T09:58:00Z",
    rollback_deadline: AN_HOUR_AHEAD,
  },
  {
    id: "chg_0002",
    workspace_id: "ws_1",
    provider: "claude",
    summary: "Update authentication handling",
    operations: [{ path: "src/auth.ts", status: "updated" }],
    status: "applied",
    created_at: "2026-08-13T09:44:00Z",
    rollback_deadline: AN_HOUR_AHEAD,
  },
  {
    id: "chg_0003",
    workspace_id: "ws_1",
    provider: "claude",
    summary: "Remove stale draft",
    operations: [{ path: "drafts/notes.old.md", status: "deleted" }],
    status: "rolled_back",
    created_at: "2026-08-13T08:44:00Z",
  },
  {
    id: "chg_0004",
    workspace_id: "ws_1",
    provider: "chatgpt",
    summary: "Save the PRD",
    operations: [{ path: "notes/PRD.md", status: "created" }],
    status: "applied",
    created_at: "2026-08-13T06:20:00Z",
  },
  {
    id: "chg_0005",
    workspace_id: "ws_1",
    provider: "grok",
    summary: "Save generated hero image",
    operations: [{ path: "assets/generated/hero.png", status: "created" }],
    status: "applied",
    created_at: "2026-08-13T05:58:00Z",
  },
];

export const HELD: Approval[] = [
  {
    change_set_id: "chg_4182aa01",
    workspace_id: "ws_1",
    workspace_name: "ai-workspace",
    provider: "claude",
    summary: "Update authentication handling",
    created_at: "2026-08-13T10:00:00Z",
    operations: [
      {
        type: "update",
        path: "src/auth.ts",
        diff: [
          "--- a/src/auth.ts",
          "+++ b/src/auth.ts",
          "@@ -41,6 +41,7 @@",
          " export async function verify(token: string) {",
          "-  const claims = decode(token)",
          "+  const claims = await decodeAndVerify(token)",
          '+  if (!claims) throw new AuthError("invalid token")',
          "   return claims",
          " }",
        ].join("\n"),
      },
      { type: "create", path: "src/errors.ts", diff: "" },
    ],
    kind: "write",
  },
];

// The other three questions the same block has to ask. They have no design
// board of their own — the prompt predates command execution — so these exist
// to check the reused visual language against the screens next to them.
export const HELD_COMMAND: Approval[] = [
  {
    change_set_id: "cmd:9f21c0",
    workspace_id: "ws_1",
    workspace_name: "ai-workspace",
    provider: "chatgpt",
    summary: "rm -rf node_modules/.cache",
    created_at: "2026-08-13T10:00:00Z",
    operations: null,
    kind: "command",
    command: ["rm", "-rf", "node_modules/.cache"],
    dir: "packages/web",
    rule: "recursive-delete",
    reason: "removes a directory and everything under it",
  },
];

export const HELD_DISCLOSURE: Approval[] = [
  {
    change_set_id: "cmd:3ab7e5",
    workspace_id: "ws_1",
    workspace_name: "ai-workspace",
    provider: "chatgpt",
    summary: "ps -eo pid,etime,stat,comm,args",
    created_at: "2026-08-13T10:00:00Z",
    operations: null,
    kind: "disclosure",
    command: ["ps", "-eo", "pid,etime,stat,comm,args"],
    dir: "",
    rule: "reads-machine-state",
    reason:
      "reports the state of this whole machine rather than of the workspace, and machine state routinely carries credentials",
  },
];

export const CLAIM: PairClaim = {
  request_id: "req_1",
  client_name: "ChatGPT",
  verify_code: "EF-RE",
  created_at: "2026-08-13T10:00:00Z",
};

// The command gate on the Safety board. One grant, so the board shows a
// granted workspace by name rather than the empty state.
export const COMMANDS: CommandSettingsInfo = {
  rung: "workspace",
  grants: [
    {
      workspace_id: "ws_1",
      rung: "workspace",
      granted_at: "2026-08-12T14:20:00Z",
    },
  ],
};

// Tasks for the lane's recent line and the record board. Durations are what
// the Core sends: a Go time.Duration, in nanoseconds.
const MS = 1e6;

export const TASKS: TaskInfo[] = [
  {
    task_id: "tsk_1",
    state: "failed",
    label: "npm run build",
    dir: "web",
    exit_code: 1,
    provider: "claude",
    started_at: "2026-08-13T09:46:00Z",
    duration: 8400 * MS,
    stdout:
      "> fylane-web@0.1.0 build\n> vite build\n\nvite v5.4.8 building for production...\n✓ 214 modules transformed.",
    stderr:
      'error during build:\nCould not resolve "./lib/missing" from "src/app.tsx"',
    stdout_cursor: 9214,
  },
  {
    task_id: "tsk_2",
    state: "succeeded",
    label: "ps -eo pid,etime,stat,comm,args",
    dir: "",
    exit_code: 0,
    provider: "claude",
    started_at: "2026-08-13T09:38:00Z",
    duration: 60 * MS,
    stdout:
      "  PID     ELAPSED STAT COMM\n  482    01:12:03 S    node\n  911    00:04:41 S    go",
    stdout_cursor: 2048,
  },
  {
    task_id: "tsk_3",
    state: "running",
    label: "go test ./...",
    dir: "",
    exit_code: 0,
    provider: "grok",
    started_at: "2026-08-13T09:59:40Z",
    duration: 20_000 * MS,
    stdout: "ok  \tfylane/companion/internal/tasks\t0.351s",
  },
  {
    task_id: "tsk_4",
    state: "canceled",
    label: "tail -n 200 logs/dev.log",
    dir: "",
    exit_code: 130,
    provider: "chatgpt",
    started_at: "2026-08-13T07:41:00Z",
    duration: 2100 * MS,
    stdout: "listening on :5173",
  },
  {
    task_id: "tsk_5",
    state: "succeeded",
    label: "node scripts/check-env.mjs",
    dir: "",
    exit_code: 0,
    provider: "claude",
    started_at: "2026-08-12T18:22:55Z",
    duration: 340 * MS,
    stdout: "environment looks fine",
  },
];

export const NOW = new Date("2026-08-13T10:00:00Z");

// Settings. The page reads the Core for three of its four sections, so the
// board injects them; otherwise the connection list and the rungs would draw
// empty and could not be checked against boards 07 and 08.
export const PREFS: PrefsInfo = {
  task_timeout_seconds: 60,
  allow_stop_tasks: true,
  autostart: { supported: true, enabled: true },
  read_boundary: { state: "enforced", detail: "subprocess reads are bounded" },
};

export const CONNECT: ConnectInfo = {
  mode: "relay",
  state: "running",
  connector_url: "https://relay.fylane.app/c/8f21ab90",
  relay_url: "wss://relay.fylane.app/tunnel",
  providers: [
    // The four ways in, at the four states the panel has to draw: ready with
    // nothing to set up, signed in, not installed, and installed but with no
    // credential yet.
    {
      kind: "cloudflare-quick",
      binary: "cloudflared",
      install: "brew install cloudflared",
      download:
        "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
      installed: true,
      needs_token: false,
      needs_hostname: false,
      stable: false,
      setup: "none",
      authorized: true,
      checkable: false,
      can_sign_out: false,
      opens_browser: false,
    },
    {
      kind: "cloudflare-named",
      binary: "cloudflared",
      install: "brew install cloudflared",
      download:
        "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
      installed: true,
      needs_token: false,
      needs_hostname: true,
      stable: true,
      setup: "browser",
      authorized: true,
      checkable: true,
      can_sign_out: true,
      opens_browser: true,
    },
    {
      kind: "tailscale-funnel",
      binary: "tailscale",
      install: "install Tailscale",
      download: "https://tailscale.com/download",
      installed: false,
      needs_token: false,
      needs_hostname: false,
      stable: true,
      setup: "browser",
      authorized: false,
      checkable: false,
      can_sign_out: false,
      opens_browser: false,
    },
    {
      kind: "ngrok",
      binary: "ngrok",
      install: "brew install ngrok",
      download: "https://ngrok.com/download",
      credential: "https://dashboard.ngrok.com/get-started/your-authtoken",
      installed: true,
      needs_token: true,
      needs_hostname: false,
      stable: false,
      setup: "token",
      authorized: false,
      checkable: true,
      can_sign_out: true,
      opens_browser: false,
    },
  ],
};

export const SETTINGS_DEPS: SettingsDeps = {
  prefs: async () => PREFS,
  status: async () => STATUS,
  setMode: async () => ({ approval_mode: "safe" }),
  dock: async () => ({ supported: true, hidden: false }),
  setDock: async (hidden: boolean) => ({ supported: true, hidden }),
  save: async () => PREFS,
  connect: async () => CONNECT,
  commands: async () => COMMANDS,
  setRung: async () => COMMANDS,
  revokeGrant: async () => COMMANDS,
  revokeDelegation: async () => COMMANDS,
  setNetwork: async () => ({ workspaces: [], currentWorkspaceID: "" }),
  startSetup: async () => CONNECT,
  cancelSetup: async () => CONNECT,
  startDownload: async () => CONNECT,
  cancelDownload: async () => CONNECT,
  signOut: async () => CONNECT,
  mintCode: async () => ({ code: "7K4M-2QB9", expires_in_seconds: 600 }),
  // The remote machines' own Cores: one folder on vps-1 authorized a week
  // ago, the network switches answer as pressed.
  remote: (id) => {
    const view = MACHINES.find((m) => m.info.id === id);
    const folders = view?.workspaces ?? [];
    const commands: CommandSettingsInfo = {
      rung: "workspace",
      grants:
        id === "m_vps1"
          ? [{ workspace_id: "ws_r1", rung: "workspace", granted_at: "2026-09-05T09:00:00Z" }]
          : [],
    };
    const refuse = () => Promise.reject(new Error("not in the harness"));
    return {
      status: refuse,
      workspaces: async () => ({ workspaces: folders, currentWorkspaceID: folders[0]?.id ?? "" }),
      approvals: async () => [],
      tasks: async () => [],
      changeSets: async () => [],
      resolveApproval: refuse,
      addWorkspace: refuse,
      selectWorkspace: refuse,
      pauseWorkspace: refuse,
      resumeWorkspace: refuse,
      cancelTask: refuse,
      acceptChangeSet: refuse,
      rollbackChangeSet: refuse,
      commandSettings: async () => commands,
      revokeGrant: async () => ({ rung: "workspace", grants: [] }),
      setNetwork: async (wsID, allow) => ({
        workspaces: folders.map((w) => (w.id === wsID ? { ...w, network_reach: allow ? "allowed" : "denied" } : w)),
        currentWorkspaceID: folders[0]?.id ?? "",
      }),
    };
  },
};

// First run. The onboarding boards stand in for the native folder picker and
// for the one real write, so the frames can be walked through without a Core.
export const NO_SOURCES: Source[] = SOURCES.map((s) => ({
  ...s,
  connected: false,
  lanes_carried: 0,
}));

export const ALL_SOURCES: Source[] = SOURCES.map((s) => ({
  ...s,
  connected: true,
}));

export const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

export async function pickFolder(): Promise<Workspace> {
  await wait(200);
  return WORKSPACES[1];
}

export async function testWrite(): Promise<FirstWriteOutcome> {
  await wait(600);
  return { status: "applied", path: FIRST_FILE, bytes: FIRST_BYTES };
}

export function snapshot(over: Partial<LaneSnapshot> = {}): LaneSnapshot {
  return {
    online: true,
    status: STATUS,
    workspace: WORKSPACES[0],
    approvals: [],
    changeSets: CHANGE_SETS,
    sources: SOURCES,
    ...over,
  };
}

// ── remote machines (Batch R) ───────────────────────────────────────────

/** A small directory tree on the fake machine, for the folder sheet. */
const REMOTE_HOME = "/home/deploy";
const REMOTE_TREE: Record<string, RemoteEntry[]> = {
  "/": [{ name: "home" }, { name: "srv" }, { name: "var" }],
  "/home": [{ name: "deploy" }],
  [REMOTE_HOME]: [
    { name: ".cache", hidden: true },
    { name: ".config", hidden: true },
    { name: ".fylane", hidden: true },
    { name: "api", repo: true },
    { name: "fylane", repo: true },
    { name: "notes" },
    { name: "scratch" },
    { name: "site", repo: true },
  ],
  [REMOTE_HOME + "/api"]: [{ name: "cmd" }, { name: "internal" }],
  [REMOTE_HOME + "/fylane"]: [
    { name: "companion" },
    { name: "desktop" },
    { name: "relay" },
  ],
  [REMOTE_HOME + "/notes"]: [],
  [REMOTE_HOME + "/scratch"]: [{ name: "old" }],
  [REMOTE_HOME + "/site"]: [{ name: "public" }],
};

export function remoteListing(path: string): RemoteListing {
  let p = path === "" || path === "~" ? REMOTE_HOME : path;
  if (p.startsWith("~/")) p = REMOTE_HOME + p.slice(1);
  const entries = REMOTE_TREE[p];
  if (!entries) return { entries: [], reason: "nodir" };
  return {
    path: p,
    parent: p === "/" ? undefined : p.slice(0, p.lastIndexOf("/")) || "/",
    home: REMOTE_HOME,
    entries,
  };
}

export const MACHINES: MachineView[] = [
  {
    info: {
      id: "m_vps1",
      name: "vps-1",
      host: "vps.example.com",
      user: "deploy",
      state: "online",
      version: "0.0.4",
      since: "2026-09-12T08:00:00Z",
    },
    workspaces: [
      {
        ...WORKSPACES[0],
        id: "ws_r1",
        name: "api",
        root_path: "/home/deploy/api",
      },
      {
        ...WORKSPACES[1],
        id: "ws_r2",
        name: "worker",
        root_path: "/home/deploy/worker",
      },
    ],
    currentWorkspaceID: "ws_r1",
    reachable: true,
  },
  {
    info: {
      id: "m_build",
      name: "build-box",
      host: "10.0.0.7",
      state: "missing",
      detail: "Fylane is not installed on this machine",
      reason: "missing",
      since: "2026-09-12T08:00:00Z",
    },
    workspaces: [],
    currentWorkspaceID: "",
    reachable: false,
  },
];

/** The task list with a few rows from vps-1 mixed in, newest first. */
export const TASKS_REMOTE: TaskInfo[] = [
  {
    task_id: "tsk_r1",
    state: "running",
    label: "go test ./...",
    dir: "",
    exit_code: 0,
    provider: "claude",
    started_at: "2026-08-13T10:00:00Z",
    duration: 12 * 1000 * MS,
    machine_id: "m_vps1",
    machine: "vps-1",
  },
  ...TASKS.slice(0, 3),
  {
    task_id: "tsk_r2",
    state: "succeeded",
    label: "systemctl --user restart api",
    dir: "",
    exit_code: 0,
    provider: "chatgpt",
    started_at: "2026-08-13T09:40:00Z",
    duration: 900 * MS,
    machine_id: "m_vps1",
    machine: "vps-1",
  },
  ...TASKS.slice(3),
];

export const HELD_REMOTE: Approval[] = [
  {
    ...HELD_COMMAND[0],
    change_set_id: "cmd:r7b2e1",
    workspace_id: "ws_r1",
    workspace_name: "api",
    provider: "claude",
    summary: "go test ./...",
    command: ["go", "test", "./..."],
    machine_id: "m_vps1",
    machine: "vps-1",
  },
];

// ── memory (board 17) ──────────────────────────────────────────────────

function ago(hours: number): string {
  return new Date(NOW.getTime() - hours * 3_600_000).toISOString();
}

export const MEMORY_NOTES: MemoryNote[] = [
  {
    id: 12,
    workspace_id: "ws_1",
    provider: "chatgpt",
    title: "IDLE connection layer passed the two real-account tests",
    body: "Gmail and Fastmail each ran for 30 minutes; median arrival delay 1.8 s. Gmail drops the connection once after 10 idle minutes and the backoff reconnect picks it up without losing mail.\nFastmail's IDLE responses carry RECENT beside EXISTS; the parser ignores it, noted in idle.go.\nNext time delete the poller first, not the settings toggle, or both sync paths run at once.",
    change_set_id: "chg_0001",
    run_id: "tsk_1",
    created_at: ago(1),
  },
  {
    id: 11,
    workspace_id: "ws_1",
    provider: "chatgpt",
    title: "Reconnect backoff capped at 60 seconds",
    body: "Exponential from 1 s, capped at 60 s, not user-adjustable.",
    change_set_id: "chg_0002",
    created_at: ago(3),
  },
  {
    id: 10,
    workspace_id: "ws_1",
    provider: "claude",
    title: "Gmail push API needs Pub/Sub and a public callback; dropped",
    body: "Both mailboxes must work the same way, so IMAP IDLE it is.",
    created_at: ago(26),
  },
  {
    id: 9,
    workspace_id: "ws_1",
    provider: "chatgpt",
    title: "sync_cursor table replaced the JSON state file",
    body: "One row per account; the JSON file is deleted on first run.",
    change_set_id: "chg_0003",
    created_at: ago(72),
  },
  {
    id: 8,
    workspace_id: "ws_1",
    provider: "chatgpt",
    title: "Polling at 5-minute intervals has a median delay of 2 min 40 s",
    body: "Measured over 200 messages with go run ./cmd/measure.",
    run_id: "tsk_2",
    created_at: ago(74),
  },
  {
    id: 7,
    workspace_id: "ws_1",
    provider: "claude",
    title: "Account model unchanged: IDLE hangs off Account, no new entity",
    body: "",
    created_at: ago(96),
  },
  {
    id: 6,
    workspace_id: "ws_1",
    provider: "chatgpt",
    title: "Summary of notes up to #5 (from 2026-08-01)",
    body: "Forty notes from the first week: the poller was measured, the IDLE design chosen, the account model kept.",
    created_at: ago(120),
  },
];

export const MEMORY_DOC: MemoryDoc = {
  state: {
    workspace_id: "ws_1",
    provider: "chatgpt",
    updated_at: ago(2),
    page: {
      goal: "Move inbox sync from polling to IMAP IDLE and get delay under 5 s before v0.4, without touching the account model.",
      progress:
        "IDLE connection layer done and past the Gmail and Fastmail real-account tests; reconnect uses exponential backoff capped at 60 s. Sync state now lives in the SQLite sync_cursor table. The old polling timer is still there, so both paths run at once. The realtime / every-5-minutes toggle in settings is not done.",
      next: "Delete internal/poll and run go test ./...; then the settings toggle; then update CHANGELOG.",
      decisions: [
        "IMAP IDLE, not the Gmail push API: both mailboxes must work, no second path.",
        "Reconnect backoff caps at 60 s and is not user-adjustable.",
        "The polling code is deleted outright, no fallback switch.",
        "Sync state goes in SQLite, not a JSON file.",
        "IDLE hangs off Account; no new entity.",
      ],
      open: [
        "iCloud drops IDLE after 29 minutes: per-provider heartbeat?",
        "The connection ceiling for many accounts idling at once is unmeasured.",
      ],
    },
  },
  notes: MEMORY_NOTES,
  live: 7,
  archived: 40,
};

export const MEMORY_EMPTY: MemoryDoc = { state: null, notes: [], live: 0, archived: 0 };

/** A source that answers from a fixture and never changes it. */
export function memorySource(doc: MemoryDoc): MemorySource {
  return {
    fetch: async (_ws, q) => ({
      ...doc,
      notes: doc.notes.filter(
        (n) =>
          !!n.archived === q.archived &&
          (!q.query || n.title.toLowerCase().includes(q.query.toLowerCase())),
      ),
    }),
    savePage: async (workspace_id, page) => ({
      workspace_id,
      page,
      provider: "user",
      updated_at: NOW.toISOString(),
    }),
    deleteNote: async () => {},
    clear: async () => {},
    export: async () => "",
  };
}
