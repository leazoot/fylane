import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import {
  applyTunnel,
  cancelTunnelDownload,
  cancelTunnelSetup,
  copyText,
  fetchCommandSettings,
  fetchConnect,
  fetchPrefs,
  fetchStatus,
  fetchDock,
  setDockHidden,
  mintPairingCode,
  openURL,
  raiseWindow,
  savePrefs,
  setCommandRung,
  setWriteMode,
  signOutTunnel,
  startCore,
  startTunnelDownload,
  startTunnelSetup,
  type ClearedBackups,
  revokeCommandGrant,
  remoteCore,
  type CommandGrant,
  type CommandRung,
  type MachineInfo,
  type LanguageServer,
  setWorkspaceNetwork,
  type ProxyProvider,
  type ReadBoundaryInfo,
  type ConnectInfo,
  type PairingCodeInfo,
  type PrefsInfo,
  type TunnelProvider,
  type TunnelDownloadState,
  type TunnelSetupState,
  type Workspace,
  type WriteMode,
  type DockInfo,
} from "../lib/core";
import { LANGS, useT, type Key, type Lang, type Translator } from "../lib/i18n";
import { agoShort } from "../lib/records";
import {
  awaitingHostname,
  canUse,
  codeLeft,
  connectionLive,
  readSettings,
  stepTimeout,
} from "../lib/settings";
import type { Theme } from "../lib/theme";
import type { MachineView } from "../lib/poll";
import { detectOS } from "../lib/platform";
import { Jelly } from "../components/Jelly";

// Settings (Fylane-V3, boards 08–12). One page, four sections, and each is
// organised the way its content wants: three cells side by side for the
// basics, a two-column policy panel for execution, one connection in effect
// with the rest as rows beneath it, and two promises above the controls that
// act on them.
//
// The settings map onto real capability one for one. The rung is the Core's
// own command rung, the timeout binds what a caller may ask for, and turning
// the stop button off really removes it from the task rows.

/** The MCP providers this Core forwards to, listed and not editable.
 *
 *  There is no board for a provider page, and inventing one would be changing
 *  the design rather than following it — so this reuses the rule rows it sits
 *  among. What it must not do is stay invisible: a provider marked
 *  `trust: workspace` runs tools Fylane cannot inspect without asking, and an
 *  authorization is only safe while it is visible. A start-up log line
 *  is not visible.
 *
 *  Nothing renders when nothing is configured. An empty list on a page with no
 *  design for one is a row that only ever says "no". */
/** A rule's label. Its explanation is a tooltip on hover or focus, the
 * same one the fixed pill uses, so the column reads as a list of settings
 * and the manual is one pointer away. */
function Label({ text, help, id }: { text: ReactNode; help?: string; id: string }) {
  const [open, setOpen] = useState(false);
  if (!help) {
    return <div className="fy-slabel">{text}</div>;
  }
  return (
    <div className="fy-slabel">
      <span
        className="fy-help"
        tabIndex={0}
        aria-describedby={open ? id : undefined}
        onMouseEnter={() => setOpen(true)}
        onMouseLeave={() => setOpen(false)}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
      >
        {text}
        {open && (
          <span className="fy-tip fy-tip-left" id={id} role="tooltip">
            {help}
          </span>
        )}
      </span>
    </div>
  );
}

/** The heading over a group of rows: the column's eyebrow, so the rows
 *  under it read as its contents and not as its neighbours. */
function GroupHead({ text, help, id }: { text: ReactNode; help?: string; id: string }) {
  return (
    <div className="fy-rule-group">
      <Label text={text} help={help} id={id} />
    </div>
  );
}

/** Folders on one remote machine, with what its own Core says about them.
 *  The machine's folders come with every poll; its rung and grants are
 *  read once, the way this computer's are. */
export interface RemoteFolders {
  machine: MachineInfo;
  folders: Workspace[];
  rung: CommandRung | null;
  grants: CommandGrant[];
}

/** The name on the vendor's download page, not the binary's. */
function productName(binary: string): string {
  return { tailscale: "Tailscale", cloudflared: "cloudflared", ngrok: "ngrok" }[binary] ?? binary;
}

function ProxyRows({ proxies, tr }: { proxies: ProxyProvider[]; tr: Translator }) {
  const { t } = tr;
  if (proxies.length === 0) {
    return null;
  }
  const following = proxies.filter((p) => p.trust !== "ask");
  return (
    <>
      <GroupHead text={t("set.proxies")} help={t("set.proxiesNote")} id="fy-help-proxies" />
      {proxies.map((p) => (
        <div className="fy-rule-row" key={p.name}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div className="fy-slabel">{p.name}</div>
          </div>
          <span
            style={{
              fontSize: 11.5,
              whiteSpace: "nowrap",
              color: p.trust === "ask" ? "var(--fy-muted)" : "var(--fy-amber)",
            }}
          >
            {t(p.trust === "ask" ? "set.proxyAsks" : "set.proxyFollows")}
          </span>
        </div>
      ))}
      {following.length > 0 && (
        <div className="fy-warn">
          <div style={{ flex: 1 }}>
            <div className="fy-slabel">{following.map((p) => p.name).join(", ")}</div>
            <div className="fy-snote" style={{ color: "var(--fy-ink2)" }}>
              {t("set.proxyWarn")}
            </div>
          </div>
        </div>
      )}
    </>
  );
}

const RUNGS: { key: CommandRung; label: Key; note: Key }[] = [
  { key: "strict", label: "set.rungStrict", note: "set.rungStrictNote" },
  {
    key: "workspace",
    label: "set.rungWorkspace",
    note: "set.rungWorkspaceNote",
  },
  { key: "open", label: "set.rungOpen", note: "set.rungOpenNote" },
];

// The second approval axis. Two entries, not three: `balanced` is a
// single carve-out rather than a ladder, and its note names the exclusions
// rather than the rule — what someone loosening this needs to know is what
// still stops, not what goes through.
const MODES: { key: WriteMode; label: Key; note: Key }[] = [
  { key: "safe", label: "set.writeSafe", note: "set.writeSafeNote" },
  { key: "balanced", label: "set.writeBalanced", note: "set.writeBalancedNote" },
];

/** One approval axis: a heading and its mutually exclusive choices.
 *
 *  Generic over the key so the two axes keep their own types — a rung and a
 *  write mode are not interchangeable, and nothing here should let one be
 *  passed where the other belongs.
 *
 *  A null `current` disables the rows rather than guessing. Both axes have a
 *  default the Core falls back to, so a guess would land on it and look
 *  right while the machine ran the other one. */
