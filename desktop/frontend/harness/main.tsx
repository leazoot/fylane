import React from "react";
import { createRoot } from "react-dom/client";
import "../src/style.css";
import { LaneScreen } from "../src/screens/Lane";
import { TasksScreen } from "../src/screens/Tasks";
import { MemoryScreen } from "../src/screens/Memory";
import { SettingsScreen } from "../src/screens/Settings";
import { OnboardingScreen, type FirstRunStep } from "../src/screens/Onboarding";
import { CommandPalette } from "../src/components/CommandPalette";
import { Jelly } from "../src/components/Jelly";
import { Dock } from "../src/components/Dock";
import { PairClaimSheet } from "../src/components/PairClaimSheet";
import { AddMachineSheet } from "../src/components/AddMachineSheet";
import { RemoteFolderSheet } from "../src/components/RemoteFolderSheet";
import { translatorFor, type Key } from "../src/lib/i18n";
import type { Approval } from "../src/lib/core";
import * as fx from "./fixtures";

// Design-fidelity harness: renders one screen with fixture data so a
// screenshot can be diffed against its design board.
// Development only — it is never part of the shipped bundle.

// The boards are drawn in English, so the labels come from the dictionary
// under a fixed translator rather than being spelled out again here — a
// renamed screen has to reach the boards too.
const en = translatorFor("en");

// Desktop v2 §1 said three pages; board 17 added the fourth.
const NAV: { key: string; label: Key }[] = [
  { key: "lane", label: "nav.lane" },
  { key: "tasks", label: "nav.tasks" },
  { key: "memory", label: "nav.memory" },
  { key: "settings", label: "nav.settings" },
];

const params = new URLSearchParams(location.search);
const board = params.get("board") ?? "lane";

// Density is remembered per machine, so a board that wants the compact list
// has to seed the same key the screen reads. `?density=compact` next to the
// board name, the way `?theme=dark` works.
const density = params.get("density");
if (density === "compact" || density === "comfortable") {
  try {
    window.localStorage.setItem("fylane.density", density);
  } catch {
    // A profile that refuses storage just gets the default board.
  }
}
const noop = () => {};

// Both themes are boards (12–14), so the harness stamps the same attribute
// the shell does: `?theme=dark` next to the board name.
const theme = params.get("theme");
if (theme === "dark" || theme === "light") {
  document.documentElement.setAttribute("data-fy", theme);
}

// The title bar the window really draws, minus the platform branch: the
// boards are all macOS, so the left edge is the traffic-light reserve.
/** Which page the board is standing on, for the dock's current-item mark. */
function active(): string {
  if (board.startsWith("tasks")) return "tasks";
  if (board.startsWith("memory")) return "memory";
  if (board.startsWith("settings")) return "settings";
  return "lane";
}

function Header({ active }: { active: string }) {
  return (
    <div className="fy-titlebar">
      <span style={{ flex: "none", width: 86 }} />
      <span style={{ flex: 1 }} />
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span
          className="fy-dot fy-dot-sm"
          style={{ background: "var(--fy-sage)" }}
        />
        <span
          style={{
            fontSize: 11.5,
            color: "var(--fy-muted)",
            letterSpacing: ".01em",
          }}
        >
          {en.t("shell.titleCalm")}
        </span>
      </div>
      <span style={{ flex: 1 }} />
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          paddingRight: 16,
        }}
      >
        <span className="fy-headbtn">{en.t("shell.pause")}</span>
        <span className="fy-palettebtn">
          {en.t("shell.actions")}
          <span
            style={{
              font: "500 10.5px/1 var(--fy-mono)",
              letterSpacing: ".04em",
            }}
          >
            {"⌘K"}
          </span>
        </span>
      </div>
    </div>
  );
}

// The window floats the title bar over the page and pads the scroller to
// match (see App.tsx). A board drawn without that padding gets its own page
// head clipped, which is not what the board says.
function Scroller({ children }: { children: React.ReactNode }) {
  return (
    <div
      style={{
        flex: 1,
        minHeight: 0,
        position: "relative",
        overflowY: "auto",
        paddingTop: 46,
      }}
    >
      {children}
    </div>
  );
}

