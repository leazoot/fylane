import {
  fetchApprovals,
  fetchChangeSets,
  fetchCommandSettings,
  fetchMachines,
  fetchPrefs,
  fetchSources,
  fetchStatus,
  fetchTasks,
  fetchWorkspaces,
  remoteCore,
  type Approval,
  type ChangeSet,
  type CommandSettingsInfo,
  type MachineInfo,
  type PrefsInfo,
  type RemoteCore,
  type TaskInfo,
  type Workspace,
} from "./core";
import type { LaneSnapshot } from "./lane";

// One poll of the Core, assembled in one place.
//
// This lives outside the window component on purpose. When the Sources
// screen came down the fetch behind it went with it, and the
// snapshot kept type-checking with `sources: []` — so the Lane screen drew
// every platform as disconnected for a whole release. A missing field is
// caught by the compiler here, and the test next door pins that what the
// Core reported is what the Lane is handed.

export interface CorePollers {
  status: typeof fetchStatus;
  workspaces: typeof fetchWorkspaces;
  approvals: typeof fetchApprovals;
  tasks: typeof fetchTasks;
  commandSettings: typeof fetchCommandSettings;
  prefs: typeof fetchPrefs;
  changeSets: typeof fetchChangeSets;
  sources: typeof fetchSources;
  machines: typeof fetchMachines;
  /** The control API of one remote machine. */
  remote: (id: string) => RemoteCore;
}

const CORE: CorePollers = {
  status: fetchStatus,
  workspaces: fetchWorkspaces,
  approvals: fetchApprovals,
  tasks: fetchTasks,
  commandSettings: fetchCommandSettings,
  prefs: fetchPrefs,
  changeSets: fetchChangeSets,
  sources: fetchSources,
  machines: fetchMachines,
  remote: remoteCore,
};

/** One remote machine as the window sees it this round: the Core's link
 *  state plus the machine's own folders. A machine whose link is up but
 *  which did not answer this round keeps its state and loses its lists —
 *  a stale list would offer folders nothing can reach. */
export interface MachineView {
  info: MachineInfo;
  workspaces: Workspace[];
  currentWorkspaceID: string;
  reachable: boolean;
}

export interface CorePoll {
  snapshot: LaneSnapshot;
  workspaces: Workspace[];
  currentWorkspaceID: string;
  tasks: TaskInfo[];
  commands: CommandSettingsInfo;
  prefs: PrefsInfo;
  /** Every configured remote machine, in the Core's order. */
  machines: MachineView[];
}

/** pollCore reads everything the window shows in one pass. It throws if the
 * Core cannot be reached; the caller decides what an unreachable Core looks
 * like on screen. */
export async function pollCore(deps: CorePollers = CORE): Promise<CorePoll> {
  // Status first: it is the cheapest call and the one that tells us the
  // control API is answering at all.
  const status = await deps.status();
  const [
    wsList,
    localApprovals,
    localTasks,
    commands,
    prefs,
    sources,
    machineList,
  ] = await Promise.all([
    deps.workspaces(),
    deps.approvals(),
    deps.tasks(),
    deps.commandSettings(),
    deps.prefs(),
    deps.sources(),
    deps.machines(),
  ]);
  const remotes = await Promise.all(
    machineList.map((m) => pollMachine(deps, m)),
  );
  const current =
    wsList.workspaces.find((w) => w.id === wsList.currentWorkspaceID) ??
    wsList.workspaces[0] ??
    null;
  // Change sets belong to a workspace, so they can only be read once one is
  // known — that is why this call is not in the batch above.
  const localChangeSets = current ? await deps.changeSets(current.id) : [];

  // What happened on other machines sits in the same lists as what happened
  // here, each record stamped with where it came from. The gate is one gate:
  // a request from any machine must reach the lane, whichever machine the
  // rail is standing on.
  const approvals = [...localApprovals, ...remotes.flatMap((r) => r.approvals)];
  const tasks = [...localTasks, ...remotes.flatMap((r) => r.tasks)].sort(
    (a, b) => Date.parse(b.started_at) - Date.parse(a.started_at),
  );
  const changeSets = [
    ...localChangeSets,
    ...remotes.flatMap((r) => r.changeSets),
  ];

  return {
    snapshot: {
      online: true,
      status,
      workspace: current,
      approvals,
      changeSets,
      sources,
    },
    workspaces: wsList.workspaces,
    currentWorkspaceID: wsList.currentWorkspaceID,
    tasks,
    commands,
    prefs,
    machines: remotes.map((r) => r.view),
  };
}

interface RemotePoll {
  view: MachineView;
  approvals: Approval[];
  tasks: TaskInfo[];
  changeSets: ChangeSet[];
}

/** pollMachine reads one remote machine's lists through the Core's proxy. A
 *  machine that is not online is not asked; one that is and fails to answer
 *  is reported as unreachable this round and never fails the whole poll —
 *  the local Core answered, and the window must keep showing that. */
async function pollMachine(
  deps: CorePollers,
  info: MachineInfo,
): Promise<RemotePoll> {
  const empty: RemotePoll = {
    view: { info, workspaces: [], currentWorkspaceID: "", reachable: false },
    approvals: [],
    tasks: [],
    changeSets: [],
  };
  if (info.state !== "online") return empty;
  try {
    const core = deps.remote(info.id);
    const [wsList, approvals, tasks] = await Promise.all([
      core.workspaces(),
      core.approvals(),
      core.tasks(),
    ]);
    const current =
      wsList.workspaces.find((w) => w.id === wsList.currentWorkspaceID) ??
      wsList.workspaces[0] ??
      null;
    const changeSets = current ? await core.changeSets(current.id) : [];
    const tag = { machine_id: info.id, machine: info.name };
    return {
      view: {
        info,
        workspaces: wsList.workspaces,
        currentWorkspaceID: current?.id ?? "",
        reachable: true,
      },
      approvals: approvals.map((a) => ({ ...a, ...tag })),
      tasks: tasks.map((t) => ({ ...t, ...tag })),
      changeSets: changeSets.map((c) => ({ ...c, ...tag })),
    };
  } catch {
    return empty;
  }
}