function PolicyGroup<K extends string>({
  label,
  options,
  current,
  loading,
  tr,
  onPick,
}: {
  label: string;
  options: { key: K; label: Key; note: Key }[];
  current: K | null;
  loading: boolean;
  tr: Translator;
  onPick: (key: K) => void;
}) {
  const { t } = tr;
  return (
    <div role="radiogroup" aria-label={label}>
      <div className="fy-eyebrow fy-eyebrow-tight" style={{ marginBottom: 11 }}>
        {label}
      </div>
      {loading ? (
        <div style={{ padding: "18px 13px" }}>
          <Jelly size={20} busyLabel={label} />
        </div>
      ) : (
        options.map((o) => (
          <button
            key={o.key}
            type="button"
            role="radio"
            className="fy-policy"
            aria-checked={current === o.key}
            disabled={current === null}
            onClick={() => onPick(o.key)}
          >
            <span style={{ flex: 1, minWidth: 0 }}>
              <span className="fy-policy-label">{t(o.label)}</span>
              <span className="fy-policy-note">{t(o.note)}</span>
            </span>
            <svg className="fy-policy-check" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
              <path
                d="M2.6 7.5 5.4 10.2 11.4 3.9"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.7"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        ))
      )}
    </div>
  );
}

const PROVIDER_NAMES: Record<string, Key> = {
  "cloudflare-quick": "conn.cfQuick",
  "cloudflare-named": "conn.cfNamed",
  "tailscale-funnel": "conn.tailscale",
  ngrok: "conn.ngrok",
};

const PROVIDER_NOTES: Record<string, Key> = {
  "cloudflare-quick": "conn.cfQuickNote",
  "cloudflare-named": "conn.cfNamedNote",
  "tailscale-funnel": "conn.tailscaleNote",
  ngrok: "conn.ngrokNote",
};

// The reads this page makes. They are a prop with a real default for the same
// reason pollCore's are: the design harness has no Core to answer them, and a
// section that cannot be drawn cannot be checked against its board.
export interface SettingsDeps {
  prefs: typeof fetchPrefs;
  save: typeof savePrefs;
  connect: typeof fetchConnect;
  commands: typeof fetchCommandSettings;
  setRung: typeof setCommandRung;
  revokeGrant: typeof revokeCommandGrant;
  startSetup: typeof startTunnelSetup;
  cancelSetup: typeof cancelTunnelSetup;
  startDownload: typeof startTunnelDownload;
  cancelDownload: typeof cancelTunnelDownload;
  signOut: typeof signOutTunnel;
  mintCode: typeof mintPairingCode;
  setNetwork: typeof setWorkspaceNetwork;
  status: typeof fetchStatus;
  setMode: typeof setWriteMode;
  dock: typeof fetchDock;
  setDock: typeof setDockHidden;
  /** The control API of one remote machine, for its folders' rows. */
  remote: typeof remoteCore;
}

const CORE: SettingsDeps = {
  prefs: fetchPrefs,
  save: savePrefs,
  connect: fetchConnect,
  commands: fetchCommandSettings,
  setRung: setCommandRung,
  revokeGrant: revokeCommandGrant,
  startSetup: startTunnelSetup,
  cancelSetup: cancelTunnelSetup,
  startDownload: startTunnelDownload,
  cancelDownload: cancelTunnelDownload,
  signOut: signOutTunnel,
  mintCode: mintPairingCode,
  setNetwork: setWorkspaceNetwork,
  status: fetchStatus,
  setMode: setWriteMode,
  dock: fetchDock,
  setDock: setDockHidden,
  remote: remoteCore,
};

export interface SettingsProps {
  /** Remote machines as the window sees them; their granted folders are
   *  listed beside this computer's. */
  machines?: MachineView[];
  lang: Lang;
  onLang(lang: Lang): void;
  theme: Theme;
  onTheme(theme: Theme): void;
  workspaces: Workspace[];
  recordCount: number;
  onClearRecords: () => void;
  /** How many writes are still inside their undo window. */
  undoCount: number;
  onClearBackups: () => Promise<ClearedBackups | null>;
  onError: (message: string) => void;
  /** The Core's version, for the corner of the page head. */
  version: string;
  /** A newer release the daily check has seen, and the page to get it from.
   *  Both absent when there is nothing to say — which covers two states, a
   *  check that is switched off and a check that found nothing, on purpose:
   *  the promise is "you will be told when there is a newer
   *  version", and silence keeps it either way. Claiming "up to date" would
   *  not, because with the check off nobody looked. */
  update?: { version: string; page?: string };
  /** Whether the Core is reachable. The page reads its settings when it
   *  mounts and again each time this turns true, so a page opened while the
   *  Core was starting — or after it was killed — recovers on its own. While
   *  false, a failed read raises no message: the shell is already saying the
   *  Core is down, and a second sentence about "approval settings" would be
   *  the same fact wearing a different name. */
  online?: boolean;
  deps?: SettingsDeps;
}

export function SettingsScreen({
  lang,
  onLang,
  theme,
  onTheme,
  workspaces,
  machines = [],
  recordCount,
  onClearRecords,
  undoCount,
  onClearBackups,
  onError,
  version,
  update,
  online = true,
  deps = CORE,
}: SettingsProps) {
  const tr = useT();
  const { t, tn } = tr;
  const [prefs, setPrefs] = useState<PrefsInfo | null>(null);
  const [rung, setRung] = useState<CommandRung | null>(null);
  const [mode, setMode] = useState<WriteMode | null>(null);
  const [dock, setDock] = useState<DockInfo | null>(null);
  const [proxies, setProxies] = useState<ProxyProvider[]>([]);
  const [servers, setServers] = useState<LanguageServer[]>([]);
  const [grants, setGrants] = useState<CommandGrant[]>([]);
  // The folder list arrives as a prop and is re-read from the Core's own
  // answer after a change: the word beside each switch is computed there, so
  // echoing the click locally would put a guess on the screen.
  const [folders, setFolders] = useState<Workspace[]>(workspaces);
  useEffect(() => setFolders(workspaces), [workspaces]);
  // What each online machine's own Core says about its folders. Read when
  // the set of online machines changes, not on every poll; the folder
  // lists themselves ride along with the poll.
  const online_ = machines.filter((m) => m.info.state === "online");
  const onlineKey = online_.map((m) => m.info.id).join(",");
  const [remoteRead, setRemoteRead] = useState<
    Record<string, { rung: CommandRung | null; grants: CommandGrant[]; folders?: Workspace[] }>
  >({});
  useEffect(() => {
    let alive = true;
    for (const m of online_) {
      const id = m.info.id;
      void deps
        .remote(id)
        .commandSettings()
        .then((res) => {
          if (alive) setRemoteRead((r) => ({ ...r, [id]: { rung: res.rung, grants: res.grants } }));
        })
        .catch(() => {
          if (alive) setRemoteRead((r) => ({ ...r, [id]: { rung: null, grants: [] } }));
        });
    }
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onlineKey, online]);
  const remote: RemoteFolders[] = online_
    .filter((m) => remoteRead[m.info.id])
    .map((m) => ({
      machine: m.info,
      folders: remoteRead[m.info.id].folders ?? m.workspaces,
      rung: remoteRead[m.info.id].rung,
      grants: remoteRead[m.info.id].grants,
    }));
  const withdrawRemote = async (machineID: string, workspaceID: string) => {
    try {
      const res = await deps.remote(machineID).revokeGrant(workspaceID);
      setRemoteRead((r) => ({ ...r, [machineID]: { ...r[machineID], rung: res.rung, grants: res.grants } }));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errGate"));
    }
  };
  const setRemoteNetwork = async (machineID: string, id: string, allow: boolean) => {
    try {
      const res = await deps.remote(machineID).setNetwork(id, allow);
      setRemoteRead((r) => ({ ...r, [machineID]: { ...r[machineID], folders: res.workspaces } }));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errPrefs"));
    }
  };
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let alive = true;
    // A shell setting, read beside the Core's rather than after them. Failing
    // to read it is a quiet absence — the switch does not appear — not a
    // reason to hold the page.
    void deps
      .dock()
      .then((info) => {
        if (alive) {
          setDock(info);
        }
      })
      .catch(() => {});
    void readSettings(deps).then((read) => {
      if (!alive) {
        return;
      }
      setPrefs(read.prefs);
      setRung(read.rung);
      setMode(read.mode);
      setProxies(read.proxies);
      setServers(read.servers);
      setGrants(read.grants);
      setLoading(false);
      if (online) {
        read.errors.forEach((key) => onError(t(key)));
      }
    });
    return () => {
      alive = false;
    };
    // Re-read when the Core comes back, not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [online]);

  const setNetwork = async (id: string, allow: boolean) => {
    try {
      setFolders((await deps.setNetwork(id, allow)).workspaces);
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errPrefs"));
    }
  };

  const patch = async (next: Parameters<typeof savePrefs>[0]) => {
    try {
      setPrefs(await deps.save(next));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errPrefs"));
      // The switch must not stay where the click put it when the Core
      // refused; re-read what is actually stored.
      deps
        .prefs()
        .then(setPrefs)
        .catch(() => {});
    }
  };

  // The open rung needs an explicit acknowledgement and the Core refuses it
  // without one. The design shows the three rungs as a plain choice
  // list; a click that silently failed would be worse than the extra
  // question, so the acknowledgement is asked in place.
  const [asking, setAsking] = useState(false);

  const chooseRung = async (next: CommandRung, confirmed = false) => {
    if (next === "open" && !confirmed) {
      setAsking(true);
      return;
    }
    setAsking(false);
    try {
      const res = await deps.setRung(next, next === "open");
      setRung(res.rung);
      setGrants(res.grants);
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errGate"));
    }
  };

  const chooseDock = async (hidden: boolean) => {
    try {
      setDock(await deps.setDock(hidden));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errPrefs"));
    }
  };

  // The Core answers with the mode now in force rather than echoing the
  // request, so a refusal cannot leave a policy on screen that nobody set.
  const chooseMode = async (next: WriteMode) => {
    try {
      setMode((await deps.setMode(next)).approval_mode);
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errGate"));
    }
  };

  // The Core answers a withdrawal with the settings this page redraws from,
  // so the list cannot disagree with what it now holds.
  const withdraw = async (workspaceID: string) => {
    try {
      const res = await deps.revokeGrant(workspaceID);
      setRung(res.rung);
      setGrants(res.grants);
    } catch (e) {
      onError(e instanceof Error ? e.message : t("shell.errGate"));
    }
  };

  const themes: { key: Theme; label: string }[] = [
    { key: "auto", label: t("settings.themeAuto") },
    { key: "light", label: t("settings.themeLight") },
    { key: "dark", label: t("settings.themeDark") },
  ];
  const currentRung = RUNGS.find((r) => r.key === rung);
  // Both axes, because naming only one would let the panel head answer
  // "what is in force?" with half the answer while looking complete.
  const currentMode = MODES.find((m) => m.key === mode);
  // Fixed for the life of the window; the OS does not change underneath it.
  const [os] = useState(detectOS);

  return (
    <div className="fy-setpage">
      <div className="fy-pagehead">
        <div>
          <h1 className="fy-display" style={{ fontSize: 28, lineHeight: 1.1 }}>
            {t("set.title")}
          </h1>
          <div
            style={{
              marginTop: 7,
              fontSize: 12.5,
              color: "var(--fy-faint)",
              maxWidth: 560,
            }}
          >
            {t("set.sub")}
          </div>
        </div>
        <div
          className="fy-revealer"
          style={{
            textAlign: "right",
            fontSize: 11.5,
            color: "var(--fy-faint)",
            lineHeight: 1.65,
          }}
        >
          <div>Fylane {version || "—"}</div>
          {/* A notice, not a control: it downloads and installs nothing.
              It sits on the line that already names the build rather than in
              a banner, because a version is what it is about. The link is
              hidden while the release page is empty — the constant on the
              Core says consumers do exactly that — leaving the version
              itself, which is still worth knowing. */}
          {update && (
            <div style={{ color: "var(--fy-amber)" }}>
              {t("set.updateHere", { version: update.version })}
              {update.page && (
                <>
                  {" · "}
                  <button
                    type="button"
                    className="fy-textbtn"
                    style={{ fontSize: 11.5, color: "var(--fy-amber)" }}
                    onClick={() => void openURL(update.page ?? "").catch(() => {})}
                  >
                    {t("set.updateGet")}
                  </button>
                </>
              )}
            </div>
          )}
          {/* The design reveals the OS release and Node version here. This
              window collects neither, so it says the two things it does know
              rather than inventing a number. */}
          <div className="fy-reveal" style={{ "--fy-reveal-h": "20px" } as CSSProperties}>
            {t("set.buildOn", { os: os === "windows" ? "Windows" : "macOS" })}
          </div>
        </div>
      </div>

      {/* ── basics ───────────────────────────────────────────────────── */}
      <div style={{ marginTop: 32 }}>
        <div className="fy-sechead">
          <h2>{t("set.basics")}</h2>
          <p>{t("set.basicsNote")}</p>
        </div>
        <div className="fy-cells">
          <div className="fy-cell">
            <div className="fy-cell-title">{t("settings.language")}</div>
            <Opts
              label={t("settings.language")}
              options={LANGS.map((l) => ({ key: l.key, label: l.label }))}
              value={lang}
              onPick={onLang}
            />
          </div>
          <div className="fy-cell">
            <div className="fy-cell-title">{t("settings.appearance")}</div>
            <Opts
              label={t("settings.appearance")}
              options={themes.map((o) => ({ key: o.key, label: o.label }))}
              value={theme}
              onPick={onTheme}
            />
          </div>
          <div className="fy-cell">
            <div className="fy-cell-title">{t("set.presence")}</div>
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 11,
                marginTop: 13,
              }}
            >
              {loading ? (
                <Jelly size={20} busyLabel={t("set.autostart")} />
              ) : (
                <Switch
                  label={t("set.autostart")}
                  on={prefs?.autostart.enabled ?? false}
                  disabled={!prefs?.autostart.supported}
                  onToggle={() => void patch({ autostart: !prefs?.autostart.enabled })}
                />
              )}
              <span style={{ fontSize: 12, color: "var(--fy-muted)" }}>
                {prefs && !prefs.autostart.supported
                  ? prefs.autostart.detail || t("set.autostartUnsupported")
                  : t("set.autostartNote")}
              </span>
            </div>
            {/* Hidden from the Dock, the app is reached from the menu bar's
                own item, which every platform has — so this can never strand
                the window. Where there is no Dock there is no row: unlike a
                boundary, a missing convenience is not news. */}
            {dock?.supported && (
              <div className="fy-cell-body" style={{ display: "flex", alignItems: "center", gap: 10, marginTop: 10 }}>
                <Switch label={t("set.dock")} on={dock.hidden} onToggle={() => void chooseDock(!dock.hidden)} />
                <span className="fy-snote">{t("set.dock")}</span>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ── execution & approval ─────────────────────────────────────── */}
      <div className="fy-panel" style={{ marginTop: 38 }}>
        <div className="fy-panel-head">
          <h2
            style={{
              margin: 0,
              font: "400 20px/1.2 var(--fy-serif)",
              color: "var(--fy-ink)",
            }}
          >
            {t("set.execution")}
          </h2>
          {/* Only the anchor word is set in caps. The rung's own name is a
              phrase, and upper-casing a phrase shouts it. */}
          <div
            style={{
              whiteSpace: "nowrap",
              fontSize: 11.5,
              color: "var(--fy-faint)",
            }}
          >
            <span className="fy-eyebrow fy-eyebrow-tight">{t("setV3.current")}</span>
            {" · "}
            {currentRung ? t(currentRung.label) : "—"}
            {" · "}
            {currentMode ? t(currentMode.label) : "—"}
          </div>
        </div>

        <div className="fy-panel-cols">
          <div className="fy-panel-col">
            <PolicyGroup
              label={t("set.ordinary")}
              options={RUNGS}
              current={rung}
              loading={loading}
              tr={tr}
              onPick={(key) => void chooseRung(key)}
            />
            {/* Both notices sit under the list they are about. The card's
                right column is long, so a notice at the card's foot lands a
                screen below the choice that raised it and is never seen. */}
            {asking && (
              <div className="fy-warn">
                <div style={{ flex: 1 }}>
                  <div className="fy-slabel">{t("set.openConfirmTitle")}</div>
                  <div className="fy-snote" style={{ color: "var(--fy-ink2)" }}>
                    {t("set.openConfirmBody")}
                  </div>
                </div>
                <div
                  style={{
                    flex: "none",
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                  }}
                >
                  <button type="button" className="fy-quiet" onClick={() => setAsking(false)}>
                    {t("set.cancel")}
                  </button>
                  <button
                    type="button"
                    className="fy-smallbtn"
                    onClick={() => void chooseRung("open", true)}
                  >
                    {t("set.openConfirmYes")}
                  </button>
                </div>
              </div>
            )}

            {/* the open rung is never a quiet state. */}
            {!asking && rung === "open" && (
              <div className="fy-warn">
                <div style={{ flex: 1 }}>
                  <div className="fy-slabel">{t("set.openWarning")}</div>
                  <div className="fy-snote" style={{ color: "var(--fy-ink2)" }}>
                    {t("set.openWarningBody")}
                  </div>
                </div>
              </div>
            )}

            {/* The second approval axis, under its own heading rather than
                among the execution rules on the right. That column holds
                bounds on how a command runs; this is the other half of the
                answer to "when am I asked?", and the left column is where
                someone comes to read that. It is also how this axis went
                missing once already: it had no place of its own, so a
                redesign carried it off without anyone noticing. */}
            <div style={{ marginTop: 22 }}>
              <PolicyGroup
                label={t("set.writes")}
                options={MODES}
                current={mode}
                loading={loading}
                tr={tr}
                onPick={(key) => void chooseMode(key)}
              />
            </div>
          </div>

          <div className="fy-panel-col">
            <div className="fy-eyebrow fy-eyebrow-tight" style={{ marginBottom: 11 }}>
              {t("setV3.rules")}
            </div>
            <RiskyRow tr={tr} />
            <ReadBoundaryRow
              info={prefs?.read_boundary ?? null}
              tr={tr}
              onToggle={(on) => void patch({ read_boundary: on })}
            />
            <div className="fy-rule-row">
              <div style={{ flex: 1, minWidth: 0 }}>
                <Label text={t("set.allowStop")} help={t("set.allowStopNote")} id="fy-help-stop" />
              </div>
              <Switch
                label={t("set.allowStop")}
                on={prefs?.allow_stop_tasks ?? true}
                disabled={prefs === null}
                onToggle={() => void patch({ allow_stop_tasks: !prefs?.allow_stop_tasks })}
              />
            </div>
            <div className="fy-rule-row">
              <div style={{ flex: 1, minWidth: 0 }}>
                <Label text={t("set.timeout")} help={t("set.timeoutNote")} id="fy-help-timeout" />
              </div>
              <Stepper
                label={t("set.timeout")}
                seconds={prefs?.task_timeout_seconds ?? null}
                tr={tr}
                onPick={(secs) => void patch({ task_timeout_seconds: secs })}
              />
            </div>
            <ProxyRows proxies={proxies} tr={tr} />
            <ServerRows servers={servers} tr={tr} />
            <GrantRows
              grants={grants}
              folders={folders}
              rung={rung}
              remote={remote}
              tr={tr}
              onWithdraw={(id) => void withdraw(id)}
              onWithdrawRemote={(machineID, id) => void withdrawRemote(machineID, id)}
            />
            <NetworkRows
              workspaces={folders}
              remote={remote}
              tr={tr}
              onToggle={(id, allow) => void setNetwork(id, allow)}
              onToggleRemote={(machineID, id, allow) => void setRemoteNetwork(machineID, id, allow)}
            />
          </div>
        </div>

      </div>

      {/* ── connection ───────────────────────────────────────────────── */}
      <div style={{ marginTop: 38 }}>
        <div className="fy-sechead">
          <h2>{t("set.connection")}</h2>
          <p>{t("set.connectionNote")}</p>
        </div>
        <Connection tr={tr} deps={deps} onError={onError} />
      </div>

      {/* ── privacy ──────────────────────────────────────────────────── */}
      <div style={{ marginTop: 40 }}>
        <div className="fy-sechead">
          <h2>{t("set.privacy")}</h2>
          <p>{t("set.privacyNote")}</p>
        </div>
        <div className="fy-promises">
          <PromiseCell tag={t("set.p1tag")} title={t("set.p1")} body={t("set.p1d")} />
          <PromiseCell
            tag={t("setV3.p2tag")}
            title={t("set.p2")}
            body={`${t("set.p2d")} ${tn("privacy.folders", workspaces.length)}`}
          />
          <PromiseCell tag={t("set.p3tag")} title={t("set.p3")} body={t("set.p3d")} />
        </div>

        <div className="fy-controlrow">
          <div style={{ flex: 1, minWidth: 220 }}>
            <div className="fy-controlrow-title">{t("set.p4")}</div>
            <div className="fy-controlrow-note">
              {t("set.p4d", { count: tn("record.entries", recordCount) })}
            </div>
          </div>
          <button
            type="button"
            className="fy-smallbtn"
            style={{ flex: "none" }}
            disabled={recordCount === 0}
            onClick={onClearRecords}
          >
            {t("record.clear")}
          </button>
        </div>

        <UndoCopies tr={tr} count={undoCount} onClear={onClearBackups} />
      </div>
    </div>
  );
}

/** The undo copies, and the one way to be rid of them.
 *
 *  Asked twice on purpose. "Clear records" above drops finished rows and can
 *  be pressed freely; this drops the only thing that can put a file back, and
 *  the two must not look like the same button. */
function UndoCopies({
  tr,
  count,
  onClear,
}: {
  tr: Translator;
  count: number;
  onClear: () => Promise<ClearedBackups | null>;
}) {
  const { t, tn } = tr;
  const [asking, setAsking] = useState(false);
  // What the last clear removed. The row's own note goes back to "nothing is
  // within its undo window" the moment the copies are gone, which is true and
  // says nothing about what just happened; this line is the only place the
  // user learns the size of what they gave up.
  const [done, setDone] = useState<ClearedBackups | null>(null);

  return (
    <div className="fy-controlrow" style={asking ? { alignItems: "flex-start" } : undefined}>
      <div style={{ flex: 1, minWidth: 220 }}>
        <div className="fy-controlrow-title">{t("set.undo")}</div>
        <div className="fy-controlrow-note">
          {count === 0 ? t("set.undoNone") : tn("set.undoSome", count)}
        </div>
        {asking && (
          <div
            className="fy-snote"
            style={{ marginTop: 10, color: "var(--fy-amber)", maxWidth: 520 }}
          >
            {t("set.undoAsk")}
          </div>
        )}
        {!asking && done && (
          <div className="fy-snote" style={{ marginTop: 10, maxWidth: 520 }}>
            {done.cleared === 0 ? t("set.undoDoneNone") : tn("set.undoDone", done.cleared)}
            {done.backups_removed < done.cleared && (
              <> {t("set.undoDoneLeft", { n: done.cleared - done.backups_removed })}</>
            )}
          </div>
        )}
      </div>
      <div style={{ flex: "none", display: "flex", alignItems: "center", gap: 12 }}>
        {asking ? (
          <>
            <button type="button" className="fy-quiet" onClick={() => setAsking(false)}>
              {t("set.undoCancel")}
            </button>
            <button
              type="button"
              className="fy-danger"
              onClick={() => {
                setAsking(false);
                void onClear().then(setDone);
              }}
            >
              {t("set.undoConfirm")}
            </button>
          </>
        ) : (
          <button
            type="button"
            className="fy-smallbtn"
            disabled={count === 0}
            onClick={() => {
              setDone(null);
              setAsking(true);
            }}
          >
            {t("set.undoClear")}
          </button>
        )}
      </div>
    </div>
  );
}

/** High-risk commands. Not a control: the product does not offer "always
 *  allow" for these, and the row says so rather than showing a switch that
 *  refuses. */
/** Standing command authorizations, one row per authorized folder.
 *
 *  On the default rung the first approval in a folder authorizes the folder,
 *  not the one command. The Core has kept that list and been able to
 *  withdraw from it all along; until now nothing on this page read it, so an
 *  authorization given six weeks ago could be neither seen nor taken back.
 *
 *  Three things this block does deliberately.
 *
 *  It withdraws on one press. Clearing the undo copies asks twice because it
 *  destroys the only thing that can put a file back; withdrawing destroys
 *  nothing and can only narrow what this machine will do unattended. Asking
 *  twice would teach the user that becoming safer is expensive.
 *
 *  It says something different on each rung, because the same sentence is
 *  false on two of the three. `strict` consults no grant (`cmdgate.go`
 *  answers Approval before reaching one) and `open` reaches no grant either,
 *  having already answered Allowed. On those rungs a row is not what keeps
 *  commands unasked — the rung is — and the note says so rather than letting
 *  the list imply an authority it does not have.
 *
 *  It lists the rows that are not in effect. A grant taken at a laxer rung is
 *  already ignored, but a row the user cannot see is a row they cannot clear.
 */
function GrantRows({
  grants,
  folders,
  rung,
  remote,
  tr,
  onWithdraw,
  onWithdrawRemote,
}: {
  grants: CommandGrant[];
  folders: Workspace[];
  rung: CommandRung | null;
  remote: RemoteFolders[];
  tr: Translator;
  onWithdraw: (id: string) => void;
  onWithdrawRemote: (machineID: string, id: string) => void;
}) {
  const { t } = tr;
  if (rung === null) {
    return null;
  }
  // Read once per render rather than threaded in as a prop: nothing here
  // ticks, and an authorization's age is read in days.
  const now = new Date();
  // A grant can outlive the folder's place in the list. Falling back to the
  // id keeps the row visible and therefore clearable; dropping it would leave
  // an authorization on the machine with nothing on screen to remove it.
  const nameIn = (list: Workspace[], id: string) => list.find((w) => w.id === id)?.name ?? id;
  const note: Record<CommandRung, Key> = {
    strict: "set.grantsNoteStrict",
    workspace: "set.grantsNoteWorkspace",
    open: "set.grantsNoteOpen",
  };
  // A grant records the rung it was given under as a string; an unknown
  // one (a newer build's) is shown as-is rather than mislabelled.
  const rungName = (r: string) => {
    const known = RUNGS.find((x) => x.key === r);
    return known ? t(known.label) : r;
  };
  // What the row says about one grant, under the rung of the machine that
  // holds it: each machine's own setting decides whether it is in effect.
  const wording = (g: CommandGrant, under: CommandRung | null): [string, string | undefined] => {
    const ago = agoShort(new Date(g.granted_at).getTime(), now, tr);
    return under === "open"
      ? [t("set.grantInert", { ago }), t("set.grantInertDetail")]
      : g.rung === under
        ? [t("set.grantSince", { ago }), undefined]
        : [t("set.grantStale", { ago }), t("set.grantStaleDetail", { rung: rungName(g.rung) })];
  };
  const row = (
    key: string,
    name: ReactNode,
    line: string,
    more: string | undefined,
    inEffect: boolean,
    withdraw: () => void,
  ) => (
    <div className="fy-rule-row" key={key}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <Label text={name} help={more} id={"fy-help-grant-" + key} />
        <div className="fy-snote" style={{ color: inEffect ? undefined : "var(--fy-ink2)" }}>
          {line}
        </div>
      </div>
      <button type="button" className="fy-smallbtn" onClick={withdraw}>
        {t("set.grantWithdraw")}
      </button>
    </div>
  );
  const none = grants.length === 0 && remote.every((r) => r.grants.length === 0);
  return (
    <>
      <GroupHead text={t("set.grants")} help={t(note[rung])} id="fy-help-grants" />
      {none && (
        <div className="fy-rule-row">
          <div style={{ flex: 1, minWidth: 0 }}>
            <div className="fy-snote">{t("set.grantsNone")}</div>
          </div>
        </div>
      )}
      {grants.map((g) => {
        const [line, more] = wording(g, rung);
        return row(g.workspace_id, nameIn(folders, g.workspace_id), line, more, g.rung === rung, () =>
          onWithdraw(g.workspace_id),
        );
      })}
      {remote.map((r) =>
        r.grants.map((g) => {
          const [line, more] = wording(g, r.rung);
          return row(
            r.machine.id + ":" + g.workspace_id,
            <>
              <span className="fy-mchip">{r.machine.name}</span>
              {nameIn(r.folders, g.workspace_id)}
            </>,
            `${t("set.onMachine", { name: r.machine.name })} · ${line}`,
            more,
            g.rung === r.rung,
            () => onWithdrawRemote(r.machine.id, g.workspace_id),
          );
        }),
      )}
    </>
  );
}

/** Outbound network, one row per granted folder.
 *
 *  Per folder because one machine routinely holds both a repository whose
 *  build has to fetch dependencies and a repository whose contents should not
 *  be able to leave; a machine-wide switch could only take the lower answer.
 *
 *  The row says two different things and never merges them. The switch is
 *  what the folder asks for. The word beside it is what the folder actually
 *  gets, which differs on Linux — Landlock denies TCP and has no UDP rule, so
 *  DNS and QUIC still leave — and differs again on a machine that cannot deny
 *  anything at all. A screen that showed only the switch would be reporting a
 *  boundary that is not there.
 */
function NetworkRows({
  workspaces,
  remote,
  tr,
  onToggle,
  onToggleRemote,
}: {
  workspaces: Workspace[];
  remote: RemoteFolders[];
  tr: Translator;
  onToggle: (id: string, allow: boolean) => void;
  onToggleRemote: (machineID: string, id: string, allow: boolean) => void;
}) {
  const { t } = tr;
  const live = (list: Workspace[]) => list.filter((w) => w.status !== "revoked");
  const granted = live(workspaces);
  const elsewhere = remote.map((r) => ({ machine: r.machine, folders: live(r.folders) }));
  if (granted.length === 0 && elsewhere.every((r) => r.folders.length === 0)) {
    return null;
  }
  const reachOf = (w: Workspace) => w.network_reach ?? "allowed";
  const partial = granted.filter((w) => reachOf(w) === "partial");
  const unbounded = granted.filter((w) => reachOf(w) === "unbounded");
  const word: Record<string, Key> = {
    allowed: "set.netAllowed",
    denied: "set.netDenied",
    partial: "set.netPartial",
    unbounded: "set.netUnbounded",
  };
  const row = (key: string, w: Workspace, name: ReactNode, prefix: string, toggle: (allow: boolean) => void) => {
    const reach = reachOf(w);
    const allowed = reach === "allowed";
    return (
      <div className="fy-rule-row" key={key}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="fy-slabel">{name}</div>
          <div
            className="fy-snote"
            style={{
              color: reach === "unbounded" ? "var(--fy-amber)" : undefined,
            }}
          >
            {prefix}
            {t(word[reach] ?? "set.netAllowed")}
          </div>
        </div>
        <Switch
          label={`${t("set.network")} — ${prefix}${w.name}`}
          on={allowed}
          onToggle={() => toggle(!allowed)}
        />
      </div>
    );
  };
  return (
    <>
      <GroupHead text={t("set.network")} help={t("set.networkNote")} id="fy-help-network" />
      {granted.map((w) => row(w.id, w, w.name, "", (allow) => onToggle(w.id, allow)))}
      {elsewhere.map((r) =>
        r.folders.map((w) =>
          row(
            r.machine.id + ":" + w.id,
            w,
            <>
              <span className="fy-mchip">{r.machine.name}</span>
              {w.name}
            </>,
            `${t("set.onMachine", { name: r.machine.name })} · `,
            (allow) => onToggleRemote(r.machine.id, w.id, allow),
          ),
        ),
      )}
      {unbounded.length > 0 && (
        <div className="fy-warn">
          <div style={{ flex: 1 }}>
            <div className="fy-slabel">{unbounded.map((w) => w.name).join(", ")}</div>
            <div className="fy-snote" style={{ color: "var(--fy-ink2)" }}>
              {t("set.netUnboundedWhy")}
            </div>
          </div>
        </div>
      )}
      {unbounded.length === 0 && partial.length > 0 && (
        <div className="fy-rule-row">
          <div className="fy-snote" style={{ flex: 1 }}>
            {t("set.netPartialWhy")}
          </div>
        </div>
      )}
    </>
  );
}

/** The installed language servers, listed and nothing more.
 *
 *  Read-only on purpose. Stopping one is already automatic — twenty idle
 *  minutes and it is reaped — and the per-workspace permission to start one
 *  lives in this process, so it is gone when the Companion is. A "revoke"
 *  button would be a control over something that expires on its own.
 *
 *  Nothing renders when none is installed, the same as the provider list: a
 *  row that can only ever say "no" is not worth the space on a page whose
 *  design has no place for one.
 */
/** The language a server covers, written the way its community writes it.
 *
 *  The Core sends its own identifier (`rust`, `typescript`), which is what a
 *  settings file needs and not what a person reads. A name that is not in this
 *  table falls through unchanged: servers can be added by hand in the
 *  Companion's settings, and inventing a display name for one Fylane has never
 *  heard of would be guessing at the user's own configuration. */
function languageName(name: string): string {
  const known: Record<string, string> = {
    go: "Go",
    rust: "Rust",
    typescript: "TypeScript",
    python: "Python",
  };
  return known[name] ?? name;
}

/** The file types a server answers for, kept to a readable length.
 *
 *  TypeScript's server covers eight extensions. Printing all eight turns the
 *  line into a list nobody reads; printing three and counting the rest says
 *  the same thing and stays a sentence. */
function fileList(extensions: string[], tr: Translator): string {
  const shown = extensions.slice(0, 3).join(" ");
  const rest = extensions.length - 3;
  return rest > 0 ? tr.t("set.serverMore", { shown, rest: String(rest) }) : shown;
}

function ServerRows({ servers, tr }: { servers: LanguageServer[]; tr: Translator }) {
  const { t } = tr;
  if (servers.length === 0) {
    return null;
  }
  return (
    <>
      <GroupHead text={t("set.servers")} help={t("set.serversNote")} id="fy-help-servers" />
      {servers.map((s) => (
        <div className="fy-rule-row" key={s.name}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div className="fy-slabel">{languageName(s.name)}</div>
            <div className="fy-snote">{t("set.serverFiles", { files: fileList(s.extensions, tr) })}</div>
          </div>
          <span
            style={{
              fontSize: 11.5,
              whiteSpace: "nowrap",
              color: s.running ? "var(--fy-ink2)" : "var(--fy-faint)",
            }}
          >
            {t(s.running ? "set.serverRunning" : "set.serverIdle")}
          </span>
        </div>
      ))}
    </>
  );
}

/** The kernel read boundary, in the three states it actually has.
 *
 *  `absent` is a stated line with no switch. A disabled switch would sit in
 *  the off position and read as "you turned this off" — and telling absence
 *  from a choice is the whole reason the Core reports a word here instead of
 *  a boolean. Hiding the row instead was the other option and is worse: a
 *  defence that is not present is exactly the thing a person needs told.
 *
 *  `off` keeps saying so. Same rule as the open rung and the trusted
 *  providers above: an authorization in force is only safe while it is
 *  visible, and so is a defence that has been switched off. */
function ReadBoundaryRow({
  info,
  tr,
  onToggle,
}: {
  info: ReadBoundaryInfo | null;
  tr: Translator;
  onToggle: (on: boolean) => void;
}) {
  const { t } = tr;
  const absent = info?.state === "absent";
  const on = info?.state === "enforced";
  return (
    <>
      <div className="fy-rule-row">
        <div style={{ flex: 1, minWidth: 0 }}>
          <Label
            text={t("set.readBoundary")}
            help={absent ? undefined : t("set.readBoundaryNote")}
            id="fy-help-readbox"
          />
          {absent && <div className="fy-snote">{info.detail || t("set.readBoundaryAbsent")}</div>}
        </div>
        {!absent && (
          <Switch
            label={t("set.readBoundary")}
            on={on}
            disabled={info === null}
            onToggle={() => onToggle(!on)}
          />
        )}
      </div>
      {info?.state === "off" && (
        <div className="fy-warn">
          <div style={{ flex: 1 }}>
            <div className="fy-slabel">{t("set.readBoundaryOff")}</div>
            <div className="fy-snote" style={{ color: "var(--fy-ink2)" }}>
              {t("set.readBoundaryOffBody")}
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function RiskyRow({ tr }: { tr: Translator }) {
  const { t } = tr;
  const [tip, setTip] = useState(false);
  return (
    <div className="fy-rule-row" style={{ paddingTop: 2 }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span className="fy-dot fy-dot-sm" style={{ background: "var(--fy-amber)" }} />
          <Label text={t("set.risky")} help={t("set.riskyNote")} id="fy-help-risky" />
        </div>
      </div>
      <button
        type="button"
        className="fy-fixedpill"
        aria-describedby="fy-risky-tip"
        onMouseEnter={() => setTip(true)}
        onMouseLeave={() => setTip(false)}
        onFocus={() => setTip(true)}
        onBlur={() => setTip(false)}
      >
        {t("set.riskyValue")}
        <span style={{ fontSize: 10.5, color: "var(--fy-faint)" }}>{t("set.fixed")}</span>
        {tip && (
          <span className="fy-tip" id="fy-risky-tip" role="tooltip">
            {t("set.riskyTip")}
          </span>
        )}
      </button>
    </div>
  );
}

// ── connection ─────────────────────────────────────────────────────────────

function Connection({
  tr,
  deps,
  onError,
}: {
  tr: Translator;
  deps: SettingsDeps;
  onError: (m: string) => void;
}) {
  const { t } = tr;
  const [info, setInfo] = useState<ConnectInfo | null>(null);
  const [open, setOpen] = useState("");
  const [busy, setBusy] = useState("");
  const [switching, setSwitching] = useState(false);
  const [copied, setCopied] = useState(false);
  const [draft, setDraft] = useState({ hostname: "", token: "" });

  // A quick tunnel renames the machine on restart, so the section re-reads
  // while it is on screen rather than trusting one load.
  useEffect(() => {
    let alive = true;
    const load = () =>
      deps
        .connect()
        .then((n) => alive && setInfo(n))
        .catch(() => {});
    load();
    const timer = window.setInterval(load, 3000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, []);

  const awaitRestart = async () => {
    let asked = false;
    for (let i = 0; i < 40; i++) {
      await new Promise((r) => window.setTimeout(r, 500));
      try {
        const next = await deps.connect();
        if (!next.restarting) {
          setInfo(next);
          return;
        }
      } catch {
        if (!asked) {
          asked = true;
          await startCore().catch(() => {});
        }
      }
    }
    onError(t("conn.switchTimeout"));
  };

  const use = async (p: TunnelProvider) => {
    setBusy(p.kind);
    try {
      const next = await applyTunnel({
        provider: p.kind,
        hostname: draft.hostname.trim(),
        token: draft.token.trim(),
      });
      if (next.restarting) {
        setSwitching(true);
        await awaitRestart();
      } else {
        setInfo(next);
      }
      setOpen("");
      setDraft({ hostname: "", token: "" });
    } catch (e) {
      onError(e instanceof Error ? e.message : t("conn.title"));
    } finally {
      setBusy("");
      setSwitching(false);
    }
  };

  const signIn = async (p: TunnelProvider) => {
    try {
      setInfo(await deps.startSetup(p.kind));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("conn.signInFailed"));
    }
  };

  const signOut = async (p: TunnelProvider) => {
    try {
      setInfo(await deps.signOut(p.kind));
      // The token box held a credential for a way in that no longer has one.
      setDraft((d) => ({ ...d, token: "" }));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("conn.signOutFailed"));
    }
  };

  // Pressing this is the consent a download requires. What arrives is decided by
  // the Core's pin table — the panel above showed the program, the source and
  // the digest it will be checked against, and this sends none of them back.
  const getProgram = async (p: TunnelProvider) => {
    try {
      setInfo(await deps.startDownload(p.kind));
    } catch (e) {
      onError(e instanceof Error ? e.message : t("conn.downloadFailed"));
    }
  };

  const stopGetProgram = async () => {
    try {
      setInfo(await deps.cancelDownload());
    } catch {
      // Best-effort, same as cancelling a sign-in: the poll reports what
      // actually happened, and a toast here would be about the cancel rather
      // than about the download the user was abandoning.
    }
  };

  const stopSignIn = async () => {
    try {
      setInfo(await deps.cancelSetup());
    } catch {
      // Cancelling is best-effort: the poll reports what actually happened,
      // and an error toast here would be about the cancel rather than about
      // the sign-in the user was trying to abandon.
    }
  };

  // The sign-in page is opened once per URL. The section re-reads every three
  // seconds, so opening on every poll would pile up browser tabs.
  const opened = useRef("");
  useEffect(() => {
    const setup = info?.setup;
    if (setup?.phase !== "waiting" || !setup.url) {
      return;
    }
    // cloudflared and tailscale open their own browser. Opening it here too
    // put two tabs on screen for one authorization; the button below stays,
    // for the case where the vendor's own attempt did not come up.
    if (info?.providers.find((p) => p.kind === setup.provider)?.opens_browser) {
      return;
    }
    if (opened.current !== setup.url) {
      opened.current = setup.url;
      void openURL(setup.url).catch(() => {});
    }
  }, [info?.setup?.url, info?.setup?.phase]);

  // The user is in a browser when this finishes, and the answer is back here.
  // Only on the change: the phase stays "ready" until the next sign-in, and
  // raising on every poll would fight whatever they do next.
  const lastPhase = useRef<string | undefined>(undefined);
  useEffect(() => {
    const phase = info?.setup?.phase;
    if (phase !== lastPhase.current && (phase === "ready" || phase === "failed")) {
      void raiseWindow().catch(() => {});
    }
    lastPhase.current = phase;
  }, [info?.setup?.phase]);

  // The hostname the sign-in implies, filled in rather than asked for. It
  // belongs to one way in, not to the section: another provider's domain is
  // not a suggestion for this one.
  const suggested = info?.providers.find((p) => p.kind === open)?.suggested_hostname ?? "";
  // Only into an untouched field — overwriting what someone is typing would
  // be the window arguing with them.
  const filled = useRef("");
  useEffect(() => {
    if (suggested && filled.current !== suggested && draft.hostname === "") {
      filled.current = suggested;
      setDraft((d) => ({ ...d, hostname: suggested }));
    }
  }, [suggested]);

  const relayMode = info?.mode === "relay";
  const running = info?.state === "running" || info?.state === "starting";
  const endpoint = info?.connector_url ?? "";
  const live = info !== null && connectionLive(info);

  // The pairing code. The connect page offers a typed code when its loopback
  // claim finds no Companion — a browser on another machine — and until now
  // nothing in this window could produce one, so that fallback was a door
  // with no key behind it.
  const [code, setCode] = useState<PairingCodeInfo | null>(null);
  const [codeSecs, setCodeSecs] = useState(0);
  const [minting, setMinting] = useState(false);
  const [codeErr, setCodeErr] = useState("");
  const [codeCopied, setCodeCopied] = useState(false);

  const mint = async () => {
    setMinting(true);
    setCodeErr("");
    try {
      const got = await deps.mintCode();
      setCode(got);
      setCodeSecs(got.expires_in_seconds);
    } catch (e) {
      setCodeErr(String(e));
      setCode(null);
    } finally {
      setMinting(false);
    }
  };

  // Ticks only while a code is on screen. It is the code's own life running
  // out, so it starts when the code arrives rather than when the page opens.
  useEffect(() => {
    if (!code) {
      return;
    }
    const id = window.setInterval(() => setCodeSecs((s) => (s <= 0 ? 0 : s - 1)), 1000);
    return () => window.clearInterval(id);
  }, [code]);
  const name =
    relayMode || !info?.provider
      ? t("conn.relay")
      : t(PROVIDER_NAMES[info.provider] ?? "conn.title");

  if (!info) {
    return (
      <div
        className="fy-insetcard"
        style={{
          display: "flex",
          justifyContent: "center",
          padding: "34px 19px",
        }}
      >
        <Jelly size={28} busyLabel={t("set.connection")} />
      </div>
    );
  }

  return (
    <>
      <div className="fy-insetcard">
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: 18,
            flexWrap: "wrap",
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 11 }}>
            <span
              className="fy-dot"
              style={{
                background: live ? "var(--fy-sage)" : "var(--fy-rule)",
              }}
            />
            <span style={{ fontSize: 16, fontWeight: 500, letterSpacing: ".005em" }}>{name}</span>
            {live && <span className="fy-sagepill">{t("setV3.inEffect")}</span>}
          </div>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 16,
              fontSize: 11.5,
              color: "var(--fy-muted)",
              flexWrap: "wrap",
            }}
          >
            {switching && <Jelly size={20} busyLabel={t("conn.starting")} />}
            {/* The Core sends "" before it has decided; there is no word for
                that, and asking the dictionary for one would throw. */}
            {info.state !== "" && <span>{t(`conn.state.${info.state}` as Key)}</span>}
            {relayMode && (
              <>
                <span className="fy-vline" style={{ height: 10 }} />
                <span>{t("set.hosted")}</span>
              </>
            )}
          </div>
        </div>

        <div className="fy-endpoint">
          <span className="fy-eyebrow fy-eyebrow-tight" style={{ flex: "none" }}>
            {t("setV3.endpoint")}
          </span>
          <span
            className="fy-endpoint-url"
            style={endpoint ? undefined : { color: "var(--fy-faint)" }}
          >
            {endpoint || t("conn.noAddress")}
          </span>
          <button
            type="button"
            className="fy-quiet"
            style={{
              flex: "none",
              color: copied ? "var(--fy-sage)" : undefined,
            }}
            disabled={!endpoint}
            onClick={() => {
              void copyText(endpoint);
              setCopied(true);
              window.setTimeout(() => setCopied(false), 1500);
            }}
          >
            {copied ? t("conn.copied") : t("conn.copy")}
          </button>
        </div>

        {/* Same row, same language as the endpoint above it: a labelled mono
            value with quiet actions. The code is minted on the press, never
            on render. */}
        <div className="fy-endpoint">
          <span className="fy-eyebrow fy-eyebrow-tight" style={{ flex: "none" }}>
            {t("pairv3.label")}
          </span>
          {minting ? (
            <span style={{ flex: 1, minWidth: 0 }}>
              <Jelly size={20} busyLabel={t("pairv3.minting")} />
            </span>
          ) : (
            <span
              className="fy-endpoint-url"
              style={code && codeLeft(codeSecs) ? undefined : { color: "var(--fy-faint)" }}
            >
              {codeErr || (code && codeLeft(codeSecs) ? code.code : t("pairv3.none"))}
            </span>
          )}
          {code && codeLeft(codeSecs) && !minting && (
            <>
              <span style={{ flex: "none", fontSize: 11.5, color: "var(--fy-muted)" }}>
                {t("pairv3.left", { time: codeLeft(codeSecs) ?? "" })}
              </span>
              <button
                type="button"
                className="fy-quiet"
                style={{ flex: "none", color: codeCopied ? "var(--fy-sage)" : undefined }}
                onClick={() => {
                  void copyText(code.code);
                  setCodeCopied(true);
                  window.setTimeout(() => setCodeCopied(false), 1500);
                }}
              >
                {codeCopied ? t("conn.copied") : t("conn.copy")}
              </button>
            </>
          )}
          <button
            type="button"
            className="fy-quiet"
            style={{ flex: "none" }}
            // A code names an address platforms can reach. Without one the
            // Core refuses anyway, and offering the button would only produce
            // an error the row could have avoided.
            disabled={minting || !endpoint}
            onClick={() => void mint()}
          >
            {code ? t("pairv3.again") : t("pairv3.show")}
          </button>
        </div>
      </div>

      <div className="fy-eyebrow fy-eyebrow-tight" style={{ margin: "22px 0 4px" }}>
        {t("set.otherWays")}
      </div>
      {info.providers.map((p) => {
        const active = !relayMode && info.provider === p.kind && running;
        const shown = open === p.kind;
        return (
          <div key={p.kind}>
            <div className="fy-wayrow">
              <span
                className="fy-dot fy-dot-sm"
                style={{
                  background: active
                    ? "var(--fy-sage)"
                    : p.authorized
                      ? "var(--fy-amber)"
                      : "var(--fy-rule)",
                }}
              />
              <span className="fy-wayname">{t(PROVIDER_NAMES[p.kind] ?? "conn.title")}</span>
              <span className="fy-waynote">
                {active ? t("conn.inUse") : t(PROVIDER_NOTES[p.kind] ?? "conn.title")}
              </span>
              <button
                type="button"
                className="fy-underbtn"
                style={{ flex: "none" }}
                aria-expanded={shown}
                onClick={() => {
                  // Each way in has its own credential. Carrying a half-typed
                  // token from one row into the next would offer to start a
                  // tunnel with the wrong provider's secret. The hostname is
                  // seeded from this provider's own suggestion, because the
                  // effect above only fires when the suggestion changes and a
                  // suggestion that arrived earlier would never be applied.
                  const seed = p.suggested_hostname ?? "";
                  setDraft({ hostname: shown ? "" : seed, token: "" });
                  filled.current = shown ? "" : seed;
                  setOpen(shown ? "" : p.kind);
                }}
              >
                {shown ? t("set.setupClose") : t("set.setup")}
              </button>
            </div>
            {shown && (
              <div className="fy-waybody">
                <Ready
                  tr={tr}
                  provider={p}
                  setup={info.setup}
                  platform={info.platform ?? ""}
                  download={info.download}
                  token={draft.token}
                  onToken={(token) => setDraft((d) => ({ ...d, token }))}
                  onSignIn={() => void signIn(p)}
                  onSignOut={() => void signOut(p)}
                  onCancelSignIn={() => void stopSignIn()}
                  onDownload={() => void getProgram(p)}
                  onCancelDownload={() => void stopGetProgram()}
                  onOpen={(url) => void openURL(url).catch(() => {})}
                />
                {/* Not in the design, but a named tunnel cannot start without
                    somewhere to answer — a "use this" that silently fails
                    would not be a working setting. */}
                {p.needs_hostname && (
                  <div style={{ marginTop: 14, maxWidth: 420 }}>
                    <input
                      className="fy-field"
                      value={draft.hostname}
                      placeholder={t("conn.fieldHostname")}
                      aria-label={t("conn.fieldHostname")}
                      onChange={(e) => setDraft((d) => ({ ...d, hostname: e.target.value }))}
                    />
                    {/* Signed in but the domain has not come back yet: that is
                        the Core still working, not the user still deciding.
                        The field stays typable — someone who knows their own
                        hostname should not have to wait for the lookup. */}
                    {awaitingHostname(p, p.suggested_hostname ?? "", draft.hostname) ? (
                      <div style={{ marginTop: 10 }}>
                        <Jelly size={20} label={t("conn.hostnameWait")} />
                      </div>
                    ) : (
                      <div className="fy-snote">{t("conn.hostnameHow")}</div>
                    )}
                  </div>
                )}
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 14,
                    marginTop: 14,
                    flexWrap: "wrap",
                  }}
                >
                  <button
                    type="button"
                    className="fy-smallbtn"
                    disabled={!canUse(p, draft.token, draft.hostname) || busy !== "" || active}
                    onClick={() => void use(p)}
                  >
                    {busy === p.kind ? (
                      <Jelly size={20} busyLabel={t("conn.starting")} />
                    ) : (
                      t("set.useThis")
                    )}
                  </button>
                  <span style={{ fontSize: 12, color: "var(--fy-faint)" }}>
                    {!p.installed
                      ? t("set.installFirst")
                      : awaitingHostname(p, p.suggested_hostname ?? "", draft.hostname)
                        ? t("conn.hostnameWait")
                        : !canUse(p, draft.token, draft.hostname)
                          ? p.needs_hostname && draft.hostname.trim() === ""
                            ? t("conn.needHostname")
                            : t("conn.signInFirst")
                          : t(p.stable ? "conn.stable" : "conn.temporary")}
                  </span>
                </div>
              </div>
            )}
            <div className="fy-hline" />
          </div>
        );
      })}
    </>
  );
}

