import {
  AcceptChangeSet,
  AddWorkspace,
  ApplyTunnel,
  CancelTask,
  CancelTunnelDownload,
  CancelTunnelSetup,
  ChangeSets,
  ClearBackups,
  ClearTasks,
  CommandSettings,
  Connect,
  CopyText,
  CoreStatus,
  Dock,
  SetDockHidden,
  PairClaims,
  PairingCode,
  PauseWorkspace,
  PendingApprovals,
  RaiseWindow,
  ResolveApproval,
  ResolvePairClaim,
  ResumeWorkspace,
  OpenURL,
  OpenWorkspaceDir,
  RevokeCommandGrant,
  RollbackChangeSet,
  Save,
  SaveSettings,
  SelectWorkspace,
  SetWorkspaceNetwork,
  Settings,
  SetCommandRung,
  SetWriteMode,
  SignOutTunnel,
  Sources,
  StartCore,
  StartTunnelDownload,
  StartTunnelSetup,
  Tasks,
  Workspaces,
  Machines,
  AddMachine,
  RemoveMachine,
  ConnectMachine,
  DisconnectMachine,
  InstallMachine,
  MachineCall,
  ProbeMachine,
  BrowseMachine,
  SelectMachine,
  UpdateMachine,
} from "../../wailsjs/go/main/App";

// Typed wrappers over the Core control API bridge. Every payload is JSON
// produced by companion/internal/ctlapi; the shapes here mirror those
// responses and must stay in sync with them.

export type CoreStatusInfo = {
  version: string;
  pending_approvals: number;
  tunnel: "connected" | "offline" | "disabled";
  relay_url?: string;
  connector_url?: string;
  approval_mode: WriteMode;
  command_rung?: "strict" | "workspace" | "open";
  running_tasks?: number;
  /** The newest release the daily check has seen, and whether it is newer
   *  than this build. Both absent when the check is disabled (it is by
   *  default) — absence means nothing is known, never "you are current". */
  latest_version?: string;
  update_available?: boolean;
  /** Where to get it. Absent until the public release page exists, and a
   *  notice shows no link while it is. */
  download_page?: string;
};

export type OpPreview = {
  type: string;
  path: string;
  to?: string;
  diff?: string;
  sensitive?: boolean;
  recursive_delete?: boolean;
  /** How much a recursive delete takes. Absent means nobody counted — the
   *  budget ran out before the walk started — which is not an empty
   *  directory, and a zero would read as one. */
  tree?: OpTree;
  /** The copy this delete makes will not fit in the recycle area, so the undo
   *  will not have anything behind it. Only ever set when that is known. */
  beyond_undo?: boolean;
  /** How far this change reaches. Absent means nobody asked — no language
   *  server was warm for this file — which is not the same as callers: 0,
   *  meaning the question was asked and nothing else uses it. */
  impact?: OpImpact;
};

export type OpTree = {
  files: number;
  bytes: number;
  /** Both numbers are floors: the walk ran out of budget. */
  partial?: boolean;
};

export type OpImpact = {
  /** The symbols asked about, in file order. */
  symbols?: string[];
  /** Uses in other files. Uses inside this file are not counted: its diff is
   *  on the screen already. */
  callers: number;
  /** The count is a floor: more symbols changed than were asked about, or the
   *  approval budget ended the work early. */
  partial?: boolean;
};

/** Which question a prompt is asking. One screen, five questions: a write is
 *  asked about before it lands, a command before it runs, a disclosure before
 *  data leaves, a delegation before an agent starts working unattended, and a
 *  proxy before Fylane forwards a call to an MCP server it cannot inspect. The
 *  Core states it rather than leaving the screen to guess from which fields
 *  are populated. */
export type ApprovalKind =
  "write" | "command" | "disclosure" | "delegation" | "proxy";