function laneSnapshot() {
  const held: Record<string, Approval[]> = {
    "lane-held": fx.HELD,
    "lane-command": fx.HELD_COMMAND,
    "lane-disclosure": fx.HELD_DISCLOSURE,
  };
  if (held[board]) {
    return fx.snapshot({
      approvals: held[board],
      status: { ...fx.STATUS, pending_approvals: 1 },
    });
  }
  if (board === "lane-empty") {
    return fx.snapshot({
      sources: fx.SOURCES.map((s) => ({ ...s, connected: false })),
      changeSets: [],
    });
  }
  if (board === "lane-paused") {
    return fx.snapshot({
      workspace: { ...fx.WORKSPACES[0], status: "paused" },
    });
  }
  if (board === "lane-remote") {
    return fx.snapshot({ workspace: fx.MACHINES[0].workspaces[0] });
  }
  if (board === "lane-remote-held") {
    return fx.snapshot({
      workspace: fx.MACHINES[0].workspaces[0],
      approvals: fx.HELD_REMOTE,
      status: { ...fx.STATUS, pending_approvals: 1 },
    });
  }
  if (board === "lane-remote-missing") {
    return fx.snapshot({ workspace: null });
  }
  if (board === "lane-offline") {
    return {
      online: false,
      status: null,
      workspace: null,
      approvals: [],
      changeSets: [],
      sources: [],
    };
  }
  return fx.snapshot();
}

// The machine boards stand the rail on a remote machine: vps-1 (online, with
// its own folders), or build-box (reachable, no Fylane yet). Every other
// lane board stands on this computer with the two machines in the list.
function machineOf(): string {
  if (board === "lane-remote-missing") return "m_build";
  if (board.startsWith("lane-remote")) return "m_vps1";
  return "";
}

function Lane({ tasks = fx.TASKS }: { tasks?: typeof fx.TASKS }) {
  const machine = machineOf();
  return (
    <LaneScreen
      snapshot={laneSnapshot()}
      tasks={
        board === "lane-remote-missing"
          ? []
          : board.startsWith("lane-remote")
            ? fx.TASKS_REMOTE
            : tasks
      }
      workspaces={
        machine === "m_vps1"
          ? fx.MACHINES[0].workspaces
          : machine
            ? []
            : fx.WORKSPACES
      }
      machines={fx.MACHINES}
      machineID={machine}
      onSelectMachine={noop}
      onAddMachine={noop}
      onRemoveMachine={noop}
      onInstallMachine={noop}
      onReconnectMachine={noop}
      onDisconnectMachine={noop}
      canStop
      onApprove={noop}
      onReject={noop}
      onSelectWorkspace={noop}
      onChooseWorkspace={noop}
      onOpenDir={noop}
      onStopTask={noop}
      onTogglePause={noop}
      onStartCore={noop}
      onGotoTasks={noop}
    />
  );
}