/** One labelled mono fact. The first carries the hairline and the rest hang
 *  under it as one group, so three facts read as one disclosure rather than
 *  three separate settings. Same row, same language as the endpoint above. */
function Fact({ label, value, first }: { label: string; value: string; first?: boolean }) {
  return (
    <div
      className="fy-endpoint"
      style={first ? { marginTop: 12 } : { marginTop: 8, paddingTop: 0, borderTop: "none" }}
    >
      <span className="fy-eyebrow fy-eyebrow-tight" style={{ flex: "none", width: 74 }}>
        {label}
      </span>
      <span
        className="fy-endpoint-url"
        style={{ whiteSpace: "normal", wordBreak: "break-all", fontSize: 12.5 }}
      >
        {value}
      </span>
    </div>
  );
}

/** The program this way in needs is not on the machine. Four states, and which
 *  one shows is decided by the Core: an offer exists only when this build has a
 *  pin for this platform, so the window never puts a button on screen that
 *  could not be honoured. */
function Missing({
  tr,
  provider: p,
  platform,
  download,
  onDownload,
  onCancelDownload,
  onOpen,
}: {
  tr: Translator;
  provider: TunnelProvider;
  platform: string;
  download?: TunnelDownloadState;
  onDownload: () => void;
  onCancelDownload: () => void;
  onOpen: (url: string) => void;
}) {
  const { t } = tr;
  // One download runs at a time, so another provider's progress must not
  // appear under this row.
  const mine = download && download.provider === p.kind ? download : undefined;

  if (mine?.phase === "running") {
    return (
      <div>
        <div className="fy-snote" style={{ marginTop: 0 }}>
          {t("conn.downloading")}
        </div>
        {/* No byte counter: tunnelget streams to the hash and the file without
            reporting progress, so a number here would be a claim the Core
            cannot make. Same shape as the sign-in waiting state below. */}
        <div style={{ display: "flex", alignItems: "center", gap: 16, marginTop: 12 }}>
          <Jelly size={20} label={t("conn.downloadingShort")} />
          <button type="button" className="fy-quiet" onClick={onCancelDownload}>
            {t("conn.signInCancel")}
          </button>
        </div>
      </div>
    );
  }

  if (mine?.phase === "failed") {
    return (
      <div>
        {/* Not fy-warn. That class marks a condition the user is living in —
            the open rung, providers following it — plus the one confirm
            before entering it. A download that failed and discarded its own
            file leaves no condition behind, and lending amber to a failed
            action would cost the open rung the one thing it needs from
            its standing warning: that it never reads as transient. This is
            the shape src/ already uses for "attend to this, here". */}
        <div
          className="fy-snote"
          style={{ marginTop: 0, color: "var(--fy-brick)", maxWidth: 520 }}
        >
          {t("conn.downloadFailed")}
        </div>
        {/* The Core's own words for what went wrong. The line above is true of
            every failure; this is the one that says which. */}
        {mine.detail && (
          <div className="fy-snote" style={{ maxWidth: 520 }}>
            {mine.detail}
          </div>
        )}
        <div style={{ display: "flex", alignItems: "center", gap: 14, marginTop: 12 }}>
          <button type="button" className="fy-smallbtn" onClick={onDownload}>
            {t("conn.downloadRetry")}
          </button>
          <button
            type="button"
            className="fy-quiet"
            disabled={!p.download}
            onClick={() => p.download && onOpen(p.download)}
          >
            {t("conn.openDownload")}
          </button>
        </div>
      </div>
    );
  }

  // No pin for this platform: there is nothing to offer, so only the way out
  // is drawn.
  if (!p.offer) {
    return (
      <div>
        <div className="fy-snote" style={{ marginTop: 0 }}>
          {t("conn.unpinned", { name: productName(p.binary) })}
        </div>
        <button
          type="button"
          className="fy-smallbtn"
          style={{ marginTop: 10 }}
          disabled={!p.download}
          onClick={() => p.download && onOpen(p.download)}
        >
          {t("conn.openDownload")}
        </button>
      </div>
    );
  }

  return (
    <div>
      <div className="fy-snote" style={{ marginTop: 0 }}>
        {t("conn.offerLead", { name: p.binary })}
      </div>

      <Fact first label={t("conn.offerProgram")} value={`${p.offer.binary} ${p.offer.version}`} />
      <Fact label={t("conn.offerSource")} value={p.offer.source} />
      <Fact label={t("conn.offerDigest")} value={p.offer.sha256} />

      <div className="fy-snote" style={{ marginTop: 12 }}>
        {t("conn.offerNote")}
      </div>

      {/* Two actions side by side. Downloading is the exception carved
          out, not the default path — hiding "install it yourself" behind the
          first button would quietly make the exception the default. */}
      <div style={{ display: "flex", alignItems: "center", gap: 14, marginTop: 14 }}>
        <button type="button" className="fy-smallbtn" onClick={onDownload}>
          {t("conn.offerGet")}
        </button>
        <button
          type="button"
          className="fy-quiet"
          disabled={!p.download}
          onClick={() => p.download && onOpen(p.download)}
        >
          {t("conn.offerSelf")}
        </button>
      </div>
    </div>
  );
}