export type Approval = MachineTag & {
  change_set_id: string;
  workspace_id: string;
  workspace_name: string;
  provider: string;
  summary: string;
  created_at: string;
  operations: OpPreview[] | null;
  kind: ApprovalKind;
  /** argv of a command or delegation prompt; a command has no diff to show,
   *  so this and `reason` are the whole basis for the decision. */
  command?: string[] | null;
  /** Workspace-relative working directory of a command. */
  dir?: string;
  /** Stable rule id from the Core's rule table, for keying — not for reading. */
  rule?: string;
  /** That rule's own sentence about why it stopped this command. */
  reason?: string;
  /** True when approving also authorizes the whole workspace, not just this
   *  one command. */
  grant?: boolean;
  /** True when the user's own route rule asked for this stop. */
  must_ask?: boolean;
  /** What this run gets from the outbound boundary: "allowed", "denied",
   *  "partial" or "unbounded". A statement, never a second question — the
   *  prompt asks whether the command may run and nothing here could work out
   *  in advance whether it needs the network. */
  network?: string;
};

/** Availability of a granted folder. A desktop grant never expires on its
 * own: the only ways to lose one are the volume not being mounted
 * ("unavailable") or the directory having moved ("missing"). */
export type Availability = "available" | "unavailable" | "missing";

export type Workspace = {
  id: string;
  name: string;
  mode: string;
  exclude_rules: string[];
  sensitive_rules: string[];
  status: string;
  created_at: string;
  last_used_at?: string;
  // root_path is served only on this loopback surface for local display;
  // it never leaves the machine.
  root_path: string;
  availability: Availability;
  /** What this workspace asks for about outbound traffic: "allow" or "deny".
   *  Absent from a Core that predates the field, which read as allow. */
  network?: string;
  /** What its programs actually get: "allowed", "denied", "partial" (TCP only
   *  — Landlock has no UDP rule, so DNS and QUIC still leave), or "unbounded"
   *  (asked for and this machine cannot deliver it). Computed by the Core so
   *  the rule lives in one language. */
  network_reach?: string;
};

export type ChangeSetOp = {
  path: string;
  status: string;
  sha256?: string;
};

export type ChangeSet = MachineTag & {
  id: string;
  workspace_id: string;
  provider: string;
  summary: string;
  operations: ChangeSetOp[] | null;
  status: string;
  created_at: string;
  applied_at?: string;
  rollback_deadline?: string;
  /** When the user reviewed this write and said it was right. Absent means
   *  nobody has — which is not the same as the undo window having closed. */
  accepted_at?: string;
};

export type Source = {
  provider: string;
  connected: boolean;
  last_seen_at?: string;
  lanes_carried: number;
};

export async function fetchStatus(): Promise<CoreStatusInfo> {
  return JSON.parse(await CoreStatus());
}

// startCore asks the shell to launch the Companion core when it is not
// already running; the regular status poll picks it up once it answers.
export async function startCore(): Promise<string> {
  return StartCore();
}

export async function copyText(text: string): Promise<void> {
  await CopyText(text);
}

// raiseWindow surfaces the window when a new approval needs attention.
export async function raiseWindow(): Promise<void> {
  await RaiseWindow();
}

export type PairClaim = {
  request_id: string;
  client_name: string;
  verify_code: string;
  created_at: string;
};

// fetchPairClaims lists push-pairing prompts awaiting the local decision.
export async function fetchPairClaims(): Promise<PairClaim[]> {
  const res = JSON.parse(await PairClaims());
  return res.claims ?? [];
}

export async function resolvePairClaim(
  requestID: string,
  approved: boolean,
): Promise<void> {
  await ResolvePairClaim(requestID, approved);
}

/** A code a platform's connect page will accept, and how long it lasts. */
export type PairingCodeInfo = { code: string; expires_in_seconds: number };

// mintPairingCode asks the Core for a fresh code. Only ever on a deliberate
// press: a code minted on render is a code that changes under whoever is
// typing it into a platform (the 2026-08-15 defect).
export async function mintPairingCode(): Promise<PairingCodeInfo> {
  const res = JSON.parse(await PairingCode());
  return {
    code: res.code ?? "",
    expires_in_seconds: res.expires_in_seconds ?? 0,
  };
}

export async function fetchApprovals(): Promise<Approval[]> {
  const res = JSON.parse(await PendingApprovals());
  return res.approvals ?? [];
}

export async function resolveApproval(
  changeSetID: string,
  approved: boolean,
): Promise<void> {
  await ResolveApproval(changeSetID, approved);
}

