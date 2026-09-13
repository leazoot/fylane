import type { Key } from "./i18n";
import {
  fetchCommandSettings,
  fetchPrefs,
  fetchStatus,
  type CommandGrant,
  type DelegationGrant,
  type CoreStatusInfo,
  type CommandRung,
  type ConnectInfo,
  type LanguageServer,
  type PrefsInfo,
  type ProxyProvider,
  type TunnelProvider,
  type WriteMode,
} from "./core";

/** Whether "use this way in" can do anything yet.
 *
 *  Two rules that pull in opposite directions. A provider whose program is
 *  missing cannot start, full stop. But a provider whose sign-in state cannot
 *  be read (`checkable` false — Tailscale keeps it in a daemon) gets the
 *  benefit of the doubt: refusing to start a tunnel that would have worked is
 *  worse than letting it run and report its own error. */
export function canUse(p: TunnelProvider, tokenDraft = "", hostnameDraft = ""): boolean {
  if (!p.installed) {
    return false;
  }
  // A named tunnel has nowhere to answer without one. The hostname is not
  // stored anywhere the Core can hand back, so the draft is the only source —
  // and "use this" used to be offered with the field still empty, which could
  // only fail after the Core had already been asked to restart.
  if (p.needs_hostname && hostnameDraft.trim() === "") {
    return false;
  }
  if (p.setup === "none" || p.authorized || !p.checkable) {
    return true;
  }
  // A token typed just now counts, even though the Core has not stored it
  // yet: it travels with the same request that starts the tunnel.
  return tokenDraft.trim() !== "";
}

/** Whether the hostname field is still waiting on the Core rather than on the
 *  user.
 *
 *  The suggestion is read out of the sign-in's own certificate and then looked
 *  up over the network, so it arrives some polls after the sign-in does. Only
 *  a signed-in provider is expecting one: without that, an empty field means
 *  "type one", not "wait". */
export function awaitingHostname(
  p: TunnelProvider,
  suggested: string,
  hostnameDraft: string,
): boolean {
  return p.needs_hostname && p.authorized && suggested === "" && hostnameDraft.trim() === "";
}

/** Whether the connection panel may say a way in is in effect.
 *
 *  A mode is a setting; a connection is a fact. Relay is the default mode even
 *  on a machine that has never paired with one, so reading "in effect" off the
 *  mode put a green dot beside a lane nothing could reach. Relay counts only
 *  when there is an address to reach this machine on; a tunnel counts once it
 *  is running or coming up, because it reports its own address a moment
 *  later. */
export function connectionLive(
  info: Pick<ConnectInfo, "mode" | "state" | "connector_url">,
): boolean {
  if (info.state === "running" || info.state === "starting") {
    return true;
  }
  return info.mode === "relay" && (info.connector_url ?? "") !== "";
}

/** The timeout values the Core accepts, in the order the stepper walks them.
 *
 *  Kept in step with `app.TaskTimeouts` by `prefs_wire_test.go`, which fails
 *  if only one side moves — the desktop offering a value the Core refuses is a
 *  save that fails with nothing on screen to explain it. 50 is the Core's own
 *  fallback for a machine that has never stored a preference. */
export const TIMEOUTS = [30, 50, 60, 300];

/** Where a step lands. The stored value is not always one of ours — a config
 *  written by hand, or by an older build, can say 50 — so the stepper walks to
 *  the nearest value on the far side rather than to the nearest value overall.
 *  From 50, "+" has to mean 60; snapping to the closest entry first would send
 *  it to 300, which is not what pressing "+" once looks like it does.
 *
 *  Returns null at the end of the run, which is what disables the button. */
export function stepTimeout(seconds: number, direction: 1 | -1): number | null {
  const reachable = TIMEOUTS.filter((v) => (direction > 0 ? v > seconds : v < seconds));
  if (reachable.length === 0) {
    return null;
  }
  return direction > 0 ? Math.min(...reachable) : Math.max(...reachable);
}

// What opening the settings page reads, and what to say when a part of it
// could not be read. Kept out of the screen so the failure wording can be
// asserted without rendering anything.