/** Getting one way in ready to use. Three shapes, none of them a command to
 *  copy: the program is missing (offer the pinned build, or the vendor's page
 *  when there is no pin), browser sign-in (run the vendor's own login and let
 *  the user approve on the web), or a token (open the page that issues it and
 *  take a paste). */
function Ready({
  tr,
  provider: p,
  setup,
  platform,
  download,
  token,
  onToken,
  onSignIn,
  onSignOut,
  onCancelSignIn,
  onDownload,
  onCancelDownload,
  onOpen,
}: {
  tr: Translator;
  provider: TunnelProvider;
  setup?: TunnelSetupState;
  platform: string;
  download?: TunnelDownloadState;
  token: string;
  onToken: (v: string) => void;
  onSignIn: () => void;
  onSignOut: () => void;
  onCancelSignIn: () => void;
  onDownload: () => void;
  onCancelDownload: () => void;
  onOpen: (url: string) => void;
}) {
  const { t } = tr;

  if (!p.installed) {
    return (
      <Missing
        tr={tr}
        provider={p}
        platform={platform}
        download={download}
        onDownload={onDownload}
        onCancelDownload={onCancelDownload}
        onOpen={onOpen}
      />
    );
  }

  if (p.setup === "token") {
    return (
      <div>
        <div className="fy-snote" style={{ marginTop: 0 }}>
          {p.authorized ? t("conn.tokenStored") : t("conn.tokenHow")}
        </div>
        <div
          style={{
            display: "flex",
            flexWrap: "wrap",
            alignItems: "center",
            gap: 12,
            marginTop: 10,
          }}
        >
          <input
            className="fy-field"
            style={{ flex: "1 1 220px", maxWidth: 420 }}
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={token}
            placeholder={t("conn.fieldToken")}
            aria-label={t("conn.fieldToken")}
            onChange={(e) => onToken(e.target.value)}
          />
          <button
            type="button"
            className="fy-smallbtn"
            disabled={!p.credential}
            onClick={() => p.credential && onOpen(p.credential)}
          >
            {t("conn.openDashboard")}
          </button>
          {p.authorized && p.can_sign_out && (
            <button type="button" className="fy-quiet" onClick={onSignOut}>
              {t("conn.forgetToken")}
            </button>
          )}
        </div>
      </div>
    );
  }

  if (p.setup !== "browser") {
    return null;
  }

  // Only this provider's own sign-in is this provider's business: one runs at
  // a time, and another one's progress must not appear under this row.
  const mine = setup && setup.provider === p.kind ? setup : undefined;

  if (mine?.phase === "starting" || mine?.phase === "waiting") {
    return (
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 16,
          flexWrap: "wrap",
        }}
      >
        <Jelly
          size={20}
          label={mine.phase === "starting" ? t("conn.signingIn") : t("conn.signInWaiting")}
        />
        {mine.url && (
          <button type="button" className="fy-smallbtn" onClick={() => onOpen(mine.url!)}>
            {t("conn.signInOpen")}
          </button>
        )}
        <button type="button" className="fy-quiet" onClick={onCancelSignIn}>
          {t("conn.signInCancel")}
        </button>
      </div>
    );
  }

  // What this panel adds is state, not description: the row above it already
  // carries the provider's own sentence, and repeating it here printed the
  // same line twice. Not signed in needs no line at all — the button says it.
  const state =
    mine?.phase === "failed"
      ? mine.detail || t("conn.signInFailed")
      : p.authorized
        ? t("conn.signedIn")
        : "";

  return (
    <div>
      {state && (
        <div className="fy-snote" style={{ marginTop: 0 }}>
          {state}
        </div>
      )}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 12,
          marginTop: state ? 10 : 0,
        }}
      >
        <button type="button" className="fy-smallbtn" onClick={onSignIn}>
          {p.authorized ? t("conn.signInAgain") : t("conn.signIn")}
        </button>
        {/* Only where the credential is Fylane's to remove. A sign-out that
            silently did nothing would be worse than not offering one. */}
        {p.authorized && p.can_sign_out && (
          <button type="button" className="fy-quiet" onClick={onSignOut}>
            {t("conn.signOut")}
          </button>
        )}
      </div>
      {p.authorized && !p.can_sign_out && (
        <div className="fy-snote">{t("conn.signOutNotOurs", { name: p.binary })}</div>
      )}
    </div>
  );
}