export async function fetchWorkspaces(): Promise<{
  workspaces: Workspace[];
  currentWorkspaceID: string;
}> {
  const res = JSON.parse(await Workspaces());
  return {
    workspaces: res.workspaces ?? [],
    currentWorkspaceID: res.current_workspace_id ?? "",
  };
}

export async function fetchChangeSets(
  workspaceID: string,
): Promise<ChangeSet[]> {
  const res = JSON.parse(await ChangeSets(workspaceID));
  return res.change_sets ?? [];
}

// addWorkspace opens the native folder picker; null means cancelled.
export async function addWorkspace(): Promise<Workspace | null> {
  const res = await AddWorkspace();
  return res ? JSON.parse(res) : null;
}

export async function selectWorkspace(id: string): Promise<void> {
  await SelectWorkspace(id);
}

/** Records the workspace's answer and returns the whole list back, because
 *  the effective answer is not the setting: a machine that cannot deny
 *  anything answers "unbounded" to a workspace that asked for "deny". */
export async function setWorkspaceNetwork(
  id: string,
  allow: boolean,
): Promise<{ workspaces: Workspace[]; currentWorkspaceID: string }> {
  const res = JSON.parse(await SetWorkspaceNetwork(id, allow));
  return {
    workspaces: res.workspaces ?? [],
    currentWorkspaceID: res.current_workspace_id ?? "",
  };
}

export async function pauseWorkspace(id: string): Promise<void> {
  await PauseWorkspace(id);
}

export async function resumeWorkspace(id: string): Promise<void> {
  await ResumeWorkspace(id);
}

/** How a provider is authorized. "browser" runs the vendor's own sign-in and
 *  the user approves in a browser; "token" means the vendor issues a token on
 *  a page and there is no CLI flow, so it is pasted once; "none" needs no
 *  account at all. No provider asks the user to open a terminal. */
export type TunnelSetup = "none" | "browser" | "token";

/** One way to publish this machine, as the Core reports it. */
export type TunnelProvider = {
  kind: string;
  binary: string;
  install: string;
  installed: boolean;
  needs_token: boolean;
  needs_hostname: boolean;
  /** Whether the address survives a restart. */
  stable: boolean;
  setup: TunnelSetup;
  /** The vendor's own download page, offered when `installed` is false. It is
   *  the whole answer on a platform this build has no pin for, and stays the
   *  second answer everywhere else — a pinned fetch became possible, but it
   *  did not make installing it yourself the lesser path. */
  download?: string;
  /** The pinned build Fylane could fetch instead. Present only when the
   *  program is missing and this build has a pin for this platform; absent
   *  means there is nothing to offer, which the screen says rather than
   *  showing an offer it cannot honour. */
  offer?: TunnelOffer;
  /** Where a "token" provider issues its token. */
  credential?: string;
  authorized: boolean;
  /** False when `authorized` is the benefit of the doubt rather than a read
   *  of local state — the screen must not claim to know it is signed in. */
  checkable: boolean;
  /** Whether Fylane can undo the sign-in. False where the credential is not
   *  Fylane's to remove — Tailscale signs the whole machine in. */
  can_sign_out: boolean;
  /** The address to offer, worked out from the sign-in. Absent until the Core
   *  knows it — the lookup is a network call and the snapshot never waits. */
  suggested_hostname?: string;
  /** True when the sign-in command opens the browser itself. Opening it here
   *  as well gives one authorization two tabs, one of which flashes past. */
  opens_browser: boolean;
};

/** What would be downloaded, shown before the user is asked. These are the
 *  same three facts the download is bound by — the consent is informed by
 *  what actually constrains it. */
export type TunnelOffer = {
  binary: string;
  version: string;
  /** The publisher's release address, without the version or file name —
   *  those are already on their own lines. */
  source: string;
  sha256: string;
};

export type DownloadPhase = "running" | "ready" | "failed";

export type TunnelDownloadState = {
  provider?: string;
  phase: DownloadPhase;
  detail?: string;
};

export type SetupPhase = "starting" | "waiting" | "ready" | "failed";

