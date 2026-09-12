import type { MachineView } from "../src/lib/poll";
import type {
  Approval,
  ChangeSet,
  CommandSettingsInfo,
  ConnectInfo,
  CoreStatusInfo,
  PrefsInfo,
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
  setNetwork: async () => ({ workspaces: [], currentWorkspaceID: "" }),
  startSetup: async () => CONNECT,
  cancelSetup: async () => CONNECT,
  startDownload: async () => CONNECT,
  cancelDownload: async () => CONNECT,
  signOut: async () => CONNECT,
  mintCode: async () => ({ code: "7K4M-2QB9", expires_in_seconds: 600 }),
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