// ── small controls ─────────────────────────────────────────────────────────

function Opts<T extends string>({
  label,
  options,
  value,
  onPick,
}: {
  label: string;
  options: { key: T; label: string }[];
  value: T;
  onPick: (key: T) => void;
}) {
  return (
    <div className="fy-opts" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.key}
          type="button"
          role="radio"
          className="fy-opt"
          aria-checked={o.key === value}
          onClick={() => onPick(o.key)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function Switch({
  label,
  on,
  disabled,
  onToggle,
}: {
  label: string;
  on: boolean;
  disabled?: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      className="fy-switch"
      aria-label={label}
      aria-checked={on}
      disabled={disabled}
      onClick={onToggle}
    >
      <i />
    </button>
  );
}

/** The timeout, as the desktop stepper the design draws. It steps through the
 *  values the Core accepts rather than being free-typed; a value stored from
 *  elsewhere is shown as it is and steps to its nearest neighbour. */
function Stepper({
  label,
  seconds,
  tr,
  onPick,
}: {
  label: string;
  seconds: number | null;
  tr: Translator;
  onPick: (secs: number) => void;
}) {
  const { t } = tr;
  const unknown = seconds === null;
  const down = unknown ? null : stepTimeout(seconds, -1);
  const up = unknown ? null : stepTimeout(seconds, 1);
  const format = (secs: number) =>
    secs < 60 ? t("set.secs", { n: secs }) : t("set.mins", { n: secs / 60 });

  return (
    <div
      className="fy-stepper"
      data-unknown={unknown ? "true" : "false"}
      role="group"
      aria-label={label}
    >
      <button
        type="button"
        aria-label={`${label} −`}
        disabled={down === null}
        onClick={() => down !== null && onPick(down)}
      >
        −
      </button>
      <i />
      {/* The stored value as it is, even when it is not one of ours: showing
          the nearest option instead would report a setting nobody chose. */}
      <output>{unknown ? "\u2014" : format(seconds)}</output>
      <i />
      <button
        type="button"
        aria-label={`${label} +`}
        disabled={up === null}
        onClick={() => up !== null && onPick(up)}
      >
        +
      </button>
    </div>
  );
}

function PromiseCell({ tag, title, body }: { tag: string; title: string; body: ReactNode }) {
  return (
    <div className="fy-promise">
      <div className="fy-promise-tag">{tag}</div>
      <div className="fy-promise-title">{title}</div>
      <div className="fy-promise-body">{body}</div>
    </div>
  );
}