/** A browser sign-in in progress. Absent when nothing is running. */
export type TunnelSetupState = {
  provider?: string;
  phase: SetupPhase;
  /** The page to approve on. Shown as well as opened, so a user whose
   *  browser did not come up can still get there. */
  url?: string;
  detail?: string;
};

export type ConnectInfo = {
  mode: "direct" | "relay";
  provider?: string;
  state: "stopped" | "starting" | "running" | "failed" | "";
  detail?: string;
  public_url?: string;
  connector_url?: string;
  /** The relay this machine paired with, in either mode: the way back. */
  relay_url?: string;
  /** The Core stored a new mode and is exiting; wait for it to come back. */
  restarting?: boolean;
  providers: TunnelProvider[];
  setup?: TunnelSetupState;
  /** The program fetch running or last finished. */
  download?: TunnelDownloadState;
  /** This machine's target as Go names it ("darwin/arm64"). The screen needs
   *  it to say which platform a build has no pin for — without it, "we have
   *  nothing for you" reads as a fault rather than a gap. */
  platform?: string;
};

// fetchConnect reads the connection screen's state, in either mode.
export async function fetchConnect(): Promise<ConnectInfo> {
  const res = JSON.parse(await Connect());
  return { ...res, providers: res.providers ?? [] };
}

// startTunnelSetup runs a provider's own browser sign-in. It answers as soon
// as the command is up — the URL to open arrives on a later read, because the
// user is about to spend minutes in a browser.
export async function startTunnelSetup(provider: string): Promise<ConnectInfo> {
  const res = JSON.parse(await StartTunnelSetup(provider));
  return { ...res, providers: res.providers ?? [] };
}

// signOutTunnel forgets one provider's credential on this machine. What lives
// on the vendor's account is untouched: this undoes the sign-in, not the
// account.
export async function signOutTunnel(provider: string): Promise<ConnectInfo> {
  const res = JSON.parse(await SignOutTunnel(provider));
  return { ...res, providers: res.providers ?? [] };
}

export async function cancelTunnelSetup(): Promise<ConnectInfo> {
  const res = JSON.parse(await CancelTunnelSetup());
  return { ...res, providers: res.providers ?? [] };
}

// startTunnelDownload fetches the pinned build of a provider's program. The
// provider name is the whole request: what is fetched, from where and at which
// digest are fixed inside the Core, so there is nothing here for a
// caller to point somewhere else. It answers as soon as the transfer is
// running — progress arrives on the next read.
export async function startTunnelDownload(
  provider: string,
): Promise<ConnectInfo> {
  const res = JSON.parse(await StartTunnelDownload(provider));
  return { ...res, providers: res.providers ?? [] };
}

export async function cancelTunnelDownload(): Promise<ConnectInfo> {
  const res = JSON.parse(await CancelTunnelDownload());
  return { ...res, providers: res.providers ?? [] };
}

/** Opens a vendor page in the user's browser. https only, enforced by the
 *  shell — a window that would open anything is a way to launch handlers. */
export async function openURL(url: string): Promise<void> {
  await OpenURL(url);
}

export type TunnelRequest = {
  /** "relay" hands publishing back to the paired relay, ignoring provider. */
  mode?: "relay";
  provider: string;
  hostname?: string;
  domain?: string;
  /** Sent once, stored by the Core in the OS keychain, never read back. */
  token?: string;
};

export async function applyTunnel(req: TunnelRequest): Promise<ConnectInfo> {
  const res = JSON.parse(await ApplyTunnel(JSON.stringify(req)));
  return { ...res, providers: res.providers ?? [] };
}

export async function fetchSources(): Promise<Source[]> {
  const res = JSON.parse(await Sources());
  return res.sources ?? [];
}

// Command execution (Stage F). A task is one run_command or code_task that
// outlived the call that started it; a grant is a workspace the user
// authorized so ordinary commands stop asking.
/** "denied" and "interrupted" only ever come out of the audit log. A command
 *  the user refused never became a task, which is why the screen could not
 *  show one; an interrupted one did become a task, and then the Core stopped
 *  while it was running, so nobody knows how it ended or what it left. That
 *  is deliberately not "canceled" — cancelling is something a person did. */
