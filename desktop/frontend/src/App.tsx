import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  addMachine,
  addWorkspace,
  browseMachine,
  cancelTask,
  clearBackups,
  clearTasks,
  connectMachine,
  copyText,
  disconnectMachine,
  installMachine,
  openWorkspaceDir,
  probeMachine,
  updateMachine,
  fetchPairClaims,
  pauseWorkspace,
  raiseWindow,
  acceptChangeSet,
  remoteCore,
  removeMachine,
  resolveApproval,
  resolvePairClaim,
  resumeWorkspace,
  savePrefs,
  rollbackChangeSet,
  selectMachine,
  selectWorkspace,
  startCore,
  type CommandSettingsInfo,
  type PairClaim,
  type TaskInfo,
  memorySource,
  openURL,
} from "./lib/core";
import { RemoteFolderSheet } from "./components/RemoteFolderSheet";
import { AddMachineSheet } from "./components/AddMachineSheet";
import {
  WindowHide,
  WindowMinimise,
  WindowToggleMaximise,
} from "../wailsjs/runtime/runtime";
import { runFirstWrite } from "./lib/firstwrite";
import { pollCore, type MachineView } from "./lib/poll";
import { detectOS } from "./lib/platform";
import { OFFLINE_SNAPSHOT, type LaneSnapshot } from "./lib/lane";
import { canRollback } from "./lib/records";
import { updateNotice } from "./lib/settings";
import { LaneScreen } from "./screens/Lane";
import { OnboardingScreen } from "./screens/Onboarding";
import { TasksScreen } from "./screens/Tasks";
import { MemoryScreen } from "./screens/Memory";
import { CommandPalette } from "./components/CommandPalette";
import { PairClaimSheet } from "./components/PairClaimSheet";
import { SettingsScreen } from "./screens/Settings";
import { Jelly } from "./components/Jelly";
import { Dock } from "./components/Dock";
import {
  LangContext,
  storeLang,
  storedLang,
  useT,
  type Key,
  type Lang,
} from "./lib/i18n";
import { applyTheme, storeTheme, storedTheme, type Theme } from "./lib/theme";

// The window is the product (design decision). One native window, seven
// screens, a single line of text for navigation, and no title bar: the
// traffic lights float over the content on the first row's baseline, which
// is why every screen reserves 86px on the left.

// Desktop v2 §1: three top-level pages and no fourth. Route rules, the trace
// and the workspaces bench are gone — not hidden, not disabled, not behind a
// menu. Switching workspaces lives on the lane's own anchor, and every
// setting is one page.
export type Screen = "lane" | "tasks" | "memory" | "settings";

// Batch S added a fourth (Fylane-V3 board 17): what the connected AI wrote
// down about the folder, because it is the user's to read and correct.
const NAV: { key: Screen; label: Key }[] = [
  { key: "lane", label: "nav.lane" },
  { key: "tasks", label: "nav.tasks" },
  { key: "memory", label: "nav.memory" },
  { key: "settings", label: "nav.settings" },
];

/** Where "how the AI takes notes" points: the README's memory section. */
const MEMORY_HELP_URL = "https://github.com/leazoot/fylane#readme";

const POLL_MS = 2000;
export default function App() {
  const [lang, setLang] = useState<Lang>(storedLang);
  // The Core is told too, so what it says on its own — a system
  // notification — is in the window's language. Best-effort: a Core that
  // is not up yet hears it on the first poll instead.
  const chooseLang = useCallback((next: Lang) => {
    setLang(next);
    storeLang(next);
    void savePrefs({ language: next }).catch(() => {});
  }, []);
  return (
    <LangContext.Provider value={{ lang, setLang: chooseLang }}>
      <Window lang={lang} onLang={chooseLang} />
    </LangContext.Provider>
  );
}