export interface SettingsReaders {
  prefs: typeof fetchPrefs;
  commands: typeof fetchCommandSettings;
  /** The write mode lives on the status response rather than with the rung,
   *  because the Core keeps the two approval axes apart and the
   *  status answer is the only place it publishes the one in force. */
  status: typeof fetchStatus;
}

export interface SettingsRead {
  /** Null when the Core did not answer: no value here is the stored one, so
   *  the rows it feeds must not be offered for editing. */
  prefs: PrefsInfo | null;
  rung: CommandRung | null;
  /** The file-write approval policy in force. Null when the Core did not
   *  answer — the same rule as the rung: an unread value must not be shown
   *  as a choice, because the row would then be claiming a policy nobody
   *  confirmed is running. */
  mode: WriteMode | null;
  /** Locally configured MCP gateway providers, empty when there are none or
   *  when the Core did not answer. Read-only: nothing on this page can add,
   *  change or remove one. */
  proxies: ProxyProvider[];
  /** Installed language servers, read the same way and for the same reason:
   *  a program this machine may start is named in the config file and
   *  nowhere else. */
  servers: LanguageServer[];
  /** Workspaces holding a standing command authorization. Empty when there
   *  are none or when the Core did not answer — and an empty list is a real
   *  answer here, so the page says "nothing is authorized" rather than
   *  showing no section at all. */
  grants: CommandGrant[];
  /** Agents a yes still covers, read the same way as grants. */
  delegations: DelegationGrant[];
  /** How long one yes to an agent lasts, in hours; 0 from a Core without it. */
  delegationHours: number;
  /** Messages to raise, in the order the page lists the sections. */
  errors: Key[];
}

/** What the settings head should say about a newer release, or nothing.
 *
 *  Three separate absences collapse here, and they are not the same thing:
 *  the check is disabled (it is by default, so both fields are missing), the
 *  check ran and found nothing newer, and the check reports a newer version
 *  without naming it. All three come out as no notice — a notice that cannot
 *  name a version has nothing to say — but none of them is grounds to claim
 *  the machine is up to date, because in the first case nobody looked. */
export function updateNotice(
  status: CoreStatusInfo | null,
): { version: string; page?: string } | undefined {
  if (!status?.update_available || !status.latest_version) {
    return undefined;
  }
  return { version: status.latest_version, page: status.download_page };
}

/** readSettings never rejects: one half failing must still show the other.
 *  The messages are read-failures, not change-failures — opening a page
 *  changes nothing, and naming an edit the user never made sends them
 *  looking for a switch they did not touch. */
export async function readSettings(deps: SettingsReaders): Promise<SettingsRead> {
  const [prefs, commands, status] = await Promise.allSettled([
    deps.prefs(),
    deps.commands(),
    deps.status(),
  ]);
  const errors: Key[] = [];
  if (prefs.status === "rejected") {
    errors.push("shell.errPrefsRead");
  }
  if (commands.status === "rejected" || status.status === "rejected") {
    errors.push("shell.errGateRead");
  }
  return {
    prefs: prefs.status === "fulfilled" ? prefs.value : null,
    rung: commands.status === "fulfilled" ? commands.value.rung : null,
    mode: status.status === "fulfilled" ? status.value.approval_mode : null,
    proxies: commands.status === "fulfilled" ? (commands.value.providers ?? []) : [],
    servers: commands.status === "fulfilled" ? (commands.value.language_servers ?? []) : [],
    grants: commands.status === "fulfilled" ? commands.value.grants : [],
    delegations: commands.status === "fulfilled" ? (commands.value.delegations ?? []) : [],
    delegationHours: commands.status === "fulfilled" ? (commands.value.delegation_hours ?? 0) : 0,
    errors,
  };
}

/** A pairing code's remaining life, as the connection panel shows it.
 *
 *  Minutes and seconds above a minute, whole seconds below it: a code lasts
 *  ten minutes, and a bare countdown of 597 is a number nobody reads. Past
 *  zero it returns null — the code is gone, and a clock stuck at 0:00 next to
 *  it would say it is merely about to be. */
export function codeLeft(seconds: number): string | null {
  const left = Math.floor(seconds);
  if (left <= 0) {
    return null;
  }
  if (left < 60) {
    return String(left);
  }
  return Math.floor(left / 60) + ":" + String(left % 60).padStart(2, "0");
}
