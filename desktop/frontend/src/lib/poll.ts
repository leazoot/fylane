import {
  fetchApprovals,
  fetchChangeSets,
  fetchCommandSettings,
  fetchPrefs,
  fetchSources,
  fetchStatus,
  fetchTasks,
  fetchWorkspaces,
  type CommandSettingsInfo,
  type PrefsInfo,
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
};

export interface CorePoll {
  snapshot: LaneSnapshot;
  workspaces: Workspace[];
  currentWorkspaceID: string;
  tasks: TaskInfo[];
  commands: CommandSettingsInfo;
  prefs: PrefsInfo;
}

/** pollCore reads everything the window shows in one pass. It throws if the
 * Core cannot be reached; the caller decides what an unreachable Core looks
 * like on screen. */
export async function pollCore(deps: CorePollers = CORE): Promise<CorePoll> {
  // Status first: it is the cheapest call and the one that tells us the
  // control API is answering at all.
  const status = await deps.status();
  const [wsList, approvals, tasks, commands, prefs, sources] = await Promise.all([
    deps.workspaces(),
    deps.approvals(),
    deps.tasks(),
    deps.commandSettings(),
    deps.prefs(),
    deps.sources(),
  ]);
  const current =
    wsList.workspaces.find((w) => w.id === wsList.currentWorkspaceID) ?? wsList.workspaces[0] ?? null;
  // Change sets belong to a workspace, so they can only be read once one is
  // known — that is why this call is not in the batch above.
  const changeSets = current ? await deps.changeSets(current.id) : [];

  return {
    snapshot: { online: true, status, workspace: current, approvals, changeSets, sources },
    workspaces: wsList.workspaces,
    currentWorkspaceID: wsList.currentWorkspaceID,
    tasks,
    commands,
    prefs,
  };
}