export type TaskState =
  | "running"
  | "succeeded"
  | "failed"
  | "timed_out"
  | "canceled"
  | "denied"
  | "interrupted";

/** Where a record came from. Stamped by the poll on records read from
 *  another machine — the Core never sends it, and a record without it is
 *  from this computer. */
export type MachineTag = {
  machine_id?: string;
  machine?: string;
};

export type TaskInfo = MachineTag & {
  task_id: string;
  state: TaskState;
  label?: string;
  dir?: string;
  exit_code: number;
  error?: string;
  stdout?: string;
  stderr?: string;
  /** Byte offsets into the captured streams — and so the true size of what
   *  this command produced and handed to the platform. */
  stdout_cursor?: number;
  stderr_cursor?: number;
  /** The platform that asked for this work, when the task recorded one. */
  provider?: string;
  started_at: string;
  duration: number;
};

export type CommandRung = "strict" | "workspace" | "open";

export type CommandGrant = {
  workspace_id: string;
  rung: string;
  granted_at: string;
};

/** One locally configured MCP gateway provider. Read-only here: a provider is
 *  a program to run, and naming one is not something any surface but the
 *  config file on this machine gets to do. */
export type ProxyProvider = {
  name: string;
  /** "ask" stops for approval on every call whatever the rung says;
   *  "workspace" follows the rung and the workspace grant. */
  trust: string;
};

/** One installed language server code_navigate may start. Read-only for the
 *  same reason a provider is: it names a program this machine runs, and the
 *  config file on this machine is the only place that gets to say so. */
export type LanguageServer = {
  name: string;
  /** The suffixes it answers for. "gopls" says nothing on its own. */
  extensions: string[];
  /** Whether one is up right now, in any workspace. */
  running: boolean;
};

export type CommandSettingsInfo = {
  rung: CommandRung;
  grants: CommandGrant[];
  /** Absent from a Core that predates the field; an older Core with a
   *  gateway configured shows an empty list rather than a wrong one. */
  providers?: ProxyProvider[];
  /** Absent for the same reason, and read the same way. */
  language_servers?: LanguageServer[];
};

export async function fetchTasks(): Promise<TaskInfo[]> {
  const res = JSON.parse(await Tasks());
  return res.tasks ?? [];
}

export async function cancelTask(taskID: string): Promise<TaskInfo[]> {
  const res = JSON.parse(await CancelTask(taskID));
  return res.tasks ?? [];
}

// clearTasks forgets what has finished. Running tasks stay — they are not a
// record yet — so the call answers with the list that is left.
export async function clearTasks(): Promise<TaskInfo[]> {
  const res = JSON.parse(await ClearTasks());
  return res.tasks ?? [];
}

// Execution preferences (the settings page's execution section). None of
// these moves the
// approval line — the rung, the rule table and the audit log are unaffected by
// them. What they decide is how long a command may run, whether the
// window offers a stop button, whether the Core comes back after a reboot, and
// whether the kernel read boundary is in force.
export type AutostartInfo = {
  /** False where this build cannot register a login entry; the row is then
   *  disabled with `detail` as its explanation rather than offering a switch
   *  that would do nothing. */
  supported: boolean;
  enabled: boolean;
  detail?: string;
};

/** What this machine does about subprocess reads.
 *
 *  `enforced` bounds every child the Companion starts; `absent` is a platform
 *  that has no such boundary (Windows, a kernel without Landlock, a macOS
 *  without sandbox-exec); `off` is the user's own choice on a machine that
 *  could. The last two are drawn differently on purpose: a switch sitting in
 *  the off position where the platform decided would blame a person for
 *  something they did not do.
 *
 *  Kept in step with readbox.State by prefs_wire_test.go. */
export type ReadBoundaryState = "enforced" | "absent" | "off";

export type ReadBoundaryInfo = {
  state: ReadBoundaryState;
  /** One sentence saying why, safe to show: it never carries a path. */
  detail: string;
};

/** Whether the Dock icon is hidden — a shell setting, not a Core one: it is
 *  about how this window presents itself and has to be applied before the
 *  Core has answered anything, so the shell keeps it in its own file.
 *
 *  `supported` false means there is no Dock on this platform and the page
 *  draws no row at all: this is a convenience, not a boundary, so its absence
 *  is not something a person needs told. */