function Window({ lang, onLang }: { lang: Lang; onLang(lang: Lang): void }) {
  const translator = useT();
  const { t, tn } = translator;
  const [theme, setTheme] = useState<Theme>(storedTheme);
  useEffect(() => applyTheme(theme), [theme]);
  const chooseTheme = useCallback((next: Theme) => {
    setTheme(next);
    storeTheme(next);
  }, []);
  const [screen, setScreen] = useState<Screen>("lane");
  // Chrome shape is fixed for the life of the window; the OS does not change
  // underneath a running app.
  const [os] = useState(detectOS);
  const [snapshot, setSnapshot] = useState<LaneSnapshot>(OFFLINE_SNAPSHOT);
  const [tasks, setTasks] = useState<TaskInfo[]>([]);
  const [commands, setCommands] = useState<CommandSettingsInfo | null>(null);
  // Settings › execution owns this. Until the Core answers, a running task
  // can be stopped: taking the control away is the choice that needs saying.
  const [canStopTasks, setCanStopTasks] = useState(true);
  const [workspaces, setWorkspaces] = useState<LaneSnapshot["workspace"][]>([]);
  const [currentID, setCurrentID] = useState("");
  const [claims, setClaims] = useState<PairClaim[]>([]);
  // Remote machines, and which one the rail stands on ("" is this
  // computer). The choice is the Core's: it decides which folder the
  // platform is told is current, so the window reads it back on every
  // poll and only writes it. It changes what the rail shows, never what
  // the gate holds.
  const [machines, setMachines] = useState<MachineView[]>([]);
  const [machineID, setMachineID] = useState<string>("");
  // Choices still on their way to the Core; a poll that left before one
  // landed must not put the rail back.
  const [sheet, setSheet] = useState<"none" | "machine" | "folder">("none");
  // The machine being edited, when the machine sheet is open for one.
  const [editing, setEditing] = useState<MachineView["info"] | null>(null);
  const [now, setNow] = useState(() => new Date());
  const [commandsOpen, setCommandsOpen] = useState(false);
  const [error, setError] = useState("");
  // First run is decided from real state, not from a "seen" flag: the lane
  // has no far end until a folder is granted, so onboarding opens when the
  // Core is reachable and no workspace exists, and never reopens once one
  // does. It also stays open while the user is still walking through it,
  // which is why the answer is latched rather than re-derived every poll.
  const [firstRun, setFirstRun] = useState<"unknown" | "on" | "off">("unknown");

  const inFlight = useRef(false);
  const choosing = useRef(0);
  const toldLang = useRef(false);
  const lastHeld = useRef(0);

  const refresh = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const poll = await pollCore();
      setTasks(poll.tasks);
      setCommands(poll.commands);
      if (poll.prefs.language !== lang && !toldLang.current) {
        toldLang.current = true;
        void savePrefs({ language: lang })
          .catch(() => {})
          .finally(() => {
            toldLang.current = false;
          });
      }
      setCanStopTasks(poll.prefs.allow_stop_tasks);
      setWorkspaces(poll.workspaces);
      setCurrentID(poll.currentWorkspaceID);
      setMachines(poll.machines);
      if (choosing.current === 0) setMachineID(poll.currentMachineID);
      setFirstRun((s) =>
        s !== "unknown" ? s : poll.workspaces.length === 0 ? "on" : "off",
      );
      setSnapshot(poll.snapshot);
      setNow(new Date());
      setError("");

      // A new arrival at the gate blocks the platform on the user's
      // decision, so the window must not stay buried.
      const held = poll.snapshot.approvals.length;
      if (held > lastHeld.current) {
        void raiseWindow();
      }
      lastHeld.current = held;

      setClaims(await fetchPairClaims());
    } catch {
      setSnapshot(OFFLINE_SNAPSHOT);
      setWorkspaces([]);
      setMachines([]);
      setClaims([]);
      lastHeld.current = 0;
      // An unreachable Core is an answer too. Without this the window would
      // wait on a poll that has already failed and never draw the lane's
      // offline scene, which is the one screen with a way out of it.
      setFirstRun((s) => (s === "unknown" ? "off" : s));
    } finally {
      inFlight.current = false;
    }
  }, []);

  useEffect(() => {
    void refresh();
    const id = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(id);
  }, [refresh]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setCommandsOpen((v) => !v);
      }
      if (e.key === "Escape") setCommandsOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Returns what the action returned, or null when it failed. Most callers
  // want only the refresh and ignore it; one needs to say what it removed.
  const act = useCallback(
    async <T,>(fn: () => Promise<T>, failure: string): Promise<T | null> => {
      let out: T | null = null;
      try {
        out = await fn();
        setError("");
      } catch (e) {
        setError(e instanceof Error ? e.message : failure);
      }
      await refresh();
      return out;
    },
    [refresh],
  );

  // The machine the rail stands on decides whose folders the lane shows
  // and where a folder-level action goes. Records are another matter: each
  // one carries where it came from, so approving, stopping and undoing look
  // the record up rather than the rail.
  const selected = machines.find((m) => m.info.id === machineID) ?? null;
  const viewWorkspaces = selected ? selected.workspaces : workspaces;
  const ws = selected
    ? (selected.workspaces.find((w) => w.id === selected.currentWorkspaceID) ??
      null)
    : snapshot.workspace;
  const viewSnapshot = selected ? { ...snapshot, workspace: ws } : snapshot;
  const coreOf = (id?: string) =>
    id
      ? remoteCore(id)
      : {
          resolveApproval,
          selectWorkspace,
          pauseWorkspace,
          resumeWorkspace,
          cancelTask,
          acceptChangeSet,
          rollbackChangeSet,
        };
  // One source per machine: the screen refetches when it changes, and it
  // must not change on every poll.
  const memory = useMemo(() => memorySource(machineID), [machineID]);
  // True from the click until the Core has answered and the next poll has
  // landed: the rail shows the mark that long, not just for the request.
  const [switching, setSwitching] = useState(false);
  const chooseMachine = useCallback(
    (id: string) => {
      setMachineID(id);
      choosing.current++;
      setSwitching(true);
      void act(() => selectMachine(id), t("shell.errMachine")).finally(() => {
        choosing.current--;
        if (choosing.current === 0) setSwitching(false);
      });
    },
    [act],
  );

  const onChooseWorkspace = useCallback(() => {
    if (machineID) {
      setSheet("folder");
      return Promise.resolve(null);
    }
    return act(
      async () => void (await addWorkspace()),
      t("shell.errAddFolder"),
    );
  }, [act, machineID]);
  const onTogglePause = useCallback(() => {
    if (!ws) return;
    const core = coreOf(machineID);
    void act(
      () =>
        ws.status === "paused"
          ? core.resumeWorkspace(ws.id)
          : core.pauseWorkspace(ws.id),
      t("shell.errLaneState"),
    );
  }, [act, ws, machineID]);
  const decide = useCallback(
    (id: string, approved: boolean) => {
      const a = snapshot.approvals.find((p) => p.change_set_id === id);
      void act(
        () => coreOf(a?.machine_id).resolveApproval(id, approved),
        t(approved ? "shell.errApprove" : "shell.errReject"),
      );
    },
    [act, snapshot.approvals],
  );
  const onApprove = useCallback((id: string) => decide(id, true), [decide]);
  const onReject = useCallback((id: string) => decide(id, false), [decide]);
  const onStopTask = useCallback(
    (id: string) => {
      const task = tasks.find((k) => k.task_id === id);
      void act(
        async () => void (await coreOf(task?.machine_id).cancelTask(id)),
        t("shell.errGate"),
      );
    },
    [act, tasks],
  );
  const onAccept = useCallback(
    (id: string) =>
      void act(async () => {
        const set = snapshot.changeSets.find((c) => c.id === id);
        if (!set) return;
        await coreOf(set.machine_id).acceptChangeSet(set.workspace_id, id);
      }, t("shell.errAccept")),
    [act, snapshot.changeSets],
  );
  const onRollback = useCallback(
    (id: string) =>
      void act(async () => {
        const set = snapshot.changeSets.find((c) => c.id === id);
        if (!set) return;
        const res = await coreOf(set.machine_id).rollbackChangeSet(
          set.workspace_id,
          id,
        );
        if (res.status !== "applied" && res.status !== "rolled_back") {
          throw new Error(
            res.reason ||
              res.conflict?.reason ||
              t("shell.errRollbackIncomplete"),
          );
        }
      }, t("shell.errRollback")),
    [act, snapshot.changeSets],
  );

  if (firstRun === "on") {
    return (
      <OnboardingScreen
        workspace={snapshot.workspace}
        sources={snapshot.sources}
        onChooseFolder={async () => {
          const picked = await addWorkspace();
          if (picked) await refresh();
          return picked;
        }}
        onTestWrite={async (id) => {
          const outcome = await runFirstWrite(id, translator);
          await refresh();
          return outcome;
        }}
        onFinish={(next) => {
          setFirstRun("off");
          setScreen(next === "connect" ? "settings" : "lane");
        }}
      />
    );
  }

  const body = (() => {
    // Before the first poll settles nothing is known. Saying "Fylane is not
    // running" here would be a guess, and the wrong one most of the time.
    if (firstRun === "unknown") {
      return (
        <div
          style={{
            minHeight: "100%",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
          }}
        >
          <Jelly size={40} busyLabel={t("nav.lane")} />
        </div>
      );
    }
    switch (screen) {
      case "lane":
        return (
          <LaneScreen
            snapshot={viewSnapshot}
            tasks={tasks}
            workspaces={viewWorkspaces.filter(
              (w): w is NonNullable<typeof w> => w !== null,
            )}
            canStop={canStopTasks}
            onApprove={onApprove}
            onReject={onReject}
            onSelectWorkspace={(id) =>
              void act(
                () => coreOf(machineID).selectWorkspace(id),
                t("shell.errSwitchWorkspace"),
              )
            }
            onChooseWorkspace={() => void onChooseWorkspace()}
            onOpenDir={(path) =>
              void act(() => openWorkspaceDir(path), t("shell.errOpenDir"))
            }
            onStopTask={onStopTask}
            onTogglePause={onTogglePause}
            onStartCore={() =>
              void act(() => startCore(), t("shell.errStartCore"))
            }
            onGotoTasks={() => setScreen("tasks")}
            machines={machines}
            machineID={machineID}
            switchingMachine={switching}
            onSelectMachine={chooseMachine}
            onAddMachine={() => {
              setEditing(null);
              setSheet("machine");
            }}
            onEditMachine={(id) => {
              const m = machines.find((k) => k.info.id === id);
              if (!m) return;
              setEditing(m.info);
              setSheet("machine");
            }}
            onRemoveMachine={(id) =>
              void act(async () => {
                await removeMachine(id);
                if (id === machineID) chooseMachine("");
              }, t("shell.errMachine"))
            }
            onInstallMachine={(id) =>
              void act(() => installMachine(id), t("shell.errMachine"))
            }
            onReconnectMachine={(id) =>
              void act(() => connectMachine(id), t("shell.errMachine"))
            }
            onDisconnectMachine={(id) =>
              void act(() => disconnectMachine(id), t("shell.errMachine"))
            }
          />
        );
      case "tasks":
        return (
          <TasksScreen
            tasks={tasks}
            changeSets={snapshot.changeSets}
            workspace={ws}
            now={now}
            canStop={canStopTasks}
            onCancel={onStopTask}
            onRollback={onRollback}
            onAccept={onAccept}
            onCopy={(text) => void copyText(text)}
            onGotoLane={() => setScreen("lane")}
          />
        );
      case "memory":
        return (
          <MemoryScreen
            workspace={ws}
            machine={selected?.info.name ?? ""}
            source={memory}
            changeSets={snapshot.changeSets}
            tasks={tasks}
            now={now}
            onError={(message) => setError(`${t("shell.errMemory")}: ${message}`)}
            onGotoLane={() => setScreen("lane")}
            onGotoTasks={() => setScreen("tasks")}
            onHelp={() => void openURL(MEMORY_HELP_URL)}
          />
        );
      case "settings":
        return (
          <SettingsScreen
            online={snapshot.online}
            lang={lang}
            onLang={onLang}
            theme={theme}
            onTheme={chooseTheme}
            workspaces={workspaces.filter(
              (w): w is NonNullable<typeof w> => w !== null,
            )}
            machines={machines}
            recordCount={tasks.length + snapshot.changeSets.length}
            onClearRecords={() =>
              void act(
                async () => setTasks(await clearTasks()),
                t("shell.errClearRecords"),
              )
            }
            undoCount={
              snapshot.changeSets.filter((c) => canRollback(c, now)).length
            }
            onClearBackups={() =>
              act(() => clearBackups(ws?.id ?? ""), t("shell.errClearBackups"))
            }
            onError={setError}
            version={snapshot.status?.version ?? ""}
            update={updateNotice(snapshot.status)}
          />
        );
    }
  })();

  const held = snapshot.approvals.length;
  // What the title bar says. It is the one line the window keeps up there, so
  // it names the state the user would want to know without switching page.
  const chrome = !snapshot.online
    ? { dot: "var(--fy-faint)", title: t("shell.titleOffline") }
    : held > 0
      ? { dot: "var(--fy-amber)", title: tn("shell.titleHeld", held) }
      : ws?.status === "paused"
        ? { dot: "var(--fy-faint)", title: t("shell.titlePaused") }
        : tasks.some((task) => task.state === "running")
          ? { dot: "var(--fy-ink)", title: t("shell.titleRunning") }
          : { dot: "var(--fy-sage)", title: t("shell.titleCalm") };

  return (
    <div
      style={{
        position: "relative",
        width: "100%",
        height: "100%",
        display: "flex",
        flexDirection: "column",
        background: "var(--fy-bg)",
        overflow: "hidden",
      }}
    >
      {/* Title bar (Desktop v2 §2). One row: window decoration, the three
          page names, then the two window-wide actions. It is also the drag
          handle. On macOS the traffic lights are the system's own and float
          over this row, so the left side reserves room for them; on Windows
          the app draws the mark and the caption buttons itself. */}
      <div className="fy-titlebar fy-drag">
        {os === "windows" ? (
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              padding: "0 14px",
              flex: "none",
            }}
          >
            <span className="fy-winmark" aria-hidden="true">
              <i />
              <i />
            </span>
            <span style={{ fontSize: 12.5, color: "var(--fy-muted)" }}>
              Fylane
            </span>
          </div>
        ) : (
          <span style={{ flex: "none", width: 86 }} />
        )}

        <span style={{ flex: 1 }} />

        {/* Fylane-V3 §chrome: the top row says what the window is currently
            about, and nothing else. Navigation is the Node dock. */}
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span
            className="fy-dot fy-dot-sm"
            style={{ background: chrome.dot }}
          />
          <span
            style={{
              fontSize: 11.5,
              color: "var(--fy-muted)",
              letterSpacing: ".01em",
            }}
          >
            {chrome.title}
          </span>
        </div>

        <span style={{ flex: 1 }} />

        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            paddingRight: os === "windows" ? 8 : 16,
          }}
        >
          <button
            type="button"
            className="fy-headbtn"
            style={{
              color:
                ws?.status === "paused" ? "var(--fy-amber)" : "var(--fy-muted)",
            }}
            onClick={onTogglePause}
            disabled={!ws}
          >
            {ws?.status === "paused" ? t("shell.resume") : t("shell.pause")}
          </button>
          <button
            type="button"
            className="fy-palettebtn"
            onClick={() => setCommandsOpen(true)}
          >
            {t("shell.actions")}
            <span
              style={{
                font: `500 10.5px/1 var(--fy-mono)`,
                letterSpacing: ".04em",
              }}
            >
              {os === "windows" ? "Ctrl K" : "\u2318K"}
            </span>
          </button>
        </div>

        {os === "windows" && (
          <div style={{ display: "flex", flex: "none" }}>
            <button
              type="button"
              className="fy-caption"
              aria-label={t("shell.minimise")}
              onClick={() => WindowMinimise()}
            >
              <span
                style={{ width: 11, height: 1, background: "currentColor" }}
              />
            </button>
            <button
              type="button"
              className="fy-caption"
              aria-label={t("shell.maximise")}
              onClick={() => WindowToggleMaximise()}
            >
              <span
                style={{
                  width: 9,
                  height: 9,
                  border: "1px solid currentColor",
                  borderRadius: 1,
                }}
              />
            </button>
            {/* Closing hides the window; the Core keeps running in the tray. */}
            <button
              type="button"
              className="fy-caption fy-caption-close"
              aria-label={t("shell.close")}
              onClick={() => WindowHide()}
            >
              <span className="fy-x" />
            </button>
          </div>
        )}
      </div>

      {/* paddingTop clears the title bar, which now floats over this box.
          It scrolls with the content, which is what puts the page under the
          glass instead of stopping at its edge. */}
      <div
        style={{
          flex: 1,
          minHeight: 0,
          position: "relative",
          overflowY: "auto",
          paddingTop: 46,
        }}
      >
        {body}
      </div>

      {firstRun !== "unknown" && (
        <Dock
          pages={NAV}
          current={screen}
          onGoto={setScreen}
          pending={held > 0}
        />
      )}

      {error && (
        <div role="alert" className="fy-toast">
          {error}
          <button
            type="button"
            className="fy-textbtn"
            onClick={() => setError("")}
          >
            {t("shell.dismiss")}
          </button>
        </div>
      )}

      {commandsOpen && (
        <CommandPalette
          snapshot={snapshot}
          tasks={tasks}
          canStop={canStopTasks}
          onGoto={(s) => {
            setScreen(s);
            setCommandsOpen(false);
          }}
          onClose={() => setCommandsOpen(false)}
          onApprove={(id) => {
            setCommandsOpen(false);
            onApprove(id);
          }}
          onStopTask={(id) => {
            setCommandsOpen(false);
            onStopTask(id);
          }}
          onTogglePause={() => {
            setCommandsOpen(false);
            onTogglePause();
          }}
          onChooseWorkspace={() => {
            setCommandsOpen(false);
            void onChooseWorkspace();
          }}
          onStartCore={() => {
            setCommandsOpen(false);
            void act(() => startCore(), t("shell.errStartCore"));
          }}
        />
      )}

      {sheet === "machine" && (
        <AddMachineSheet
          editing={editing ?? undefined}
          probe={probeMachine}
          onCancel={() => {
            setSheet("none");
            setEditing(null);
          }}
          onSubmit={async (m) => {
            const saved = editing
              ? await updateMachine({ id: editing.id, ...m })
              : await addMachine(m);
            setSheet("none");
            setEditing(null);
            chooseMachine(saved.id);
            await refresh();
          }}
        />
      )}

      {sheet === "folder" && selected && (
        <RemoteFolderSheet
          machine={selected.info}
          browse={(path) => browseMachine(selected.info.id, path)}
          onCancel={() => setSheet("none")}
          onSubmit={async (path) => {
            const core = remoteCore(selected.info.id);
            const added = await core.addWorkspace(path);
            await core.selectWorkspace(added.id);
            setSheet("none");
            await refresh();
          }}
        />
      )}

      {claims.length > 0 && (
        <PairClaimSheet
          claim={claims[0]}
          onResolve={(approved) =>
            void act(
              () => resolvePairClaim(claims[0].request_id, approved),
              t("shell.errPairing"),
            )
          }
        />
      )}
    </div>
  );
}