function Board() {
  switch (board) {
    case "lane":
    case "lane-held":
    case "lane-command":
    case "lane-disclosure":
    case "lane-empty":
    case "lane-paused":
    case "lane-offline":
    case "lane-remote":
    case "lane-remote-held":
    case "lane-remote-missing":
      return (
        <>
          <Header active="lane" />
          <Scroller>
            <Lane />
          </Scroller>
        </>
      );
    case "machine-add":
    case "machine-edit":
      // The sheet over the lane. The probe answers after a beat so the
      // knock, the gate opening and the sentence can all be seen.
      return (
        <>
          <Header active="lane" />
          <Scroller>
            <Lane />
          </Scroller>
          <AddMachineSheet
            editing={
              board === "machine-edit"
                ? { ...fx.MACHINES[0].info, state: "error", reason: "auth" }
                : undefined
            }
            probe={async () => {
              await fx.wait(1200);
              return {
                reachable: true,
                running: true,
                compatible: true,
                version: "0.0.4",
              };
            }}
            onSubmit={async () => {}}
            onCancel={noop}
          />
        </>
      );
    case "machine-folder":
      // The folder sheet over the lane, walking a small fake tree; each
      // answer takes a beat so the reading state can be seen.
      return (
        <>
          <Header active="lane" />
          <Scroller>
            <Lane />
          </Scroller>
          <RemoteFolderSheet
            machine={fx.MACHINES[0].info}
            browse={async (path) => {
              await fx.wait(500);
              return fx.remoteListing(path);
            }}
            onSubmit={async () => {}}
            onCancel={noop}
          />
        </>
      );
    case "tasks":
    case "tasks-empty":
    case "tasks-remote":
      return (
        <>
          <Header active="tasks" />
          <Scroller>
            <TasksScreen
              tasks={
                board === "tasks-empty"
                  ? []
                  : board === "tasks-remote"
                    ? fx.TASKS_REMOTE
                    : fx.TASKS
              }
              changeSets={board === "tasks-empty" ? [] : fx.CHANGE_SETS}
              workspace={fx.WORKSPACES[0]}
              now={fx.NOW}
              canStop
              onCancel={noop}
              onRollback={noop}
              onAccept={noop}
              onCopy={noop}
              onGotoLane={noop}
            />
          </Scroller>
        </>
      );
    case "memory":
    case "memory-empty":
    case "memory-remote":
      return (
        <>
          <Header active="memory" />
          <Scroller>
            <MemoryScreen
              workspace={fx.WORKSPACES[0]}
              machine={board === "memory-remote" ? fx.MACHINES[0].info.name : ""}
              source={fx.memorySource(board === "memory-empty" ? fx.MEMORY_EMPTY : fx.MEMORY_DOC)}
              changeSets={fx.CHANGE_SETS}
              tasks={fx.TASKS}
              now={fx.NOW}
              onError={noop}
              onGotoLane={noop}
              onGotoTasks={noop}
              onHelp={noop}
            />
          </Scroller>
        </>
      );
    case "settings":
    case "settings-update":
      return (
        <>
          <Header active="settings" />
          <Scroller>
            <SettingsScreen
              lang="zh"
              onLang={noop}
              theme="light"
              onTheme={noop}
              workspaces={fx.WORKSPACES}
              machines={fx.MACHINES}
              undoCount={2}
              onClearBackups={async () => ({ cleared: 2, backups_removed: 2 })}
              recordCount={fx.TASKS.length + fx.CHANGE_SETS.length}
              onClearRecords={noop}
              onError={noop}
              version={fx.STATUS.version}
              update={
                board === "settings-update" ? { version: "0.0.2" } : undefined
              }
              deps={fx.SETTINGS_DEPS}
            />
          </Scroller>
        </>
      );
    case "commands":
      return (
        <>
          <Header active="lane" />
          <div style={{ flex: 1, minHeight: 0, position: "relative" }}>
            <Lane />
            <CommandPalette
              snapshot={fx.snapshot()}
              tasks={fx.TASKS}
              canStop
              onGoto={noop}
              onClose={noop}
              onApprove={noop}
              onStopTask={noop}
              onTogglePause={noop}
              onChooseWorkspace={noop}
              onStartCore={noop}
            />
          </div>
        </>
      );
    case "pairing":
      return (
        <>
          <Header active="lane" />
          <div style={{ flex: 1, minHeight: 0, position: "relative" }}>
            <Lane />
            <PairClaimSheet claim={fx.CLAIM} onResolve={noop} />
          </div>
        </>
      );
    // First run (Fylane-V3). Four steps; the board names the one to draw.
    case "first-run":
    case "first-run-write":
    case "first-run-rung":
    case "first-run-done": {
      const step: FirstRunStep = board.endsWith("-write")
        ? "write"
        : board.endsWith("-rung")
          ? "rung"
          : board.endsWith("-done")
            ? "done"
            : "folder";
      return (
        <OnboardingScreen
          step={step}
          workspace={step === "folder" ? null : fx.WORKSPACES[1]}
          sources={step === "done" ? fx.ALL_SOURCES : fx.NO_SOURCES}
          onChooseFolder={fx.pickFolder}
          onTestWrite={fx.testWrite}
          onSetRung={async () => {}}
          onFinish={noop}
        />
      );
    }
    default:
      return <div style={{ padding: 40 }}>unknown board “{board}”</div>;
  }
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
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
      <Board />
      <Dock
        pages={NAV}
        current={active()}
        onGoto={noop}
        pending={board === "lane-held"}
      />
    </div>
  </React.StrictMode>,
);