export type DockInfo = {
  supported: boolean;
  hidden: boolean;
};

export async function fetchDock(): Promise<DockInfo> {
  return JSON.parse(await Dock());
}

/** The shell answers with the state now in force, which the page redraws
 *  from — the switch must not sit where the click put it if the change was
 *  refused. */
export async function setDockHidden(hidden: boolean): Promise<DockInfo> {
  return JSON.parse(await SetDockHidden(hidden));
}

export type PrefsInfo = {
  task_timeout_seconds: number;
  allow_stop_tasks: boolean;
  autostart: AutostartInfo;
  read_boundary: ReadBoundaryInfo;
};

/** Only the fields being changed. A page that flips one switch must not
 *  restate — and so risk overwriting — the values it did not touch. */
export type PrefsPatch = {
  task_timeout_seconds?: number;
  allow_stop_tasks?: boolean;
  autostart?: boolean;
  read_boundary?: boolean;
};

export async function fetchPrefs(): Promise<PrefsInfo> {
  return JSON.parse(await Settings());
}

export async function savePrefs(patch: PrefsPatch): Promise<PrefsInfo> {
  return JSON.parse(await SaveSettings(JSON.stringify(patch)));
}

export async function fetchCommandSettings(): Promise<CommandSettingsInfo> {
  return JSON.parse(await CommandSettings());
}

// confirm is only ever true for the open rung, and the Core refuses that rung
// without it — the acknowledgement lives on both sides on purpose.
export async function setCommandRung(
  rung: CommandRung,
  confirm: boolean,
): Promise<CommandSettingsInfo> {
  return JSON.parse(await SetCommandRung(rung, confirm));
}

/** The file-write approval policy, the second of the two approval axes.
 *
 *  `safe` asks before every change set. `balanced` lets one shape through
 *  unasked — a set whose every operation creates a new, non-sensitive file;
 *  an edit, a delete or a sensitive path anywhere in it still asks. It is
 *  deliberately independent of the command rung: a write is
 *  transactional and has a rollback window, a command has neither.
 *
 *  The Core answers with the mode now in force rather than echoing the
 *  request, so a refusal cannot leave the page showing a mode nobody set. */
export type WriteMode = "safe" | "balanced";

export async function setWriteMode(
  mode: WriteMode,
): Promise<{ approval_mode: WriteMode }> {
  return JSON.parse(await SetWriteMode(mode));
}

// Withdrawing takes effect on the next command; the Core answers with the
// settings the page redraws from, so the list cannot disagree with what it
// now holds.
export async function revokeCommandGrant(
  workspaceID: string,
): Promise<CommandSettingsInfo> {
  return JSON.parse(await RevokeCommandGrant(workspaceID));
}

export type SaveFile = {
  path: string;
  content_base64: string;
};

export type SaveRequest = {
  workspace_id?: string;
  provider: string;
  summary: string;
  files: SaveFile[];
  change_set_id?: string;
};

/** Outcome of one change set (companion/internal/txn.Result). */
export type SaveResult = {
  status: string;
  change_set_id?: string;
  operations?: { path: string; status: string }[];
  conflict?: { path?: string; reason?: string } | null;
  reason?: string;
};

// saveFiles writes through the Core's save endpoint, which runs the same
// transaction engine and local approval as every other write.
export async function saveFiles(req: SaveRequest): Promise<SaveResult> {
  return JSON.parse(await Save(JSON.stringify(req)));
}

/** What clearing the undo copies actually removed. */
export type ClearedBackups = {
  cleared: number;
  backups_removed: number;
};

// clearBackups removes the undo copies for a workspace. The write records
// stay — what goes is the ability to take those writes back, so the rollback
// deadline goes with the copies and the undo button turns itself off.
export async function clearBackups(
  workspaceID: string,
): Promise<ClearedBackups> {
  return JSON.parse(await ClearBackups(workspaceID));
}

export type RollbackResult = {
  status: string;
  change_set_id?: string;
  conflict?: { path?: string; reason?: string } | null;
  reason?: string;
};

export async function rollbackChangeSet(
  workspaceID: string,
  changeSetID: string,
): Promise<RollbackResult> {
  return JSON.parse(await RollbackChangeSet(workspaceID, changeSetID));
}

/** Records the user's verdict on an applied write. It authorizes nothing and
 *  blocks nothing; undo stays available until the window closes either way.
 *  Returns the change set as it now stands. */
export async function acceptChangeSet(
  workspaceID: string,
  changeSetID: string,
): Promise<ChangeSet> {
  return JSON.parse(await AcceptChangeSet(workspaceID, changeSetID));
}

/** Reveals a granted folder in the platform's file manager. */
export async function openWorkspaceDir(path: string): Promise<void> {
  await OpenWorkspaceDir(path);
}

// ── remote machines ──────────────────────────────────────────────────────

export type MachineState =
  | "off"
  | "connecting"
  | "missing"
  | "installing"
  | "starting"
  | "online"
  | "error";

/** One remote machine as the Core reports it. Never a token, never a port:
 *  the link's secrets stay in the Core. */
export type MachineInfo = {
  id: string;
  name: string;
  host: string;
  user?: string;
  port?: number;
  state: MachineState;
  /** The Core's own sentence about a state that needs one (why it cannot
   *  connect, which version it found). */
  detail?: string;
  /** A code for `detail` when the window has its own words for it:
   *  "host_key", "auth", "resolve", "unreachable". */
  reason?: string;
  version?: string;
  since: string;
};

/** What is at an address, asked before the machine is saved. */
export type ProbeResult = {
  reachable: boolean;
  detail?: string;
  reason?: string;
  version?: string;
  running: boolean;
  compatible: boolean;
};

/** One directory on a remote machine, for the folder sheet to walk through.
 *  Only directories are listed. */
export type RemoteListing = {
  /** The directory as the machine resolved it; absent when there is a reason. */
  path?: string;
  /** The directory above; absent at the root. */
  parent?: string;
  home?: string;
  entries: RemoteEntry[];
  /** Why there is no listing: "nodir", "denied", or a link reason code. */
  reason?: string;
  detail?: string;
};

export type RemoteEntry = {
  name: string;
  /** Holds a .git. */
  repo?: boolean;
  /** A dot-directory. */
  hidden?: boolean;
};

/** browseMachine lists the directories inside `path` on a machine; an empty
 *  path is that machine's login home. */
export async function browseMachine(id: string, path: string): Promise<RemoteListing> {
  return JSON.parse(await BrowseMachine(id, path));
}

export async function probeMachine(req: MachineRequest): Promise<ProbeResult> {
  return JSON.parse(await ProbeMachine(JSON.stringify(req)));
}

export async function updateMachine(req: MachineRequest & { id: string }): Promise<MachineInfo> {
  return JSON.parse(await UpdateMachine(JSON.stringify(req)));
}

/** The machine list and which machine the window stands on ("" is this
 *  computer). The choice is the Core's, not the window's: it decides which
 *  folder workspace_info calls current. */
export type MachineList = { machines: MachineInfo[]; current: string };

export async function fetchMachines(): Promise<MachineList> {
  let raw: string;
  try {
    raw = await Machines();
  } catch (e) {
    // A Core from before this endpoint answers 404. That is "no machines",
    // not an unreachable Core — the rest of the window must keep drawing.
    if (e instanceof Error && /\(404\)/.test(e.message))
      return { machines: [], current: "" };
    throw e;
  }
  const res = JSON.parse(raw);
  return { machines: res.machines ?? [], current: res.current ?? "" };
}

/** selectMachine tells the Core which machine the window stands on. */
export async function selectMachine(id: string): Promise<void> {
  await SelectMachine(id);
}

export type MachineRequest = {
  name: string;
  host: string;
  user?: string;
  port?: number;
};

export async function addMachine(req: MachineRequest): Promise<MachineInfo> {
  return JSON.parse(await AddMachine(JSON.stringify(req)));
}

export async function removeMachine(id: string): Promise<void> {
  await RemoveMachine(id);
}

export async function connectMachine(id: string): Promise<void> {
  await ConnectMachine(id);
}

export async function disconnectMachine(id: string): Promise<void> {
  await DisconnectMachine(id);
}

export async function installMachine(id: string): Promise<void> {
  await InstallMachine(id);
}

/** The control API of one remote machine, reached through the local Core's
 *  per-machine proxy. The shapes are the same documents the local calls
 *  above read, because it is the same Core on the other end. */
export interface RemoteCore {
  status(): Promise<CoreStatusInfo>;
  workspaces(): Promise<{
    workspaces: Workspace[];
    currentWorkspaceID: string;
  }>;
  approvals(): Promise<Approval[]>;
  tasks(): Promise<TaskInfo[]>;
  changeSets(workspaceID: string): Promise<ChangeSet[]>;
  resolveApproval(changeSetID: string, approved: boolean): Promise<void>;
  addWorkspace(path: string): Promise<Workspace>;
  selectWorkspace(id: string): Promise<void>;
  pauseWorkspace(id: string): Promise<void>;
  resumeWorkspace(id: string): Promise<void>;
  cancelTask(taskID: string): Promise<TaskInfo[]>;
  acceptChangeSet(workspaceID: string, changeSetID: string): Promise<ChangeSet>;
  rollbackChangeSet(
    workspaceID: string,
    changeSetID: string,
  ): Promise<RollbackResult>;
  commandSettings(): Promise<CommandSettingsInfo>;
  revokeGrant(workspaceID: string): Promise<CommandSettingsInfo>;
  setNetwork(
    id: string,
    allow: boolean,
  ): Promise<{ workspaces: Workspace[]; currentWorkspaceID: string }>;
}

export function remoteCore(machineID: string): RemoteCore {
  const call = async (method: string, path: string, body?: unknown) =>
    JSON.parse(
      await MachineCall(
        machineID,
        method,
        path,
        body === undefined ? "" : JSON.stringify(body),
      ),
    );
  return {
    status: () => call("GET", "/v1/status"),
    workspaces: async () => {
      const res = await call("GET", "/v1/workspaces");
      return {
        workspaces: res.workspaces ?? [],
        currentWorkspaceID: res.current_workspace_id ?? "",
      };
    },
    approvals: async () => (await call("GET", "/v1/approvals")).approvals ?? [],
    tasks: async () => (await call("GET", "/v1/tasks")).tasks ?? [],
    changeSets: async (workspaceID) =>
      (
        await call(
          "GET",
          `/v1/changesets?workspace_id=${encodeURIComponent(workspaceID)}`,
        )
      ).change_sets ?? [],
    resolveApproval: async (changeSetID, approved) => {
      await call("POST", "/v1/approvals/resolve", {
        change_set_id: changeSetID,
        approved,
      });
    },
    addWorkspace: (path) => call("POST", "/v1/workspaces/add", { path }),
    selectWorkspace: async (id) => {
      await call("POST", "/v1/workspaces/select", { id });
    },
    pauseWorkspace: async (id) => {
      await call("POST", "/v1/workspaces/pause", { id });
    },
    resumeWorkspace: async (id) => {
      await call("POST", "/v1/workspaces/resume", { id });
    },
    cancelTask: async (taskID) =>
      (await call("POST", "/v1/tasks/cancel", { task_id: taskID })).tasks ?? [],
    acceptChangeSet: (workspaceID, changeSetID) =>
      call("POST", "/v1/changesets/accept", {
        workspace_id: workspaceID,
        change_set_id: changeSetID,
      }),
    rollbackChangeSet: (workspaceID, changeSetID) =>
      call("POST", "/v1/changesets/rollback", {
        workspace_id: workspaceID,
        change_set_id: changeSetID,
        confirmed_in: "desktop",
      }),
    commandSettings: () => call("GET", "/v1/commands"),
    revokeGrant: (workspaceID) =>
      call("POST", "/v1/commands/revoke", { workspace_id: workspaceID }),
    setNetwork: async (id, allow) => {
      const res = await call("POST", "/v1/workspaces/network", { id, allow });
      return {
        workspaces: res.workspaces ?? [],
        currentWorkspaceID: res.current_workspace_id ?? "",
      };
    },
  };
}
