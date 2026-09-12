// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { DICT, LangContext } from "../lib/i18n";
import type {
  Approval,
  ChangeSet,
  CommandSettingsInfo,
  ConnectInfo,
  CoreStatusInfo,
  MemoryDoc,
  MemoryQuery,
  MemorySource,
  PrefsInfo,
  Source,
  TaskInfo,
  Workspace,
} from "../lib/core";
import { OFFLINE_SNAPSHOT, type LaneSnapshot } from "../lib/lane";
import { LaneScreen } from "./Lane";
import { TasksScreen } from "./Tasks";
import { MemoryScreen } from "./Memory";
import { SettingsScreen, type SettingsDeps } from "./Settings";
import { OnboardingScreen } from "./Onboarding";
import { PairClaimSheet } from "../components/PairClaimSheet";
import { Dock } from "../components/Dock";
import type { MachineView } from "../lib/poll";

// The rendering layer, pinned.
//
// Every user-visible defect this cycle was invisible to the tests that existed:
// a read failure worded as a write failure, a timeout control still clickable
// after the read that would fill it had failed, and a green "in effect" beside
// a lane nothing could reach. All three are only true once something is drawn,
// which is why they had to be found by hand on a real machine.
//
// jsdom, `act`, and `react-dom/client` directly. No component-testing
// library: the deleted extension's popup tests set that precedent and there
// is no reason to add a dependency now that this is the only front end.

const WS: Workspace = {
  id: "ws_1",
  name: "ai-workspace",
  mode: "read_write",
  exclude_rules: [],
  sensitive_rules: [],
  status: "active",
  created_at: "2026-08-13T09:00:00",
  root_path: "/Users/you/Projects/ai-workspace",
  availability: "available",
};

function snap(over: Partial<LaneSnapshot> = {}): LaneSnapshot {
  return { ...OFFLINE_SNAPSHOT, online: true, workspace: WS, ...over };
}

function task(over: Partial<TaskInfo> = {}): TaskInfo {
  return {
    task_id: "tsk_1",
    state: "succeeded",
    label: "go test ./...",
    exit_code: 0,
    started_at: "2026-08-13T09:58:00",
    duration: 8_400_000_000,
    ...over,
  };
}

function approval(over: Partial<Approval> = {}): Approval {
  return {
    change_set_id: "chg_0000000000000001",
    workspace_id: "ws_1",
    workspace_name: "ai-workspace",
    provider: "claude",
    summary: "Update authentication handling",
    created_at: "2026-08-13T10:29:00",
    operations: [{ type: "update", path: "src/auth.ts" }],
    kind: "write",
    ...over,
  };
}

const STATUS: CoreStatusInfo = {
  version: "0.0.1",
  pending_approvals: 0,
  tunnel: "connected",
  approval_mode: "safe",
};

const PREFS: PrefsInfo = {
  autostart: { enabled: false, supported: true },
  read_boundary: { state: "enforced", detail: "subprocess reads are bounded" },
  task_timeout_seconds: 60,
  allow_stop_tasks: true,
};

function connect(over: Partial<ConnectInfo> = {}): ConnectInfo {
  return { mode: "relay", state: "", providers: [], ...over };
}

// ── harness ────────────────────────────────────────────────────────────────

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  document.body.innerHTML = "";
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

function draw(node: React.ReactNode, lang: "en" | "zh" = "en") {
  act(() => {
    root.render(
      <LangContext.Provider value={{ lang, setLang: () => {} }}>
        {node}
      </LangContext.Provider>,
    );
  });
}

/** Lets the effects that read the Core settle before anything is asserted. */
async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

const text = () => host.textContent ?? "";
const buttons = () => Array.from(host.querySelectorAll("button"));
const button = (label: string) =>
  buttons().find((b) => (b.textContent ?? "").trim() === label);

/** A rung row carries its own explanation, so it is found by its label rather
 *  than by the row's whole text. */
const rung = (label: string) =>
  buttons().find(
    (b) => b.querySelector(".fy-policy-label")?.textContent?.trim() === label,
  );

function click(el: Element | undefined) {
  if (!el) throw new Error("nothing to click");
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

const laneProps = {
  workspaces: [WS],
  canStop: true,
  onApprove: () => {},
  onReject: () => {},
  onSelectWorkspace: () => {},
  onChooseWorkspace: () => {},
  onOpenDir: () => {},
  onStopTask: () => {},
  onTogglePause: () => {},
  onStartCore: () => {},
  onGotoTasks: () => {},
};

const settingsProps = {
  lang: "en" as const,
  onLang: () => {},
  theme: "auto" as const,
  onTheme: () => {},
  workspaces: [WS],
  recordCount: 3,
  onClearRecords: () => {},
  undoCount: 0,
  onClearBackups: async () => null,
  onError: () => {},
  version: "0.0.1",
};

function deps(over: Partial<SettingsDeps> = {}): SettingsDeps {
  return {
    prefs: async () => PREFS,
    save: async () => PREFS,
    status: async () => STATUS,
    setMode: async () => ({ approval_mode: "safe" as const }),
    dock: async () => ({ supported: true, hidden: false }),
    setDock: async (hidden: boolean) => ({ supported: true, hidden }),
    connect: async () => connect(),
    commands: async () => ({ rung: "workspace", grants: [] }),
    setRung: async () => ({ rung: "workspace", grants: [] }),
    revokeGrant: async () => ({ rung: "workspace", grants: [] }),
    setNetwork: async () => ({ workspaces: [], currentWorkspaceID: "" }),
    startSetup: async () => connect(),
    cancelSetup: async () => connect(),
    startDownload: async () => connect(),
    cancelDownload: async () => connect(),
    signOut: async () => connect(),
    mintCode: async () => ({ code: "7K4M-2QB9", expires_in_seconds: 600 }),
    remote: () => {
      throw new Error("no remote machines in this test");
    },
    ...over,
  };
}

describe("forwarded MCP providers on the settings page", () => {
  // The gap this closes: a provider marked trust: workspace runs tools
  // Fylane cannot inspect without stopping to ask, and the only place that
  // said so was a line in the start-up log. An authorization is only safe
  // while it is visible, and a log nobody opens is not visible.
  it("shows a provider that does not ask, and says what that means", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps({
          commands: async () => ({
            rung: "workspace",
            grants: [],
            providers: [
              { name: "sqlite", trust: "workspace" },
              { name: "docs", trust: "ask" },
            ],
          }),
        })}
      />,
    );
    await settle();
    const text = host.textContent ?? "";
    expect(text).toContain("sqlite");
    expect(text).toContain("docs");
    // The warning names the one that does not ask, and not the one that does.
    const warn = host.querySelector(".fy-warn");
    expect(warn).not.toBeNull();
    expect(warn?.textContent).toContain("sqlite");
    expect(warn?.textContent).not.toContain("docs");
  });

  it("says nothing at all when every provider asks", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps({
          commands: async () => ({
            rung: "workspace",
            grants: [],
            providers: [{ name: "docs", trust: "ask" }],
          }),
        })}
      />,
    );
    await settle();
    expect(host.textContent).toContain("docs");
    // Nothing is in force that was not asked for, so there is no warning to
    // raise — and a warning that is always on screen stops being one.
    expect(host.querySelector(".fy-warn")).toBeNull();
  });

  it("draws no section on a machine with no providers configured", async () => {
    // There is no board for this list. Drawing an empty one would be adding a
    // row to the design whose only content is the word "none".
    draw(<SettingsScreen {...settingsProps} undoCount={0} deps={deps()} />);
    await settle();
    expect(host.textContent).not.toContain(DICT["set.proxies"].en);
  });

  it("offers no way to add, change or remove one", async () => {
    // Read-only is the decision, not an omission: a provider is a program to
    // run, and naming one has to stay something that happens in a file on
    // this machine.
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps({
          commands: async () => ({
            rung: "workspace",
            grants: [],
            providers: [{ name: "sqlite", trust: "workspace" }],
          }),
        })}
      />,
    );
    await settle();
    const touching = Array.from(
      host.querySelectorAll<HTMLElement>("button, input, [role='button']"),
    ).filter((el) =>
      (el.getAttribute("aria-label") || el.textContent || "").includes(
        "sqlite",
      ),
    );
    expect(touching).toEqual([]);
  });
});

// ── the three defects that reached a user ──────────────────────────────────

describe("settings, after a read that failed", () => {
  it("says it could not read, not that it could not change", async () => {
    // Opening a page changes nothing. Naming an edit the user never made sends
    // them looking for a switch they did not touch.
    const said: string[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        onError={(m) => said.push(m)}
        deps={deps({ prefs: async () => Promise.reject(new Error("nope")) })}
      />,
    );
    await settle();
    expect(said).toContain("Could not read the settings.");
    expect(said.join(" ")).not.toContain("Could not change");
  });

  it("does not leave the timeout stepper clickable", async () => {
    // The defect: the switch beside it went disabled and the stepper did not,
    // so the one control that could only fail was the one still offering to be
    // pressed.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ prefs: async () => Promise.reject(new Error("nope")) })}
      />,
    );
    await settle();
    const stepper = host.querySelector(".fy-stepper");
    expect(stepper).not.toBeNull();
    expect(stepper?.getAttribute("data-unknown")).toBe("true");
    expect(stepper?.querySelector("output")?.textContent).toBe("—");
    for (const b of Array.from(stepper!.querySelectorAll("button"))) {
      expect(b.disabled).toBe(true);
    }
  });
});

describe("the connected-AI rail", () => {
  // Both states used to be filled dots a few percent apart in lightness — the
  // same dot to anyone not comparing them side by side (user report,
  // 2026-08-30). Shape now carries the difference, and motion is reserved for
  // the one thing that is actually happening.
  const rail = (busy: "none" | "asking" | "running") => {
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({
          approvals:
            busy === "asking" ? [approval({ provider: "claude" })] : [],
          sources: [
            { provider: "claude", connected: true, lanes_carried: 1 },
            { provider: "grok", connected: false, lanes_carried: 0 },
          ],
        })}
        tasks={
          busy === "running"
            ? [
                task({
                  state: "running",
                  provider: "claude",
                  label: "sleep 60",
                }),
              ]
            : []
        }
      />,
    );
    return Array.from(host.querySelectorAll(".fy-dot-sm"));
  };

  it("separates connected from not connected by shape, not by shade", () => {
    const dots = rail("none");
    expect(
      dots.filter((d) => d.classList.contains("fy-dot-hollow")),
    ).toHaveLength(1);
    // Never both: a hollow ring that also breathes reads as connecting.
    expect(
      dots.some(
        (d) =>
          d.classList.contains("fy-beat") &&
          d.classList.contains("fy-dot-hollow"),
      ),
    ).toBe(false);
  });

  it("holds still while nothing is being asked of it", () => {
    // Board 04 calls this rail static, and a beat everywhere else in the
    // window means something is happening now. A permanently breathing dot
    // spends that meaning on a state that never changes.
    expect(
      rail("none").filter((d) => d.classList.contains("fy-beat")),
    ).toHaveLength(0);
  });

  it("breathes on the source that is asking, and only that one", () => {
    const beating = rail("asking").filter((d) =>
      d.classList.contains("fy-beat"),
    );
    expect(beating).toHaveLength(1);
    expect(beating[0].classList.contains("fy-dot-hollow")).toBe(false);
  });

  it("breathes while that source has a command running, approval or not", () => {
    // The first cut tied the beat to a pending approval. Under the open rung
    // an ordinary command never raises one, so the rail sat still through the
    // whole run — reported from a real machine, 2026-08-30.
    const beating = rail("running").filter((d) =>
      d.classList.contains("fy-beat"),
    );
    expect(beating).toHaveLength(1);
    // And it says which of the two it is: asking and running are not the
    // same state, and only one of them is waiting on the user.
    expect(text()).toContain("Running");
    expect(text()).not.toContain("Asking");
  });
});

describe("the connection panel", () => {
  it("does not say a way in is in effect when there is no address", async () => {
    // Relay is the default mode even on a machine that never paired with one.
    // Reading "in effect" off the mode put a green dot beside a lane nothing
    // could reach — which is what the real machine was showing.
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    expect(host.querySelector(".fy-sagepill")).toBeNull();
    expect(text()).toContain("No address yet");
  });

  it("says so once there is an address", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          connect: async () => connect({ connector_url: "https://relay/c/x" }),
        })}
      />,
    );
    await settle();
    expect(host.querySelector(".fy-sagepill")).not.toBeNull();
    expect(text()).toContain("https://relay/c/x");
  });

  it("waits beside the panel rather than drawing an empty one", async () => {
    // Never resolves: this is what the section looks like before the Core has
    // answered, and "no address yet" would be a guess at that point.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ connect: () => new Promise(() => {}) })}
      />,
    );
    await settle();
    expect(host.querySelector(".fy-jelly")).not.toBeNull();
    expect(text()).not.toContain("No address yet");
  });
});

// ── the typed-code way in ──────────────────────────────────────────────────
// The platform's connect page offers a pairing code when its loopback claim
// finds no Companion — a browser on another machine. Nothing in this window
// could produce one, so that fallback was a door with no key behind it.

describe("the pairing code", () => {
  const withAddress = {
    connect: async () => connect({ connector_url: "https://relay/c/x" }),
  };

  it("is not minted until it is asked for", async () => {
    // A code minted on render is a code that changes under whoever is typing
    // it into a platform. That is the 2026-08-15 defect, and it invalidated
    // the code the user was in the middle of using.
    let mints = 0;
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          ...withAddress,
          mintCode: async () => {
            mints++;
            return { code: "7K4M-2QB9", expires_in_seconds: 600 };
          },
        })}
      />,
    );
    await settle();
    expect(mints).toBe(0);
    expect(text()).not.toContain("7K4M-2QB9");
  });

  it("shows the code and how long it has left once asked", async () => {
    draw(<SettingsScreen {...settingsProps} deps={deps(withAddress)} />);
    await settle();
    click(button("Show a code"));
    await settle();
    expect(text()).toContain("7K4M-2QB9");
    expect(text()).toContain("10:00 left");
  });

  it("cannot be asked for when nothing can reach this machine", async () => {
    // The Core refuses without a public address. Offering the button would
    // only produce an error the row already knows how to avoid.
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    const show = button("Show a code");
    expect(show).toBeDefined();
    expect(show!.disabled).toBe(true);
  });

  it("says what went wrong rather than showing a blank code", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          ...withAddress,
          mintCode: async () => Promise.reject(new Error("relay down")),
        })}
      />,
    );
    await settle();
    click(button("Show a code"));
    await settle();
    expect(text()).toContain("relay down");
    expect(text()).not.toContain("7K4M-2QB9");
  });
});

// ── the setting that decides whether a control exists ───────────────────────

// Density: how much of the list fits on a screen. The one thing it must never
// do is change what the list contains, because two densities that show
// different tasks are two lists and only one of them can be right.
describe("list density", () => {
  const tasksProps = {
    changeSets: [] as ChangeSet[],
    workspace: WS,
    now: new Date("2026-08-13T10:00:00"),
    canStop: true,
    onCancel: () => {},
    onRollback: () => {},
    onAccept: () => {},
    onCopy: () => {},
    onGotoLane: () => {},
  };
  const three = [
    task({ task_id: "a", label: "npm run build" }),
    task({ task_id: "b", label: "cargo test" }),
    task({ task_id: "c", label: "git status" }),
  ];
  const feed = () => host.querySelector(".fy-feed") as HTMLElement | null;

  beforeEach(() => window.localStorage.clear());

  it("opens roomy, which is the density someone gets without finding this control", () => {
    draw(<TasksScreen {...tasksProps} tasks={three} />);
    expect(feed()?.dataset.density).toBe("comfortable");
  });

  it("shows every task in both densities", () => {
    draw(<TasksScreen {...tasksProps} tasks={three} />);
    const roomy = host.querySelectorAll(".fy-trow").length;
    click(button("Compact"));
    expect(host.querySelectorAll(".fy-trow").length).toBe(roomy);
    for (const label of ["npm run build", "cargo test", "git status"]) {
      expect(text()).toContain(label);
    }
  });

  it("stamps the feed so the metrics change and nothing else does", () => {
    draw(<TasksScreen {...tasksProps} tasks={three} />);
    click(button("Compact"));
    expect(feed()?.dataset.density).toBe("compact");
    click(button("Roomy"));
    expect(feed()?.dataset.density).toBe("comfortable");
  });

  it("remembers the choice on this machine", () => {
    draw(<TasksScreen {...tasksProps} tasks={three} />);
    click(button("Compact"));
    expect(window.localStorage.getItem("fylane.density")).toBe("compact");
  });

  it("keeps the filters working after a density change", () => {
    // The two controls sit in the same row and answer different questions;
    // pressing one must not reset the other.
    draw(
      <TasksScreen
        {...tasksProps}
        tasks={[
          ...three,
          task({ task_id: "d", label: "failing", state: "failed" }),
        ]}
      />,
    );
    click(button("Compact"));
    expect(feed()?.dataset.density).toBe("compact");
    expect(host.querySelectorAll(".fy-trow").length).toBe(4);
  });
});

describe("allow stopping a task", () => {
  const running = task({
    task_id: "tsk_run",
    state: "running",
    label: "npm run build",
  });
  const tasksProps = {
    changeSets: [] as ChangeSet[],
    workspace: WS,
    now: new Date("2026-08-13T10:00:00"),
    onCancel: () => {},
    onRollback: () => {},
    onAccept: () => {},
    onCopy: () => {},
    onGotoLane: () => {},
  };

  it("really removes the stop button from a running row when it is off", () => {
    // The walkthrough item that could otherwise only be checked by changing the
    // user's own setting on their own machine.
    draw(<TasksScreen {...tasksProps} tasks={[running]} canStop={false} />);
    expect(button("Stop")).toBeUndefined();
  });

  it("offers it when it is on", () => {
    draw(<TasksScreen {...tasksProps} tasks={[running]} canStop />);
    expect(button("Stop")).toBeDefined();
  });

  it("never offers it on a row that already finished", () => {
    draw(<TasksScreen {...tasksProps} tasks={[task()]} canStop />);
    expect(button("Stop")).toBeUndefined();
  });
});

// ── the lane's own invariants ──────────────────────────────────────────────

describe("the request scene", () => {
  it("sends the decision on the first frame, and only once", () => {
    // The platform is blocked on this answer and must not wait for an
    // animation; a second click must not resolve the same request twice.
    const sent: string[] = [];
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [approval()] })}
        tasks={[]}
        onApprove={(id) => sent.push(id)}
      />,
    );
    const approve = button("Approve");
    click(approve);
    click(approve);
    expect(sent).toEqual(["chg_0000000000000001"]);
  });

  it("asks each of the five kinds its own question", () => {
    const said = new Set<string>();
    for (const kind of [
      "command",
      "write",
      "disclosure",
      "delegation",
      "proxy",
    ] as const) {
      draw(
        <LaneScreen
          {...laneProps}
          snapshot={snap({ approvals: [approval({ kind, command: ["ls"] })] })}
          tasks={[]}
        />,
      );
      said.add(host.querySelector("h1")?.textContent ?? "");
    }
    // Five questions, five sentences. Asking them as one is the defect.
    expect(said.size).toBe(5);
  });

  it("offers a way out when the Core is not running", () => {
    draw(<LaneScreen {...laneProps} snapshot={OFFLINE_SNAPSHOT} tasks={[]} />);
    expect(text()).toContain("Fylane is not running");
    expect(button("Start Fylane")).toBeDefined();
  });

  it("does not call a write an ordinary command", () => {
    // "Ordinary command" is a risk read about a command. There is no such
    // thing as an ordinary write.
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [approval()] })}
        tasks={[]}
      />,
    );
    expect(text()).not.toContain("Ordinary command");
  });
});

// ── the two flags the change list used to drop ─────────────────────────────
//
// The Core marks both on the operation (txn/engine.go) and the wire has
// carried both all along; the V3 rewrite's pendingInfo mapped only op, path
// and diff. So a delete that takes a whole tree rendered as the same line as
// a delete that takes an empty directory, and a write to .env rendered as a
// write to any other file — the two things security.md says must be told
// apart. This is what stops that from silently coming back.

describe("what a change row has to say beyond its path", () => {
  /** The change list lives behind the details toggle. */
  function openChanges(over: Partial<Approval>) {
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [approval(over)] })}
        tasks={[]}
      />,
    );
    click(button("Details"));
  }

  it("says a delete takes the whole tree", () => {
    openChanges({
      summary: "Remove the legacy source",
      operations: [
        { type: "delete", path: "src/legacy", recursive_delete: true },
      ],
    });
    expect(text()).toContain("src/legacy");
    expect(text()).toContain("the whole directory and everything in it");
  });

  it("says nothing of the sort for a delete that takes one empty directory", () => {
    // The claim has to be false when it is false, or it stops being read.
    openChanges({
      operations: [{ type: "delete", path: "src/legacy" }],
    });
    expect(text()).toContain("src/legacy");
    expect(text()).not.toContain("the whole directory");
  });

  it("marks a write to a sensitive file as one", () => {
    openChanges({
      summary: "Update the environment file",
      operations: [{ type: "update", path: ".env", sensitive: true }],
    });
    expect(text()).toContain("sensitive file");
  });

  it("leaves an ordinary write unmarked", () => {
    openChanges({});
    expect(text()).toContain("src/auth.ts");
    expect(text()).not.toContain("sensitive file");
  });

  it("says both when both are true, and does not merge them into one", () => {
    openChanges({
      operations: [
        {
          type: "delete",
          path: "secrets",
          recursive_delete: true,
          sensitive: true,
        },
      ],
    });
    expect(text()).toContain("the whole directory and everything in it");
    expect(text()).toContain("sensitive file");
  });

  it("puts how far the change reaches on the row", () => {
    openChanges({
      operations: [
        { type: "update", path: "src/auth.ts", impact: { callers: 3 } },
      ],
    });
    expect(text()).toContain("3 callers elsewhere");
  });

  it("says nothing about reach when nobody measured it", () => {
    openChanges({ operations: [{ type: "update", path: "src/auth.ts" }] });
    expect(text()).not.toContain("callers");
  });

  it("keeps reach out of the risk colour", () => {
    // Brick means "be careful" on this screen. A widely used function is not
    // thereby dangerous to change, and one colour meaning both would weaken
    // the warning that has to keep working — the same reading once used to
    // keep these notes out of amber.
    openChanges({
      operations: [
        { type: "update", path: "src/auth.ts", impact: { callers: 3 } },
      ],
    });
    const brick = Array.from(host.querySelectorAll<HTMLElement>("span")).filter(
      (el) => (el.getAttribute("style") ?? "").includes("--fy-brick"),
    );
    for (const el of brick) {
      expect(el.textContent ?? "").not.toContain("callers");
    }
  });

  it("shows where a move lands, not only where it starts", () => {
    // The Core marks a move sensitive when either end is, so a row showing
    // only the source can put the sensitive note beside a path that does not
    // look sensitive.
    openChanges({
      operations: [
        {
          type: "move",
          path: "config/local.ts",
          to: ".env",
          sensitive: true,
        },
      ],
    });
    expect(text()).toContain("config/local.ts → .env");
    expect(text()).toContain("sensitive file");
  });

  it("reads as risk, not as a standing condition", () => {
    // fy-warn is the open rung's amber, which has to keep meaning "you are
    // living in this". A row in a list waiting for one answer is not
    // that, and it borrows the brick the metaline already uses on this
    // screen for a rule that stopped something on purpose.
    openChanges({
      operations: [
        { type: "delete", path: "src/legacy", recursive_delete: true },
      ],
    });
    expect(host.querySelector(".fy-warn")).toBeNull();
  });
});

// ── what the product is not allowed to go quiet about ──────────────────────
//
// the rung decides *whether the user is asked*, never whether the
// checks run — and the open rung is never a quiet state. These are the
// walkthrough's own items — the open rung's explicit confirmation block and
// its standing warning, and the high-risk row being a fixed value rather
// than a control — and until now nothing but a person looking could tell.

describe("the open rung", () => {
  it("keeps saying what it is, for as long as it is on", async () => {
    // The user's real machine is on this rung. A rung that runs ordinary
    // commands without asking must not look like any other rung.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ commands: async () => ({ rung: "open", grants: [] }) })}
      />,
    );
    await settle();
    expect(host.querySelector(".fy-warn")).not.toBeNull();
    expect(text()).toContain("Ordinary commands run without asking");
    // And it still says what did not change with it.
    expect(text()).toContain("still ask");
  });

  it("says nothing of the sort on a rung that does ask", async () => {
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    expect(host.querySelector(".fy-warn")).toBeNull();
  });

  it("asks before switching to it, and does not tell the Core until answered", async () => {
    // The Core refuses the open rung without an acknowledgement, so a click
    // that went straight through would fail silently.
    const asked: { rung: string; confirm: boolean }[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setRung: async (rung, confirm) => {
            asked.push({ rung, confirm });
            return { rung, grants: [] };
          },
        })}
      />,
    );
    await settle();

    click(rung("Allowed inside the workspace"));
    expect(asked).toEqual([]); // nothing sent yet
    expect(text()).toContain("Ordinary commands will run without asking");

    click(button("I understand, turn it on"));
    await settle();
    expect(asked).toEqual([{ rung: "open", confirm: true }]);
  });

  it("asks again the next time the open rung is chosen", async () => {
    // A user who tightened the rung and later loosens it again gets the same
    // question: the acknowledgement is per change, not once per session.
    const asked: { rung: string; confirm: boolean }[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setRung: async (rung, confirm) => {
            asked.push({ rung, confirm });
            return { rung, grants: [] };
          },
        })}
      />,
    );
    await settle();

    click(rung("Allowed inside the workspace"));
    click(button("I understand, turn it on"));
    await settle();
    expect(text()).toContain("Ordinary commands run without asking");

    click(rung("Ask once per workspace"));
    await settle();
    expect(text()).not.toContain("Ordinary commands run without asking");

    click(rung("Allowed inside the workspace"));
    expect(text()).toContain("Ordinary commands will run without asking");
    click(button("I understand, turn it on"));
    await settle();
    expect(asked.map((a) => a.rung)).toEqual(["open", "workspace", "open"]);
    expect(text()).toContain("Ordinary commands run without asking");
  });

  it("lets the question be declined without changing anything", async () => {
    const asked: string[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setRung: async (rung) => {
            asked.push(rung);
            return { rung, grants: [] };
          },
        })}
      />,
    );
    await settle();
    click(rung("Allowed inside the workspace"));
    click(button("Cancel"));
    await settle();
    expect(asked).toEqual([]);
    expect(text()).not.toContain("Ordinary commands will run without asking");
  });

  it("does not ask twice for a rung that does ask every time", async () => {
    const asked: { rung: string; confirm: boolean }[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setRung: async (rung, confirm) => {
            asked.push({ rung, confirm });
            return { rung, grants: [] };
          },
        })}
      />,
    );
    await settle();
    click(rung("Ask every time"));
    await settle();
    expect(asked).toEqual([{ rung: "strict", confirm: false }]);
  });
});

describe("a recursive delete is asked twice", () => {
  const tree = (over: Record<string, unknown> = {}) =>
    approval({
      kind: "write",
      summary: "Remove the old package",
      operations: [
        {
          type: "delete",
          path: "packages/web",
          recursive_delete: true,
          ...over,
        },
      ],
    });

  it("does not send on the first press", () => {
    // security.md asks for a double confirmation. Until now the two gates
    // were the caller's recursive=true flag and this screen — and the first
    // of those is not a person.
    let decided = 0;
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [tree()] })}
        tasks={[]}
        onApprove={() => {
          decided++;
        }}
      />,
    );
    click(button("Approve"));
    expect(decided).toBe(0);
    // And the button no longer says the same thing: a second press on
    // identical words is a reflex, not a decision.
    expect(button("Approve")).toBeUndefined();
    expect(button("Delete it all")).toBeDefined();

    click(button("Delete it all"));
    expect(decided).toBe(1);
  });

  it("shows how much goes before the press that does it", () => {
    // The size lives in the details panel, which is collapsed by default. A
    // second click on words the person was never shown is the reflex this
    // step exists to avoid, so arming opens the number too.
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({
          approvals: [tree({ tree: { files: 1284, bytes: 5 * 1024 * 1024 } })],
        })}
        tasks={[]}
      />,
    );
    expect(text()).not.toContain("1284 files");
    click(button("Approve"));
    expect(text()).toContain("1284 files");
    expect(text()).toContain("5 MB");
    expect(text()).toContain("packages/web");
  });

  it("says when the undo will be empty", () => {
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({
          approvals: [
            tree({
              tree: { files: 9, bytes: 2 * 1024 * 1024 * 1024 },
              beyond_undo: true,
            }),
          ],
        })}
        tasks={[]}
      />,
    );
    click(button("Approve"));
    expect(text()).toContain("may not come back");
  });

  it("asks an ordinary write only once", () => {
    // The second press is for the operation that cannot be taken back a file
    // at a time. Asking it of everything would make it mean nothing.
    let decided = 0;
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [approval()] })}
        tasks={[]}
        onApprove={() => {
          decided++;
        }}
      />,
    );
    click(button("Approve"));
    expect(decided).toBe(1);
  });

  it("rejects on the first press, armed or not", () => {
    // Arming guards the destructive answer. Making someone press twice to
    // say no would be friction pointed at the safe direction.
    let rejected = 0;
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [tree()] })}
        tasks={[]}
        onReject={() => {
          rejected++;
        }}
      />,
    );
    click(button("Approve"));
    click(button("Reject"));
    expect(rejected).toBe(1);
  });
});

describe("what the prompt says about the network", () => {
  const ask = (over: Partial<Approval>) =>
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [approval(over)] })}
        tasks={[]}
      />,
    );

  it("states it in the details, not on the face", async () => {
    // The face carries the statement, the command and the meta line. A
    // network word up there on every command prompt is noise, and noise is
    // how the one sentence that matters loses its reader.
    ask({ kind: "command", command: ["npm", "test"], network: "denied" });
    expect(text()).not.toContain("no network");
    click(button("Details"));
    expect(text()).toContain("no network");
  });

  it("marks the answer the machine could not deliver", () => {
    ask({ kind: "command", command: ["npm", "test"], network: "unbounded" });
    click(button("Details"));
    expect(text()).toContain("asked for, this machine cannot");
  });

  it("says nothing of the sort on a write", () => {
    ask({ kind: "write", network: "denied" });
    click(button("Details"));
    expect(text()).not.toContain("no network");
  });
});

describe("outbound network, per folder", () => {
  const folder = (over: Partial<Workspace> = {}): Workspace => ({
    ...WS,
    id: "ws_1",
    name: "ai-workspace",
    network: "allow",
    network_reach: "allowed",
    ...over,
  });

  const switchFor = (name: string) =>
    Array.from(host.querySelectorAll('[role="switch"]')).find((el) =>
      (el.getAttribute("aria-label") ?? "").includes(name),
    );

  it("gives every granted folder its own answer", async () => {
    // One machine holds both a repository whose build has to fetch
    // dependencies and one whose contents should not be able to leave. A
    // single machine-wide switch could only take the lower of the two.
    draw(
      <SettingsScreen
        {...settingsProps}
        workspaces={[
          folder(),
          folder({
            id: "ws_2",
            name: "client-work",
            network: "deny",
            network_reach: "denied",
          }),
        ]}
        deps={deps()}
      />,
    );
    await settle();
    expect(switchFor("ai-workspace")?.getAttribute("aria-checked")).toBe(
      "true",
    );
    expect(switchFor("client-work")?.getAttribute("aria-checked")).toBe(
      "false",
    );
    expect(text()).toContain("no network");
  });

  it("says when the folder asked and the machine cannot", async () => {
    // The answer that must never be drawn as an ordinary "off": the user
    // asked for no network and did not get it.
    draw(
      <SettingsScreen
        {...settingsProps}
        workspaces={[folder({ network: "deny", network_reach: "unbounded" })]}
        deps={deps()}
      />,
    );
    await settle();
    expect(text()).toContain("asked for, this machine cannot");
    const warn = Array.from(host.querySelectorAll(".fy-warn")).find((el) =>
      (el.textContent ?? "").includes("no way to deny"),
    );
    expect(warn).toBeDefined();
    // And it still says what did not change with it.
    expect(warn?.textContent).toContain("the path sandbox");
  });

  it("says what a partial boundary does not stop", async () => {
    // Landlock denies TCP and has no UDP rule. Reporting that as "denied"
    // would be the silent downgrade the whole design refuses.
    draw(
      <SettingsScreen
        {...settingsProps}
        workspaces={[folder({ network: "deny", network_reach: "partial" })]}
        deps={deps()}
      />,
    );
    await settle();
    expect(text()).toContain("DNS and QUIC still leave");
  });

  it("takes the Core's word back rather than echoing the click", async () => {
    // The word beside the switch is computed by the Core, so guessing it
    // locally would put a claim on screen that the machine never made.
    const asked: { id: string; allow: boolean }[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        workspaces={[folder()]}
        deps={deps({
          setNetwork: async (id, allow) => {
            asked.push({ id, allow });
            return {
              workspaces: [
                folder({ network: "deny", network_reach: "unbounded" }),
              ],
              currentWorkspaceID: "ws_1",
            };
          },
        })}
      />,
    );
    await settle();
    click(switchFor("ai-workspace"));
    await settle();
    expect(asked).toEqual([{ id: "ws_1", allow: false }]);
    expect(text()).toContain("asked for, this machine cannot");
  });

  it("draws nothing for a revoked folder", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        workspaces={[folder({ status: "revoked" })]}
        deps={deps()}
      />,
    );
    await settle();
    expect(switchFor("ai-workspace")).toBeUndefined();
  });
});

// The dock's open width was a constant sized off two Chinese characters, so
// "Settings" arrived clipped to "Setting". jsdom does no layout and reports
// every scrollWidth as 0, so the label widths are stubbed: what is pinned is
// that the shell's width follows them rather than a number in the source.
describe("the dock's open width", () => {
  const PAGES = [
    { key: "lane", label: "nav.lane" },
    { key: "tasks", label: "nav.tasks" },
    { key: "settings", label: "nav.settings" },
  ] as const;

  /** Gives every element a scrollWidth proportional to its text, the way a
   *  browser would. jsdom defines scrollWidth on Element.prototype, not on
   *  HTMLElement.prototype, so there is no own descriptor to put back — the
   *  stub has to be deleted or it shadows the real one for every later test. */
  function measureText() {
    Object.defineProperty(HTMLElement.prototype, "scrollWidth", {
      configurable: true,
      get(this: HTMLElement) {
        return (this.textContent ?? "").length * 7;
      },
    });
    return () => {
      delete (HTMLElement.prototype as { scrollWidth?: unknown }).scrollWidth;
    };
  }

  const shellWidth = () =>
    (host.querySelector(".fy-dock-shell") as HTMLElement | null)?.style.width ??
    "";

  /** Renders in one language and returns the pinned-open width.
   *
   *  The unmount matters: `draw` reuses one root, so a second render is the
   *  same component with its state intact — and the press that pins the dock
   *  open would un-pin it instead. Measured that way the second language
   *  reads 40px and any comparison passes for the wrong reason. */
  function openWidthFor(lang: "en" | "zh"): number {
    const restore = measureText();
    try {
      draw(<></>);
      draw(
        <Dock
          pages={[...PAGES]}
          current="lane"
          onGoto={() => {}}
          pending={false}
        />,
        lang,
      );
      click(buttons()[0]);
      return parseFloat(shellWidth());
    } finally {
      restore();
    }
  }

  it("follows the Chinese labels", () => {
    expect(openWidthFor("zh")).toBeGreaterThan(40);
  });

  it("is wider in English, where the same three labels are longer words", () => {
    // "Lane / Tasks / Settings" against "\u901a\u9053 / \u4efb\u52a1 / \u8bbe\u7f6e". The old constant was
    // measured off the second and clipped the first.
    expect(openWidthFor("en")).toBeGreaterThan(openWidthFor("zh"));
  });

  it("is the mark and nothing else while shut", () => {
    // Collapsed the dock is one 40px square. The labels are still in the DOM
    // — they have to be, or there would be nothing to measure — so the width
    // is the only thing keeping them off the screen.
    const restore = measureText();
    try {
      draw(
        <Dock
          pages={[...PAGES]}
          current="lane"
          onGoto={() => {}}
          pending={false}
        />,
      );
      expect(shellWidth()).toBe("40px");
    } finally {
      restore();
    }
  });
});

describe("the installed language servers", () => {
  const withServers = (language_servers: unknown[]) => ({
    commands: async () => ({
      rung: "workspace" as const,
      grants: [],
      language_servers,
    }),
  });

  it("names each one and what it reads, and says which are up", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps(
          withServers([
            { name: "gopls", extensions: [".go"], running: true },
            {
              name: "pyright-langserver",
              extensions: [".py", ".pyi"],
              running: false,
            },
          ]) as Partial<SettingsDeps>,
        )}
      />,
    );
    await settle();
    const shown = text();
    expect(shown).toContain("gopls");
    // A name on its own means nothing to someone who has not met gopls.
    expect(shown).toContain(".go");
    expect(shown).toContain(".pyi");
    expect(shown).toContain("running");
    // A server that has never been started is still listed: the list answers
    // what may start, not what happens to have started.
    expect(shown).toContain("pyright-langserver");
  });

  it("says an idle server starts when needed rather than reporting it as down", async () => {
    // "not running" reads as a fault. The server is installed and works; it
    // is lazily started and reclaimed after twenty idle minutes, so the
    // normal state must not look like a broken one.
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps(
          withServers([
            { name: "rust", extensions: [".rs"], running: false },
          ]) as Partial<SettingsDeps>,
        )}
      />,
    );
    await settle();
    expect(text()).toContain("starts when needed");
    expect(text()).not.toContain("not running");
  });

  it("writes the language the way its community writes it, and leaves unknown names alone", async () => {
    // The Core sends its settings-file identifier. `rust` is a key, not a
    // word anyone reads. A name Fylane has never heard of is the user's own
    // configuration and is passed through rather than guessed at.
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps(
          withServers([
            { name: "typescript", extensions: [".ts"], running: false },
            { name: "nim-langserver", extensions: [".nim"], running: false },
          ]) as Partial<SettingsDeps>,
        )}
      />,
    );
    await settle();
    expect(text()).toContain("TypeScript");
    expect(text()).toContain("nim-langserver");
  });

  it("counts the rest instead of printing eight extensions", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps(
          withServers([
            {
              name: "typescript",
              extensions: [
                ".ts",
                ".tsx",
                ".mts",
                ".cts",
                ".js",
                ".jsx",
                ".mjs",
                ".cjs",
              ],
              running: false,
            },
          ]) as Partial<SettingsDeps>,
        )}
      />,
    );
    await settle();
    expect(text()).toContain(".ts .tsx .mts and 5 more");
  });

  it("offers nothing to press", async () => {
    // Read-only on purpose: reclaim is automatic and the authorization dies
    // with the process, so a button here would control something that
    // expires on its own.
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps(
          withServers([
            { name: "gopls", extensions: [".go"], running: true },
          ]) as Partial<SettingsDeps>,
        )}
      />,
    );
    await settle();
    const row = Array.from(host.querySelectorAll(".fy-rule-row")).find((el) =>
      (el.textContent ?? "").includes("gopls"),
    );
    expect(row).toBeDefined();
    expect(row?.querySelectorAll('button, [role="switch"], input').length).toBe(
      0,
    );
  });

  it("draws no section on a machine with none installed", async () => {
    // Same as the provider list: the design has no board for an empty one,
    // and a row whose only content is "none" is not worth the space.
    draw(<SettingsScreen {...settingsProps} undoCount={0} deps={deps()} />);
    await settle();
    expect(text()).not.toContain(DICT["set.servers"].en);
  });

  it("reads an older Core, which has no such field, as none installed", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={0}
        deps={deps({
          commands: async () => ({ rung: "workspace", grants: [] }),
        })}
      />,
    );
    await settle();
    expect(text()).not.toContain(DICT["set.servers"].en);
  });
});

describe("the subprocess read boundary", () => {
  const withBoundary = (state: "enforced" | "absent" | "off", detail = "") => ({
    prefs: async () => ({ ...PREFS, read_boundary: { state, detail } }),
  });

  const boundarySwitch = () =>
    Array.from(host.querySelectorAll('[role="switch"]')).find((el) =>
      (el.getAttribute("aria-label") ?? "").includes("read boundary"),
    );

  it("offers a switch where the machine can enforce one", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps(withBoundary("enforced"))}
      />,
    );
    await settle();
    expect(boundarySwitch()?.getAttribute("aria-checked")).toBe("true");
    expect(host.querySelector(".fy-warn")).toBeNull();
  });

  it("says so, without a switch, where the machine cannot", async () => {
    // Absence is not a choice anyone made. A disabled switch would sit in the
    // off position and read as one, and hiding the row would leave a person
    // believing in a defence that is not there.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps(
          withBoundary("absent", "no read boundary is available on windows"),
        )}
      />,
    );
    await settle();
    expect(boundarySwitch()).toBeUndefined();
    expect(text()).toContain("Subprocess read boundary");
    expect(text()).toContain("no read boundary is available on windows");
    // Absence is not the user's doing, so it is not warned about as if it
    // were something they switched off.
    expect(text()).not.toContain("Programs Fylane starts can read anything");
  });

  it("keeps saying it is off, for as long as it is off", async () => {
    // Same rule as the open rung: a defence that has been switched off is
    // only safe while it is visible.
    draw(
      <SettingsScreen {...settingsProps} deps={deps(withBoundary("off"))} />,
    );
    await settle();
    expect(boundarySwitch()?.getAttribute("aria-checked")).toBe("false");
    expect(host.querySelector(".fy-warn")).not.toBeNull();
    expect(text()).toContain("Subprocess reads are not bounded");
    // And it still says what did not change with it.
    expect(text()).toContain("Approval, the rule table, the path sandbox");
  });

  it("sends the new value to the Core, and nothing else with it", async () => {
    const sent: unknown[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          ...withBoundary("enforced"),
          save: async (patch) => {
            sent.push(patch);
            return { ...PREFS, read_boundary: { state: "off", detail: "" } };
          },
        })}
      />,
    );
    await settle();
    click(boundarySwitch());
    await settle();
    expect(sent).toEqual([{ read_boundary: false }]);
  });
});

describe("high-risk commands", () => {
  it("are a stated fact, not a control that could be turned off", async () => {
    // The product does not offer "always allow" for these. A switch that
    // refuses is worse than no switch.
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    const row = host.querySelector(".fy-fixedpill");
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain("Always asks");
    expect(row?.textContent).toContain("Fixed");
    // Nothing in the execution panel that could switch it off.
    expect(
      host.querySelectorAll('[role="switch"][aria-label*="risk"]'),
    ).toHaveLength(0);
  });
});

// ── the update notice ────────────────────────────────────────────────────
//
// The Core has run this check daily, reported it on /v1/status and been
// believed to be finished. The desktop never
// declared the fields, so the notice half of "check and notify" reached
// nobody. What is pinned here is that it now arrives, that it stays quiet
// when there is nothing to say, and that it does not offer a link to a page
// that does not exist yet.
describe("the update notice", () => {
  it("names the newer version on the line that already names the build", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        version="0.0.1"
        update={{ version: "0.0.2", page: "https://example.invalid/releases" }}
        deps={deps()}
      />,
    );
    await settle();
    expect(text()).toContain("Fylane 0.0.1");
    expect(text()).toContain("0.0.2 is out");
    expect(button("Get it")).toBeTruthy();
  });

  // DownloadPage is empty until the public release page exists, and its own
  // comment says consumers hide the link while it is. The version is still
  // worth knowing, so the notice stays and only the link goes.
  it("says it without a link while there is no release page", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        version="0.0.1"
        update={{ version: "0.0.2" }}
        deps={deps()}
      />,
    );
    await settle();
    expect(text()).toContain("0.0.2 is out");
    expect(button("Get it")).toBeFalsy();
  });

  // Silence covers two states on purpose — the check is off (it is by
  // default) or it found nothing. Saying "up to date" would be a claim
  // nobody made: with the check off, nobody looked.
  it("says nothing at all when there is nothing to say", async () => {
    draw(<SettingsScreen {...settingsProps} version="0.0.1" deps={deps()} />);
    await settle();
    expect(text()).toContain("Fylane 0.0.1");
    expect(text()).not.toContain("is out");
    expect(text()).not.toContain("up to date");
  });
});

// ── standing workspace grants ─────────────────────────────────────────────
//
// The Core kept this list and could withdraw from it all along; nothing on
// this page read it, so an authorization given six weeks ago could be neither
// seen nor taken back. What is pinned here is that it is now visible on every
// rung, that it says something true on each of the three, and that one press
// withdraws it.
// The file-write approval policy: the second of the two approval axes the
// Core has carried all along. It had a control on the v2 Safety page, lost
// it when V3 replaced that page, and answered nobody for the six months
// after — while `POST /v1/safety` and the `approval_mode` status field went
// on working. What is pinned here is that the page shows which policy is
// running, can change it, and never invents one.

// Hiding the Dock icon is a shell setting: it lives with the window, not the
// Core, and where there is no Dock the row has to say so rather than offer a
// switch that would do nothing.
// The settings page used to read once, on mount, and never again: opened
// while the Core was starting — or after its console window was closed on
// Windows, which kills it — every read failed, one toast fired, and nothing
// recovered until the page was remounted.
describe("settings while the Core is unreachable", () => {
  const down = async () => {
    throw new Error("companion core is not reachable");
  };
  const unreachable = () =>
    deps({ prefs: down, commands: down, status: down, dock: down });

  it("raises no message while the shell already says the Core is down", async () => {
    const raised: string[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        online={false}
        deps={unreachable()}
        onError={(m) => raised.push(m)}
      />,
    );
    await settle();
    expect(raised).toEqual([]);
  });

  it("still raises it when the Core is supposedly up and the read fails", async () => {
    // The quiet is for a Core the shell knows is down, not for every failure.
    const raised: string[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        online={true}
        deps={unreachable()}
        onError={(m) => raised.push(m)}
      />,
    );
    await settle();
    expect(raised.length).toBeGreaterThan(0);
  });

  it("reads again on its own when the Core comes back", async () => {
    // Driven through state rather than two draws: a second draw reuses the
    // instance and would not exercise the effect's dependency.
    let reads = 0;
    let setOnline: (v: boolean) => void = () => {};
    const good = deps({
      commands: async () => {
        reads += 1;
        return { rung: "workspace" as const, grants: [] };
      },
    });
    function Host() {
      const [online, set] = useState(false);
      setOnline = set;
      return <SettingsScreen {...settingsProps} online={online} deps={good} />;
    }
    draw(<Host />);
    await settle();
    const before = reads;
    act(() => setOnline(true));
    await settle();
    expect(reads).toBeGreaterThan(before);
  });
});

describe("hiding the Dock icon", () => {
  const sw = (label: string) =>
    buttons().find(
      (b) =>
        b.getAttribute("role") === "switch" &&
        b.getAttribute("aria-label") === label,
    );

  it("offers the switch where there is a Dock, off by default", async () => {
    // Bare settingsProps carries no deps, and the real bindings do not exist
    // under jsdom — the switch only appears once the shell has answered.
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    expect(sw("Hide Dock icon")?.getAttribute("aria-checked")).toBe("false");
    expect(sw("Hide Dock icon")?.hasAttribute("disabled")).toBe(false);
  });

  it("sends the choice and shows what the shell now holds", async () => {
    let sent: boolean | null = null;
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setDock: async (hidden: boolean) => {
            sent = hidden;
            return { supported: true, hidden };
          },
        })}
      />,
    );
    await settle();
    click(sw("Hide Dock icon"));
    await settle();
    expect(sent).toBe(true);
    expect(sw("Hide Dock icon")?.getAttribute("aria-checked")).toBe("true");
  });

  it("keeps the switch where the shell left it when the change was refused", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setDock: async () => ({ supported: true, hidden: false }),
        })}
      />,
    );
    await settle();
    click(sw("Hide Dock icon"));
    await settle();
    expect(sw("Hide Dock icon")?.getAttribute("aria-checked")).toBe("false");
  });

  it("draws no row at all on a system with no Dock", async () => {
    // Windows and Linux have a taskbar, not a Dock. A disabled switch with an
    // explanation would be an option that exists only to say it does not.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ dock: async () => ({ supported: false, hidden: false }) })}
      />,
    );
    await settle();
    expect(sw("Hide Dock icon")).toBeUndefined();
    expect(text()).not.toContain("Dock");
    // The login switch it shares a cell with is unaffected.
    expect(sw("Start at login")).toBeTruthy();
  });

  it("leaves the login switch alone in the cell it now shares", async () => {
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    expect(sw("Start at login")).toBeTruthy();
    expect(text()).toContain("Startup & presence");
  });
});

describe("the file-write approval policy", () => {
  const st = (mode: "safe" | "balanced"): CoreStatusInfo => ({
    ...STATUS,
    approval_mode: mode,
  });

  it("marks the policy the Core says is running, not the default", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ status: async () => st("balanced") })}
      />,
    );
    await settle();

    expect(
      rung("New files write straight through")?.getAttribute("aria-checked"),
    ).toBe("true");
    expect(rung("Ask before every write")?.getAttribute("aria-checked")).toBe(
      "false",
    );
  });

  it("says what still asks, because that is what someone loosening it is deciding about", async () => {
    draw(<SettingsScreen {...settingsProps} />);
    await settle();
    expect(text()).toContain("Edits, deletes and sensitive paths still ask");
  });

  it("sends the choice to the Core and shows the mode the Core came back with", async () => {
    let sent = "";
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setMode: async (mode) => {
            sent = mode;
            return { approval_mode: mode };
          },
        })}
      />,
    );
    await settle();

    click(rung("New files write straight through"));
    await settle();
    expect(sent).toBe("balanced");
    expect(
      rung("New files write straight through")?.getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("keeps the row where the Core left it when the switch was refused", async () => {
    // The Core answers with the mode in force rather than echoing the
    // request. A page that moved the mark on click would show a policy the
    // machine is not running.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          setMode: async () => ({ approval_mode: "safe" as const }),
        })}
      />,
    );
    await settle();

    click(rung("New files write straight through"));
    await settle();
    expect(rung("Ask before every write")?.getAttribute("aria-checked")).toBe(
      "true",
    );
    expect(
      rung("New files write straight through")?.getAttribute("aria-checked"),
    ).toBe("false");
  });

  it("offers no choice at all when the Core did not say which policy is running", async () => {
    // Not a fallback to safe: that is the default, so a guess would land on
    // it and look right while the machine ran the other one.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          status: async () => {
            throw new Error("no status");
          },
        })}
      />,
    );
    await settle();

    expect(rung("Ask before every write")?.hasAttribute("disabled")).toBe(true);
    expect(
      rung("New files write straight through")?.getAttribute("aria-checked"),
    ).toBe("false");
  });

  it("names both axes in the panel head, not just the command one", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ status: async () => st("balanced") })}
      />,
    );
    await settle();
    expect(text()).toContain(
      "Ask once per workspace · New files write straight through",
    );
  });

  it("keeps the two approval axes apart for a screen reader", async () => {
    // One column, two groups. Were they one radiogroup, choosing a write
    // policy would read as clearing the command rung.
    draw(<SettingsScreen {...settingsProps} />);
    await settle();

    const groups = Array.from(
      host.querySelectorAll('.fy-panel-col [role="radiogroup"]'),
    ).map((g) => g.getAttribute("aria-label"));
    expect(groups).toEqual(["ORDINARY COMMANDS", "FILE WRITES"]);
  });
});

describe("standing workspace grants", () => {
  const daysAgo = (n: number) =>
    new Date(Date.now() - n * 86400000).toISOString();
  const held = (
    over: Partial<CommandSettingsInfo> = {},
  ): CommandSettingsInfo => ({
    rung: "workspace",
    grants: [
      { workspace_id: "ws_1", rung: "workspace", granted_at: daysAgo(42) },
    ],
    ...over,
  });

  it("names the folder, says how old the authorization is, and withdraws on one press", async () => {
    let revoked = "";
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          commands: async () => held(),
          revokeGrant: async (id: string) => {
            revoked = id;
            return { rung: "workspace" as const, grants: [] };
          },
        })}
      />,
    );
    await settle();

    expect(text()).toContain("ai-workspace");
    expect(text()).toContain("42 d ago");

    // One press. Clearing the undo copies asks twice because it destroys the
    // only way back; withdrawing destroys nothing and can only narrow what
    // this machine does unattended.
    click(button("Withdraw"));
    await settle();
    expect(revoked).toBe("ws_1");
    expect(text()).toContain("No folder is authorized");
  });

  // The same sentence is false on two of the three rungs: strict never
  // consults a grant and open never reaches one. A note that claimed
  // otherwise would credit these rows with an authority they do not have.
  it("says a grant is what keeps commands unasked only on the rung where it is", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ commands: async () => held() })}
      />,
    );
    await settle();
    expect(await helpOf("Folders already authorized")).toContain(
      "ordinary commands there no longer ask",
    );
  });

  // The explanation lives in a tooltip, so a test has to open it the way a
  // keyboard user does before it can read it.
  const helpOf = async (label: string) => {
    const el = Array.from(document.querySelectorAll(".fy-help")).find((e) =>
      e.textContent?.includes(label),
    ) as HTMLElement;
    await act(async () => el.focus());
    const tip = document.querySelector('[role="tooltip"]')?.textContent ?? "";
    await act(async () => el.blur());
    return tip;
  };

  it("keeps the explanation behind the label, shown while it has focus", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ commands: async () => held() })}
      />,
    );
    await settle();
    expect(document.querySelector('[role="tooltip"]')).toBeNull();
    const label = Array.from(document.querySelectorAll(".fy-help")).find((el) =>
      el.textContent?.includes("Folders already authorized"),
    ) as HTMLElement;
    await act(async () => label.focus());
    expect(document.querySelector('[role="tooltip"]')?.textContent).toContain(
      "Withdraw to be asked again",
    );
    await act(async () => label.blur());
    expect(document.querySelector('[role="tooltip"]')).toBeNull();
  });

  it("does not claim it on strict, which consults no grant at all", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ commands: async () => held({ rung: "strict" }) })}
      />,
    );
    await settle();
    const help = await helpOf("Folders already authorized");
    expect(help).toContain("these authorizations are not in effect");
    expect(help).not.toContain("ordinary commands there no longer ask");
  });

  it("does not claim it on open, where the rung and not the grant is the reason", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ commands: async () => held({ rung: "open" }) })}
      />,
    );
    await settle();
    const help = await helpOf("Folders already authorized");
    expect(help).toContain("these authorizations make no difference");
    expect(help).not.toContain("ordinary commands there no longer ask");
  });

  // A row the user cannot see is a row they cannot clear, so an authorization
  // the current rung ignores is still listed — and still says why.
  it("lists an authorization the rung no longer honours instead of hiding it", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ commands: async () => held({ rung: "strict" }) })}
      />,
    );
    await settle();
    expect(text()).toContain("ai-workspace");
    expect(text()).toContain("not in effect");
    expect(button("Withdraw")).toBeTruthy();
  });

  // An authorization can outlive the folder's place in the list. Falling back
  // to the id keeps it on screen and therefore removable.
  it("still shows a grant whose folder is no longer listed", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        workspaces={[]}
        deps={deps({ commands: async () => held() })}
      />,
    );
    await settle();
    expect(text()).toContain("ws_1");
    expect(button("Withdraw")).toBeTruthy();
  });

  // Nothing authorized is a fact worth stating on a security page. Drawing no
  // section at all would leave the user unable to tell "none" from "not shown".
  it("says nothing is authorized rather than showing nothing", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          commands: async () => ({ rung: "workspace" as const, grants: [] }),
        })}
      />,
    );
    await settle();
    expect(text()).toContain(
      "No folder is authorized. Every command is asked about.",
    );
  });
});

describe("clearing the undo copies", () => {
  it("asks a second time before taking the only way to put a file back", async () => {
    let cleared = 0;
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={2}
        onClearBackups={async () => {
          cleared += 1;
          return { cleared: 2, backups_removed: 2 };
        }}
        deps={deps()}
      />,
    );
    await settle();

    click(button("Clear the undo copies"));
    expect(cleared).toBe(0);
    expect(text()).toContain("cannot be reversed");

    click(button("Clear them"));
    expect(cleared).toBe(1);
  });

  it("is not offered when there is nothing inside its window", async () => {
    draw(<SettingsScreen {...settingsProps} undoCount={0} deps={deps()} />);
    await settle();
    expect(button("Clear the undo copies")?.disabled).toBe(true);
  });

  // The row's own note goes back to "nothing is within its undo window" the
  // instant the copies are gone, which is true and tells the user nothing
  // about what they just gave up. This line is the only place the size of it
  // is ever said.
  it("says how many writes can no longer be taken back", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={3}
        onClearBackups={async () => ({ cleared: 3, backups_removed: 3 })}
        deps={deps()}
      />,
    );
    await settle();
    click(button("Clear the undo copies"));
    click(button("Clear them"));
    await settle();

    expect(text()).toContain("3 writes can no longer be taken back");
    expect(text()).not.toContain("still taking space");
  });

  // cleared and backups_removed are two different numbers on purpose: the
  // Core drops the undo promise for every row, then deletes what it can. A
  // copy it could not delete is wasted disk that undoes nothing, and saying
  // only the first number would report a tidier machine than the real one.
  it("does not hide the copies it could not delete", async () => {
    draw(
      <SettingsScreen
        {...settingsProps}
        undoCount={3}
        onClearBackups={async () => ({ cleared: 3, backups_removed: 1 })}
        deps={deps()}
      />,
    );
    await settle();
    click(button("Clear the undo copies"));
    click(button("Clear them"));
    await settle();

    expect(text()).toContain("3 writes can no longer be taken back");
    expect(text()).toContain("2 copies could not be deleted");
  });
});

// ── the states the first pass did not reach ──────────────────────────────
//
// The tests above were chosen because those defects had already reached a
// user. These cover the rest of what the three screens can be in, while the
// screens are fresh — the same coverage written a year from now would be
// archaeology.

describe("the lane's workspace anchor", () => {
  it("offers the granted folders, marks the current one, and can add another", () => {
    const other: Workspace = {
      ...WS,
      id: "ws_2",
      name: "notes",
      root_path: "/Users/you/notes",
    };
    let picked = "";
    let added = 0;
    draw(
      <LaneScreen
        {...laneProps}
        workspaces={[WS, other]}
        snapshot={snap()}
        tasks={[]}
        onSelectWorkspace={(id) => {
          picked = id;
        }}
        onChooseWorkspace={() => {
          added += 1;
        }}
      />,
    );
    click(button("Switch"));
    expect(text()).toContain("notes");

    const current = host.querySelector('.fy-wsitem[data-current="true"]');
    expect(current?.textContent).toContain("ai-workspace");

    click(
      Array.from(host.querySelectorAll(".fy-wsitem")).find((b) =>
        b.textContent?.includes("notes"),
      ),
    );
    expect(picked).toBe("ws_2");

    click(button("Switch"));
    click(
      Array.from(host.querySelectorAll(".fy-wsitem")).find((b) =>
        b.textContent?.includes("Add a folder"),
      ),
    );
    expect(added).toBe(1);
  });

  it("offers to choose one rather than to switch when none is granted", () => {
    draw(
      <LaneScreen
        {...laneProps}
        workspaces={[]}
        snapshot={snap({ workspace: null })}
        tasks={[]}
      />,
    );
    expect(button("Switch")).toBeUndefined();
    expect(button("Choose a folder")).toBeDefined();
  });
});

describe("the tasks filter", () => {
  const rows = [
    task({ task_id: "a", state: "succeeded" }),
    task({ task_id: "b", state: "running", label: "npm run build" }),
    task({ task_id: "c", state: "failed", label: "docker compose ps" }),
  ];
  const props = {
    changeSets: [] as ChangeSet[],
    workspace: WS,
    now: new Date("2026-08-13T10:00:00"),
    canStop: true,
    onCancel: () => {},
    onRollback: () => {},
    onAccept: () => {},
    onCopy: () => {},
    onGotoLane: () => {},
  };

  it("counts every word against the same list", () => {
    draw(<TasksScreen {...props} tasks={rows} />);
    const counts = Array.from(host.querySelectorAll(".fy-filter")).map(
      (f) => f.querySelector("span")?.textContent,
    );
    expect(counts).toEqual(["3", "1", "1", "1", "0"]);
  });

  it("counts the writes nobody has reviewed, and answers zero rather than vanishing", () => {
    // Zero is an answer: "everything that landed has been looked at" is worth
    // reading, and a word that appears only when it is non-zero moves the
    // header under whoever is reading it.
    const landed = (over: Partial<ChangeSet>): ChangeSet => ({
      id: "chg",
      workspace_id: "ws_1",
      provider: "claude",
      summary: "Update auth",
      operations: [{ path: "src/auth.ts", status: "updated" }],
      status: "applied",
      created_at: "2026-08-13T09:50:00",
      ...over,
    });
    const review = () =>
      Array.from(host.querySelectorAll(".fy-filter")).find((f) =>
        f.textContent?.startsWith("To review"),
      );

    draw(
      <TasksScreen {...props} tasks={[]} changeSets={[landed({ id: "w1" })]} />,
    );
    expect(review()?.querySelector("span")?.textContent).toBe("1");

    draw(
      <TasksScreen
        {...props}
        tasks={[]}
        changeSets={[landed({ id: "w2", accepted_at: "2026-08-13T09:55:00" })]}
      />,
    );
    expect(review()?.querySelector("span")?.textContent).toBe("0");
  });

  it("is a view of the same list, not a list of its own", () => {
    // An applied write nobody looked at is also done. The word overlaps on
    // purpose, and "all" must not grow or shrink because of it.
    const landed: ChangeSet = {
      id: "w1",
      workspace_id: "ws_1",
      provider: "claude",
      summary: "Update auth",
      operations: [{ path: "src/auth.ts", status: "updated" }],
      status: "applied",
      created_at: "2026-08-13T09:50:00",
    };
    draw(<TasksScreen {...props} tasks={rows} changeSets={[landed]} />);
    const counts = Array.from(host.querySelectorAll(".fy-filter")).map(
      (f) => f.querySelector("span")?.textContent,
    );
    // all, running, done, not passed, to review — done counts the write too.
    expect(counts).toEqual(["4", "1", "2", "1", "1"]);
  });

  it("narrows to one word and says so when the word matches nothing", () => {
    draw(<TasksScreen {...props} tasks={[task()]} />);
    click(
      Array.from(host.querySelectorAll(".fy-filter")).find((f) =>
        f.textContent?.startsWith("Not passed"),
      ),
    );
    // Not "nothing has run yet" — something has, just not that.
    expect(text()).toContain("Nothing matches this filter");
    expect(text()).not.toContain("Nothing has run yet");
    click(button("Show everything"));
    expect(text()).toContain("go test ./...");
  });

  it("says nothing has run at all when nothing has", () => {
    draw(<TasksScreen {...props} tasks={[]} />);
    expect(text()).toContain("Nothing has run yet");
    expect(button("Back to the lane")).toBeDefined();
  });
});

describe("a write in the record", () => {
  const written = (over: Partial<ChangeSet> = {}): ChangeSet => ({
    id: "chg_w",
    workspace_id: "ws_1",
    provider: "claude",
    summary: "Update auth",
    operations: [{ path: "src/auth.ts", status: "updated" }],
    status: "applied",
    created_at: "2026-08-13T09:50:00",
    ...over,
  });
  const props = {
    tasks: [] as TaskInfo[],
    workspace: WS,
    now: new Date("2026-08-13T10:00:00"),
    canStop: true,
    onCancel: () => {},
    onCopy: () => {},
    onGotoLane: () => {},
  };

  it("offers the undo while the window is open", () => {
    let undone = "";
    draw(
      <TasksScreen
        {...props}
        changeSets={[written({ rollback_deadline: "2026-08-13T16:00:00" })]}
        onRollback={(id) => {
          undone = id;
        }}
        onAccept={() => {}}
      />,
    );
    const undo = button("Undo this write");
    expect(undo?.disabled).toBe(false);
    click(undo);
    expect(undone).toBe("chg_w");
  });

  it("shows the undo as spent once the window has passed, rather than hiding it", () => {
    // Hiding it would leave no explanation for why a write cannot be taken
    // back; a disabled control with the reason beside it is the honest form.
    draw(
      <TasksScreen
        {...props}
        changeSets={[written({ rollback_deadline: "2026-08-13T09:00:00" })]}
        onRollback={() => {}}
        onAccept={() => {}}
      />,
    );
    expect(button("Undo this write")?.disabled).toBe(true);
    expect(text()).toContain("window for this write has passed");
  });

  it("offers the review on a landed write nobody has looked at", () => {
    let accepted = "";
    draw(
      <TasksScreen
        {...props}
        changeSets={[written({ rollback_deadline: "2026-08-13T16:00:00" })]}
        onRollback={() => {}}
        onAccept={(id) => {
          accepted = id;
        }}
      />,
    );
    expect(text()).toContain("Not reviewed yet");
    click(button("Accept this write"));
    expect(accepted).toBe("chg_w");
  });

  it("says when it was reviewed and stops offering to review it again", () => {
    draw(
      <TasksScreen
        {...props}
        changeSets={[
          written({
            rollback_deadline: "2026-08-13T16:00:00",
            accepted_at: "2026-08-13T09:55:00",
          }),
        ]}
        onRollback={() => {}}
        onAccept={() => {}}
      />,
    );
    expect(button("Accept this write")).toBeUndefined();
    expect(text()).toContain("Accepted 09:55");
    // And the undo is still there: saying a write is right is not the same
    // decision as giving up the ability to take it back.
    expect(button("Undo this write")?.disabled).toBe(false);
  });

  it("says a write nobody ever reviewed was never reviewed", () => {
    // The whole point of the column: this must not read the same as a write
    // somebody read and approved of.
    draw(
      <TasksScreen
        {...props}
        changeSets={[written({ rollback_deadline: "2026-08-13T09:00:00" })]}
        onRollback={() => {}}
        onAccept={() => {}}
      />,
    );
    expect(text()).toContain("Never reviewed");
    // Late is still a review, so the offer stands even with the undo spent.
    expect(button("Accept this write")).toBeDefined();
  });
});

// ── keyboard and screen reader ─────────────────────────────────
//
// Operability is a requirement, not polish. Three screens were rewritten in
// this batch and nothing asserted any of it: a control with no accessible
// name is invisible to a screen reader even though it is plainly there, and a
// row whose actions appear on hover is unreachable without a mouse unless
// focus counts too.

/** Every control a screen draws, with the name a screen reader would read. */
function named() {
  return Array.from(
    host.querySelectorAll<HTMLElement>("button, input, [role='button']"),
  ).map((el) => ({
    el,
    name: (el.getAttribute("aria-label") || el.textContent || "").trim(),
  }));
}

describe("every control can be reached and named", () => {
  // Settings is in this sweep too, and it has to be: its controls are the ones
  // most likely to be icon-only or label-by-attribute — a switch with no
  // aria-label is a button a screen reader announces as nothing at all.
  const screens: [string, () => void | Promise<void>][] = [
    [
      "lane, holding a request",
      () =>
        draw(
          <LaneScreen
            {...laneProps}
            snapshot={snap({ approvals: [approval()] })}
            tasks={[]}
          />,
        ),
    ],
    [
      "lane, nothing waiting",
      () =>
        draw(<LaneScreen {...laneProps} snapshot={snap()} tasks={[task()]} />),
    ],
    [
      "tasks",
      () =>
        draw(
          <TasksScreen
            tasks={[task({ state: "running" }), task({ task_id: "b" })]}
            changeSets={[]}
            workspace={WS}
            now={new Date("2026-08-13T10:00:00")}
            canStop
            onCancel={() => {}}
            onRollback={() => {}}
            onAccept={() => {}}
            onCopy={() => {}}
            onGotoLane={() => {}}
          />,
        ),
    ],
    [
      "settings",
      async () => {
        draw(<SettingsScreen {...settingsProps} undoCount={2} deps={deps()} />);
        await settle();
      },
    ],
  ];

  for (const [what, render] of screens) {
    it(`names every control on the ${what}`, async () => {
      await render();
      // Nameless, or named only by the symbol printed on it. A button a
      // screen reader announces as "minus" is as good as unlabelled: it says
      // what the glyph is, never what pressing it does.
      const unusable = named().filter(
        (c) => c.name === "" || /^[^\p{L}\p{N}]+$/u.test(c.name),
      );
      expect(
        unusable.map((c) => `${c.name} :: ${c.el.outerHTML.slice(0, 70)}`),
      ).toEqual([]);
    });

    it(`keeps the ${what} in document order for a keyboard`, async () => {
      // A positive tabindex jumps ahead of everything else on the page and
      // makes the tab order depend on numbers nobody can see.
      await render();
      const jumped = Array.from(host.querySelectorAll("[tabindex]")).filter(
        (el) => Number(el.getAttribute("tabindex")) > 0,
      );
      expect(jumped).toEqual([]);
    });
  }

  it("reaches a running task's actions without a mouse", () => {
    // They arrive on hover. Focus has to count as well, or the stop button
    // exists only for people using a pointer.
    draw(
      <TasksScreen
        tasks={[task({ state: "running", label: "npm run build" })]}
        changeSets={[]}
        workspace={WS}
        now={new Date("2026-08-13T10:00:00")}
        canStop
        onCancel={() => {}}
        onRollback={() => {}}
        onAccept={() => {}}
        onCopy={() => {}}
        onGotoLane={() => {}}
      />,
    );
    const stop = button("Stop");
    expect(stop).toBeDefined();
    // Reachable by tab: a real button, not disabled, no negative tabindex.
    expect(stop!.disabled).toBe(false);
    expect(
      Number(stop!.getAttribute("tabindex") ?? "0"),
    ).toBeGreaterThanOrEqual(0);
    // And the CSS that reveals them keys on focus as well as hover.
    expect(host.querySelector(".fy-trow-acts")).not.toBeNull();
  });

  it("opens a task row from the keyboard", () => {
    draw(
      <TasksScreen
        tasks={[task()]}
        changeSets={[]}
        workspace={WS}
        now={new Date("2026-08-13T10:00:00")}
        canStop
        onCancel={() => {}}
        onRollback={() => {}}
        onAccept={() => {}}
        onCopy={() => {}}
        onGotoLane={() => {}}
      />,
    );
    const row = host.querySelector<HTMLElement>(".fy-trow-head")!;
    expect(row.getAttribute("role")).toBe("button");
    expect(row.tabIndex).toBe(0);
    expect(row.getAttribute("aria-expanded")).toBe("false");
    act(() => {
      row.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
    });
    expect(row.getAttribute("aria-expanded")).toBe("true");
  });

  it("tells a screen reader the state of every choice on the settings page", async () => {
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    // A radio or a switch with no aria-checked reads as an unlabelled button.
    for (const el of Array.from(
      host.querySelectorAll("[role='radio'], [role='switch']"),
    )) {
      expect(el.getAttribute("aria-checked")).toMatch(/^(true|false)$/);
    }
    // And every group of them says what it is a group of.
    for (const g of Array.from(
      host.querySelectorAll("[role='radiogroup'], [role='group']"),
    )) {
      expect(g.getAttribute("aria-label")).toBeTruthy();
    }
  });
});

describe("the granted-folders popover", () => {
  it("keeps the full path reachable even when the row has to shorten it", () => {
    // The popover hangs off the right edge of the rail. It used to be
    // anchored left, ran past the window frame, and clipped the very paths it
    // is open to show — so the shortened path carries the whole one in its
    // title, and the name is what gives way, not the path.
    const deep: Workspace = {
      ...WS,
      id: "ws_deep",
      name: "a-workspace-with-a-long-name",
      root_path: "/home/dev/projects/fylane-demo",
    };
    draw(
      <LaneScreen
        {...laneProps}
        workspaces={[deep]}
        snapshot={snap({ workspace: deep })}
        tasks={[]}
      />,
    );
    click(button("Switch"));

    const path = host.querySelector(".fy-wsitem-path");
    expect(path).not.toBeNull();
    expect(path?.getAttribute("title")).toBe("/home/dev/projects/fylane-demo");
    // The name is the flexible half; the path is why the menu is open.
    expect(host.querySelector(".fy-wsitem-name")?.textContent).toBe(
      "a-workspace-with-a-long-name",
    );
  });
});

describe("what the rail keeps back until asked", () => {
  it("holds the path and each AI's state behind a reveal, but never what is waiting", () => {
    // Hover reveals are for detail at rest. "Asking" is the reason the window
    // is in front of you, so it is not a detail and does not hide.
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({
          approvals: [approval({ provider: "claude" })],
          sources: [
            { provider: "claude", connected: true, lanes_carried: 1 },
            { provider: "grok", connected: false, lanes_carried: 0 },
          ],
        })}
        tasks={[]}
      />,
    );
    const revealed = Array.from(host.querySelectorAll(".fy-reveal")).map((el) =>
      el.textContent?.trim(),
    );
    // The workspace path and the idle provider's state are behind the reveal.
    expect(revealed).toContain(WS.root_path);
    expect(revealed).toContain("Not connected");
    // The one that is asking is not.
    expect(revealed).not.toContain("Asking");
    expect(text()).toContain("Asking");
  });
});

describe("nothing on screen is a source-code escape", () => {
  // A JSX text node does not interpret \uXXXX. Written there by hand — or by a
  // script editing the file — the escape is printed verbatim, and the stepper
  // showed a raw escape in front of a duration to a user. The rule is cheap
  // to hold everywhere, so
  // it is held everywhere rather than on the one button that broke.
  const escapes = /\\[unxU][0-9a-fA-F{]/;

  it("on the lane", () => {
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap({ approvals: [approval()] })}
        tasks={[task()]}
      />,
    );
    expect(text()).not.toMatch(escapes);
  });

  it("on the tasks page", () => {
    draw(
      <TasksScreen
        tasks={[task(), task({ task_id: "b", state: "running" })]}
        changeSets={[]}
        workspace={WS}
        now={new Date("2026-08-13T10:00:00")}
        canStop
        onCancel={() => {}}
        onRollback={() => {}}
        onAccept={() => {}}
        onCopy={() => {}}
        onGotoLane={() => {}}
      />,
    );
    expect(text()).not.toMatch(escapes);
  });

  it("on the settings page", async () => {
    draw(<SettingsScreen {...settingsProps} deps={deps()} />);
    await settle();
    expect(text()).not.toMatch(escapes);
    // The stepper's own two labels are the ones that broke.
    const stepper = host.querySelector(".fy-stepper")!;
    const [minus, plus] = Array.from(stepper.querySelectorAll("button"));
    expect(minus.textContent).toBe("−");
    expect(plus.textContent).toBe("+");
  });

  it("on the settings page when the read failed", async () => {
    // A fresh mount: rendering twice into one root keeps the component's
    // state, so the second render would still be showing the first one's
    // answer rather than the failure being set up here.
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({ prefs: async () => Promise.reject(new Error("nope")) })}
      />,
    );
    await settle();
    expect(text()).not.toMatch(escapes);
    expect(host.querySelector(".fy-stepper output")?.textContent).toBe("—");
  });
});

describe("the hostname a named tunnel needs", () => {
  const named = {
    kind: "cloudflare-named",
    binary: "cloudflared",
    install: "brew install cloudflared",
    installed: true,
    needs_token: false,
    needs_hostname: true,
    stable: true,
    setup: "browser" as const,
    authorized: true,
    checkable: true,
    can_sign_out: true,
    opens_browser: true,
  };

  const open = async (over: Record<string, unknown> = {}) => {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          connect: async () => connect({ providers: [{ ...named, ...over }] }),
        })}
      />,
    );
    await settle();
    click(button("Set up →"));
  };

  it("waits with the mark rather than offering a start that must fail", async () => {
    // Signed in, no domain back yet. The old screen showed the hint and an
    // enabled "use this": pressing it asked the Core to restart into a tunnel
    // that cannot resolve, and said so only afterwards.
    await open();
    expect(host.querySelector(".fy-jelly")).not.toBeNull();
    expect(text()).toContain("Reading the domain from your account");
    expect(button("Use this")?.disabled).toBe(true);
  });

  it("stops waiting and lets it start once the domain arrives", async () => {
    await open({ suggested_hostname: "fylane.example.com" });
    expect(host.querySelector(".fy-jelly")).toBeNull();
    expect(button("Use this")?.disabled).toBe(false);
  });

  it("does not wait for a lookup that is not coming", async () => {
    // Not signed in: an empty field means "type one", not "hold on".
    await open({ authorized: false });
    expect(host.querySelector(".fy-jelly")).toBeNull();
    expect(button("Use this")?.disabled).toBe(true);
  });
});

// ── first run ──────────────────────────────────────────────────────────────

describe("first run", () => {
  const base = {
    workspace: null,
    sources: [] as Source[],
    onChooseFolder: async () => WS,
    onTestWrite: async () => ({
      status: "applied" as const,
      path: "fylane-hello.md",
      bytes: 214,
    }),
    onSetRung: async () => {},
    onFinish: () => {},
  };

  it("never offers the open rung here", async () => {
    // that rung needs an explicit acknowledgement and leaves a standing
    // warning on the settings page. First run is not where someone should be
    // nudged into turning approvals off — it is named as existing instead.
    draw(<OnboardingScreen {...base} step="rung" workspace={WS} />);
    const rungs = Array.from(host.querySelectorAll('[role="radio"]')).map(
      (r) => r.querySelector(".fy-policy-label")?.textContent,
    );
    expect(rungs).toEqual(["Ask every time", "Ask once per workspace"]);
    expect(text()).not.toContain("Allowed inside the workspace");
    expect(text()).toContain("A third setting");
  });

  it("stores the rung as soon as it is chosen", () => {
    const stored: string[] = [];
    draw(
      <OnboardingScreen
        {...base}
        step="rung"
        workspace={WS}
        onSetRung={async (r) => {
          stored.push(r);
        }}
      />,
    );
    click(Array.from(host.querySelectorAll('[role="radio"]'))[0]);
    expect(stored).toEqual(["strict"]);
  });

  it("moves off the folder step once a folder is granted", async () => {
    draw(<OnboardingScreen {...base} />);
    expect(text()).toContain("Give one folder");
    click(button("Choose a folder…"));
    await settle();
    expect(text()).toContain("Watch one file arrive");
  });

  it("stays on the folder step when the picker was cancelled", async () => {
    draw(<OnboardingScreen {...base} onChooseFolder={async () => null} />);
    click(button("Choose a folder…"));
    await settle();
    expect(text()).toContain("Give one folder");
  });

  it("reads back what the write actually did, including being stopped", async () => {
    // A write held at the gate is the product working, not a failure to hide.
    draw(
      <OnboardingScreen
        {...base}
        step="write"
        workspace={WS}
        onTestWrite={async () => ({
          status: "held",
          reason: "This write is waiting at the gate.",
        })}
      />,
    );
    click(button("Write the file"));
    await settle();
    expect(text()).toContain("waiting at the gate");
  });

  it("can be left at any step, and lands on the lane", () => {
    let went = "";
    draw(<OnboardingScreen {...base} onFinish={(s) => (went = s)} />);
    click(button("Skip the rest"));
    expect(went).toBe("lane");
  });

  it("offers both ways out of the last step", () => {
    const went: string[] = [];
    draw(
      <OnboardingScreen
        {...base}
        step="done"
        workspace={WS}
        onFinish={(s) => went.push(s)}
      />,
    );
    click(button("Open my lane"));
    click(button("Show me how to connect one"));
    expect(went).toEqual(["lane", "connect"]);
  });

  it("names every control and prints no source escape", async () => {
    for (const step of ["folder", "write", "rung", "done"] as const) {
      draw(<OnboardingScreen {...base} step={step} workspace={WS} />);
      expect(text()).not.toMatch(/\\[unxU][0-9a-fA-F{]/);
      const unusable = named().filter(
        (c) => c.name === "" || /^[^\p{L}\p{N}]+$/u.test(c.name),
      );
      expect(unusable.map((c) => c.el.outerHTML.slice(0, 70))).toEqual([]);
    }
  });
});

// ── the pairing sheet ──────────────────────────────────────────────────────

describe("the pairing sheet", () => {
  const claim = {
    request_id: "req_1",
    client_name: "ChatGPT",
    verify_code: "N5-Y9",
    created_at: "2026-08-13T10:00:00",
  };

  const key = (k: string, target: EventTarget = window) =>
    act(() => {
      target.dispatchEvent(
        new KeyboardEvent("keydown", { key: k, bubbles: true }),
      );
    });

  it("puts the verify code on screen and says what to do when it does not match", () => {
    draw(<PairClaimSheet claim={claim} onResolve={() => {}} />);
    expect(host.querySelector(".fy-sheet-code")?.textContent).toBe("N5-Y9");
    expect(text()).toContain("matches the code shown in your browser");
    expect(text()).toContain("reject the request");
  });

  // Each of these mounts fresh: rendering twice into one root keeps the
  // component's state, so the second sheet would already have decided.
  it("answers Enter with approve", () => {
    const said: boolean[] = [];
    draw(<PairClaimSheet claim={claim} onResolve={(a) => said.push(a)} />);
    key("Enter");
    expect(said).toEqual([true]);
  });

  it("answers Esc with reject", () => {
    const said: boolean[] = [];
    draw(<PairClaimSheet claim={claim} onResolve={(a) => said.push(a)} />);
    key("Escape");
    expect(said).toEqual([false]);
  });

  it("does not answer a keystroke meant for a text field", () => {
    // Enter inside an input is a submit, not an approval of a device.
    const said: boolean[] = [];
    draw(<PairClaimSheet claim={claim} onResolve={(a) => said.push(a)} />);
    const input = document.createElement("input");
    document.body.appendChild(input);
    key("Enter", input);
    expect(said).toEqual([]);
  });

  it("decides once, however many times the key is pressed", () => {
    // A second answer would resolve a request that is already gone.
    const said: boolean[] = [];
    draw(<PairClaimSheet claim={claim} onResolve={(a) => said.push(a)} />);
    key("Enter");
    key("Enter");
    key("Escape");
    expect(said).toEqual([true]);
  });

  it("stops offering a decision once one is made", () => {
    draw(<PairClaimSheet claim={claim} onResolve={() => {}} />);
    click(button("Approve connection"));
    expect(button("Reject")).toBeUndefined();
    expect(text()).toContain("can now reach this machine");
    expect(host.querySelector(".fy-sheet")?.getAttribute("data-done")).toBe(
      "approved",
    );
  });

  it("says it was rejected rather than pretending nothing happened", () => {
    draw(<PairClaimSheet claim={claim} onResolve={() => {}} />);
    click(button("Reject"));
    expect(text()).toContain("was rejected");
    expect(host.querySelector(".fy-sheet")?.getAttribute("data-done")).toBe(
      "rejected",
    );
  });

  it("names the code for a screen reader one character at a time", () => {
    // "N5-Y9" read as a word is not something anyone can compare against a
    // browser window.
    draw(<PairClaimSheet claim={claim} onResolve={() => {}} />);
    expect(
      host.querySelector(".fy-sheet-code")?.getAttribute("aria-label"),
    ).toBe("Verification code N 5 - Y 9");
  });
});

// The panel under a way in whose program is not on the machine. Four states,
// and the whole point of drawing them is that a user can tell "Fylane will
// fetch this exact thing" from "Fylane has nothing to fetch" from "the fetch
// failed and left nothing behind". All three used to be one sentence.
describe("the connect screen when a tunnel program is missing", () => {
  const OFFER = {
    binary: "cloudflared",
    version: "2026.8.2",
    source: "https://github.com/cloudflare/cloudflared/releases/download",
    sha256: "9042c2c5d8b2de78e60f313d5fb31b6c5c1cebde787a3caf1f2c9588084ac442",
  };

  function quick(over: Partial<ConnectInfo["providers"][number]> = {}) {
    return {
      kind: "cloudflare-quick",
      binary: "cloudflared",
      install: "brew install cloudflared",
      installed: false,
      needs_token: false,
      needs_hostname: false,
      stable: false,
      setup: "none" as const,
      download: "https://vendor.example/download",
      authorized: false,
      checkable: true,
      can_sign_out: false,
      opens_browser: false,
      ...over,
    };
  }

  /** Draws Settings and expands the cloudflare row, which is where the panel
   *  lives. */
  async function openRow(
    info: Partial<ConnectInfo>,
    over: Partial<SettingsDeps> = {},
  ) {
    draw(
      <SettingsScreen
        {...settingsProps}
        deps={deps({
          connect: async () => connect({ platform: "darwin/arm64", ...info }),
          ...over,
        })}
      />,
    );
    await settle();
    click(button("Set up →"));
    await settle();
  }

  it("shows what would arrive before asking, and keeps the vendor's page beside it", async () => {
    await openRow({ providers: [quick({ offer: OFFER })] });
    // The three facts the download is actually bound by. A consent screen that
    // showed fewer of them would be asking about something vaguer than what
    // happens.
    expect(text()).toContain("cloudflared 2026.8.2");
    expect(text()).toContain(
      "https://github.com/cloudflare/cloudflared/releases/download",
    );
    expect(text()).toContain(OFFER.sha256);
    expect(button("Download and verify")).toBeDefined();
    // Downloading is the exception carved out, not the default path.
    expect(button("Install it yourself")).toBeDefined();
  });

  it("says it is not installed instead of offering something it cannot honour", async () => {
    await openRow({
      platform: "linux/riscv64",
      providers: [quick()], // no offer: this build has no pin for the target
    });
    expect(text()).toContain("is not installed on this machine");
    expect(button("Download and verify")).toBeUndefined();
    expect(button("Open the download page")).toBeDefined();
  });

  it("sends the provider and nothing else when the offer is accepted", async () => {
    let asked: string | undefined;
    let payload: unknown;
    await openRow(
      { providers: [quick({ offer: OFFER })] },
      {
        startDownload: async (provider: string, ...rest: unknown[]) => {
          asked = provider;
          payload = rest;
          return connect({ providers: [quick({ offer: OFFER })] });
        },
      },
    );
    click(button("Download and verify"));
    await settle();
    expect(asked).toBe("cloudflare-quick");
    // Nothing about what to fetch travels with the press: no url, no version,
    // no digest. The Core's pin table is the only thing that decides.
    expect(payload).toEqual([]);
  });

  it("offers a way to abandon a download that is running", async () => {
    await openRow({
      providers: [quick({ offer: OFFER })],
      download: { provider: "cloudflare-quick", phase: "running" },
    });
    expect(text()).toContain("Downloading");
    expect(button("Cancel")).toBeDefined();
    // The offer's own button is gone: pressing it again during a transfer is
    // a second download the Core would refuse anyway.
    expect(button("Download and verify")).toBeUndefined();
  });

  it("says the failed download left nothing behind, and why it failed", async () => {
    await openRow({
      providers: [quick({ offer: OFFER })],
      download: {
        provider: "cloudflare-quick",
        phase: "failed",
        detail: "cloudflared-darwin-arm64.tgz failed its checksum",
      },
    });
    // "It failed" and "something unverified is now on your disk" are different
    // sentences, and this is the one that says which happened.
    expect(text()).toContain("left nothing on this machine");
    expect(text()).toContain("failed its checksum");
    expect(button("Try again")).toBeDefined();
    // Amber is for a condition the user is living in — the open rung, a
    // provider following it. A download that discarded its own file leaves no
    // condition behind, and lending amber to it would cost the open rung's
    // standing warning the one thing it needs: never reading as transient.
    expect(host.querySelector(".fy-warn")).toBeNull();
  });

  it("keeps one provider's download out from under another's row", async () => {
    await openRow({
      providers: [quick({ offer: OFFER })],
      download: { provider: "ngrok", phase: "running" },
    });
    expect(text()).not.toContain("Downloading and checking");
    expect(button("Download and verify")).toBeDefined();
  });
});

// ── remote machines (Batch R) ──────────────────────────────────────────

const VPS: MachineView = {
  info: {
    id: "m_vps1",
    name: "vps-1",
    host: "vps.example.com",
    user: "deploy",
    state: "online",
    version: "0.0.4",
    since: "2026-09-12T08:00:00",
  },
  workspaces: [
    { ...WS, id: "ws_r1", name: "api", root_path: "/home/deploy/api" },
  ],
  currentWorkspaceID: "ws_r1",
  reachable: true,
};

const BUILD_BOX: MachineView = {
  info: {
    id: "m_build",
    name: "build-box",
    host: "10.0.0.7",
    state: "missing",
    detail: "Fylane is not installed on this machine",
    reason: "missing",
    since: "2026-09-12T08:00:00",
  },
  workspaces: [],
  currentWorkspaceID: "",
  reachable: false,
};

const machineProps = {
  machines: [VPS, BUILD_BOX],
  onSelectMachine: () => {},
  onAddMachine: () => {},
  onRemoveMachine: () => {},
  onInstallMachine: () => {},
  onReconnectMachine: () => {},
  onDisconnectMachine: () => {},
};

describe("the machine anchor", () => {
  it("stands on this computer by default and lists every machine on switch", () => {
    const picked: string[] = [];
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machineID=""
        onSelectMachine={(id) => picked.push(id)}
        snapshot={snap()}
        tasks={[]}
      />,
    );
    expect(text()).toContain("MACHINE");
    expect(text()).toContain("This computer");
    expect(text()).toContain("Local · connected");
    click(button("Switch machine"));
    const items = Array.from(
      host.querySelectorAll(".fy-wsmenu .fy-wsitem"),
    ).map((b) => b.textContent);
    expect(items[0]).toContain("This computer");
    expect(items[1]).toContain("vps-1");
    expect(items[2]).toContain("build-box");
    expect(items[3]).toContain("Add a remote machine");
    click(host.querySelectorAll(".fy-wsmenu .fy-wsitem")[1] as HTMLElement);
    expect(picked).toEqual(["m_vps1"]);
  });

  it("without the feature wired, the rail is exactly what it was", () => {
    draw(<LaneScreen {...laneProps} snapshot={snap()} tasks={[]} />);
    expect(text()).not.toContain("MACHINE");
    expect(button("Switch machine")).toBeUndefined();
  });

  it("standing on a remote machine shows its folders and never a local-only verb", () => {
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machineID="m_vps1"
        workspaces={VPS.workspaces}
        snapshot={snap({ workspace: VPS.workspaces[0] })}
        tasks={[]}
      />,
    );
    expect(text()).toContain("vps-1");
    expect(host.querySelector(".fy-machine-word")?.textContent).toBe(
      "Connected",
    );
    expect(text()).toContain("api");
    // Revealing a folder in Finder only makes sense for a folder on this
    // computer; the rail must not offer it for one on the VPS.
    expect(button("Open folder")).toBeUndefined();
    expect(button("Disconnect")).toBeDefined();
  });

  it("a machine without Fylane makes the install the scene and the verb", () => {
    const installed: string[] = [];
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machineID="m_build"
        workspaces={[]}
        onInstallMachine={(id) => installed.push(id)}
        snapshot={snap({ workspace: null })}
        tasks={[]}
      />,
    );
    expect(host.querySelector(".fy-machine-word")?.textContent).toBe(
      "Fylane is not installed",
    );
    expect(text()).toContain("build-box");
    expect(text()).not.toContain("No folder has been granted");
    expect(text()).not.toContain("WORKSPACE");
    click(host.querySelector(".fy-primary") as HTMLElement);
    expect(installed).toEqual(["m_build"]);
  });

  it("tells an outdated Fylane from a missing one", () => {
    const old = {
      ...BUILD_BOX,
      info: {
        ...BUILD_BOX.info,
        detail: "this machine runs Fylane 0.0.3; this app is 0.0.4",
        version: "0.0.3",
      },
    };
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machines={[old]}
        machineID="m_build"
        workspaces={[]}
        snapshot={snap({ workspace: null })}
        tasks={[]}
      />,
    );
    expect(host.querySelector(".fy-machine-word")?.textContent).toBe(
      "Fylane needs updating",
    );
    expect(button("Update Fylane")).toBeDefined();
  });

  it("offers to edit a machine and says why it cannot connect in the window's words", () => {
    const edited: string[] = [];
    const refused = {
      ...VPS,
      info: {
        ...VPS.info,
        state: "error" as const,
        reason: "auth",
        detail: "ssh refused the login; key-based login is required",
      },
    };
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machines={[refused]}
        machineID="m_vps1"
        onEditMachine={(id) => edited.push(id)}
        snapshot={snap({ workspace: null })}
        tasks={[]}
      />,
    );
    expect(text()).toContain("It refused the login");
    expect(text()).not.toContain("key-based login is required");
    click(button("Edit"));
    expect(edited).toEqual(["m_vps1"]);
  });

  it("removing asks once more before it goes", () => {
    const removed: string[] = [];
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machineID="m_vps1"
        onRemoveMachine={(id) => removed.push(id)}
        snapshot={snap({ workspace: VPS.workspaces[0] })}
        tasks={[]}
      />,
    );
    click(button("Remove"));
    expect(removed).toEqual([]);
    click(button("Remove?"));
    expect(removed).toEqual(["m_vps1"]);
  });

  it("a request from a remote machine says which machine, wherever the rail stands", () => {
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machineID=""
        snapshot={snap({
          approvals: [
            approval({
              kind: "command",
              command: ["go", "test"],
              machine: "vps-1",
              machine_id: "m_vps1",
            }),
          ],
        })}
        tasks={[]}
      />,
    );
    expect(host.querySelector(".fy-metaline .fy-mchip")?.textContent).toBe(
      "vps-1",
    );
    expect(text()).toContain("This computer");
  });

  it("the running headline names the machine the command runs on", () => {
    draw(
      <LaneScreen
        {...laneProps}
        {...machineProps}
        machineID=""
        snapshot={snap()}
        tasks={[
          task({ state: "running", machine: "vps-1", machine_id: "m_vps1" }),
        ]}
      />,
    );
    expect(text()).toContain("Running on vps-1");
    expect(text()).not.toContain("Running on this machine");
  });
});

describe("the tasks page with other machines", () => {
  it("chips a row from another machine and leaves this computer's rows bare", () => {
    draw(
      <TasksScreen
        tasks={[
          task({
            task_id: "r",
            label: "go test ./...",
            machine: "vps-1",
            machine_id: "m_vps1",
          }),
          task({ task_id: "l", label: "npm test" }),
        ]}
        changeSets={[]}
        workspace={WS}
        now={new Date("2026-08-13T10:00:00")}
        canStop
        onCancel={() => {}}
        onRollback={() => {}}
        onAccept={() => {}}
        onCopy={() => {}}
        onGotoLane={() => {}}
      />,
    );
    const chips = Array.from(host.querySelectorAll(".fy-trow .fy-mchip")).map(
      (c) => c.textContent,
    );
    expect(chips).toEqual(["vps-1"]);
    const rows = Array.from(host.querySelectorAll(".fy-trow-cmd")).map(
      (r) => r.textContent,
    );
    expect(rows).toEqual(["vps-1go test ./...", "npm test"]);
  });
});

describe("folders on other machines on the settings page", () => {
  const vps: MachineView = {
    info: {
      id: "m_vps1",
      name: "vps-1",
      host: "vps.example.com",
      state: "online",
      since: "2026-09-12T08:00:00Z",
    },
    workspaces: [{ ...WS, id: "ws_r1", name: "api" }],
    currentWorkspaceID: "ws_r1",
    reachable: true,
  };
  const remoteFor = (log: string[]) => (id: string) => {
    if (id !== "m_vps1") throw new Error("unexpected machine " + id);
    const refuse = () => Promise.reject(new Error("not in this test"));
    return {
      status: refuse,
      workspaces: async () => ({ workspaces: vps.workspaces, currentWorkspaceID: "ws_r1" }),
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
      commandSettings: async () => ({
        rung: "workspace" as const,
        grants: [{ workspace_id: "ws_r1", rung: "workspace", granted_at: "2026-09-05T09:00:00Z" }],
      }),
      revokeGrant: async (wsID: string) => {
        log.push("revoke " + wsID);
        return { rung: "workspace" as const, grants: [] };
      },
      setNetwork: async (wsID: string, allow: boolean) => {
        log.push(`network ${wsID} ${allow}`);
        return {
          workspaces: vps.workspaces.map((w) =>
            w.id === wsID ? { ...w, network_reach: allow ? "allowed" : "denied" } : w,
          ),
          currentWorkspaceID: "ws_r1",
        };
      },
    };
  };

  it("lists them under the same headings, marked with the machine, and acts on that machine", async () => {
    const log: string[] = [];
    draw(
      <SettingsScreen
        {...settingsProps}
        machines={[vps]}
        deps={deps({
          commands: async () => ({
            rung: "workspace",
            grants: [{ workspace_id: "ws_1", rung: "workspace", granted_at: "2026-09-01T09:00:00Z" }],
          }),
          remote: remoteFor(log),
        })}
      />,
    );
    await settle();
    await settle();

    // The group headings are eyebrows, not rows.
    const heads = Array.from(host.querySelectorAll(".fy-rule-group")).map((h) => h.textContent);
    expect(heads).toContain("Folders already authorized");
    expect(heads).toContain("Outbound network");

    // The remote folder sits under both headings with the machine on it.
    const chips = Array.from(host.querySelectorAll(".fy-mchip")).map((c) => c.textContent);
    expect(chips).toEqual(["vps-1", "vps-1"]);
    expect(text()).toContain("On vps-1 · Authorized");
    expect(text()).toContain("On vps-1 · can reach the network");

    // Withdrawing and the switch go to that machine's Core, not this one's.
    const withdraws = buttons().filter((b) => (b.textContent ?? "").trim() === "Withdraw");
    expect(withdraws.length).toBe(2);
    click(withdraws[1]);
    await settle();
    expect(log).toEqual(["revoke ws_r1"]);
    expect(Array.from(host.querySelectorAll(".fy-mchip")).length).toBe(1);

    const remoteSwitch = Array.from(host.querySelectorAll('[role="switch"]')).find((el) =>
      (el.getAttribute("aria-label") ?? "").includes("On vps-1"),
    );
    click(remoteSwitch);
    await settle();
    expect(log).toEqual(["revoke ws_r1", "network ws_r1 false"]);
    expect(text()).toContain("On vps-1 · no network");
  });
});

describe("machine switch", () => {
  it("shows the mark where the click landed while the switch is on its way", () => {
    const machines: MachineView[] = [
      {
        info: {
          id: "m_1",
          name: "HK",
          host: "hk.example",
          user: "deploy",
          state: "online",
          version: "0.0.4",
          since: "2026-09-12T08:00:00Z",
        },
        workspaces: [WS],
        currentWorkspaceID: WS.id,
        reachable: true,
      },
    ];
    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap()}
        tasks={[]}
        machines={machines}
        machineID="m_1"
        onSelectMachine={() => {}}
      />,
    );
    expect(button("Switch machine")).not.toBeUndefined();
    expect(host.querySelector(".fy-rail-wait")).toBeNull();

    draw(
      <LaneScreen
        {...laneProps}
        snapshot={snap()}
        tasks={[]}
        machines={machines}
        machineID="m_1"
        onSelectMachine={() => {}}
        switchingMachine
      />,
    );
    expect(button("Switch machine")).toBeUndefined();
    expect(host.querySelector(".fy-rail-wait .fy-jelly")).not.toBeNull();
  });
});

// ── memory (Fylane-V3 board 17) ─────────────────────────────────────────

const MEM_NOW = new Date(2026, 8, 12, 14, 30);

function memDoc(over: Partial<MemoryDoc> = {}): MemoryDoc {
  return {
    state: {
      workspace_id: "ws_1",
      provider: "chatgpt",
      updated_at: new Date(2026, 8, 12, 12, 30).toISOString(),
      page: {
        goal: "Move sync to IMAP IDLE",
        progress: "Layer done",
        next: "Delete the poller",
        decisions: ["IMAP IDLE, not push", "Backoff caps at 60 s"],
        open: ["iCloud heartbeat?"],
      },
    },
    notes: [
      {
        id: 12,
        workspace_id: "ws_1",
        provider: "chatgpt",
        title: "IDLE layer passed both accounts",
        body: "Gmail and Fastmail ran 30 minutes each.",
        change_set_id: "chg_0000000000000001",
        run_id: "tsk_1",
        created_at: new Date(2026, 8, 12, 14, 2).toISOString(),
      },
      {
        id: 11,
        workspace_id: "ws_1",
        provider: "claude",
        title: "Push API dropped",
        body: "",
        created_at: new Date(2026, 8, 9, 9, 0).toISOString(),
      },
    ],
    live: 2,
    archived: 40,
    ...over,
  };
}

/** A source that remembers what it was asked and answers from a document. */
function memSource(doc: MemoryDoc, over: Partial<MemorySource> = {}) {
  const calls: { fetch: MemoryQuery[]; saved: unknown[]; deleted: number[]; cleared: number } = {
    fetch: [],
    saved: [],
    deleted: [],
    cleared: 0,
  };
  const source: MemorySource = {
    fetch: async (_ws, q) => {
      calls.fetch.push(q);
      return {
        ...doc,
        notes: doc.notes.filter(
          (n) => !!n.archived === q.archived && (!q.query || n.title.includes(q.query)),
        ),
      };
    },
    savePage: async (workspace_id, page) => {
      calls.saved.push(page);
      return { workspace_id, page, provider: "user", updated_at: MEM_NOW.toISOString() };
    },
    deleteNote: async (_ws, id) => {
      calls.deleted.push(id);
    },
    clear: async () => {
      calls.cleared++;
    },
    export: async () => "",
    ...over,
  };
  return { source, calls };
}

const memSet: ChangeSet = {
  id: "chg_0000000000000001",
  workspace_id: "ws_1",
  provider: "chatgpt",
  summary: "IDLE layer",
  operations: [
    { path: "internal/imap/idle.go", status: "created" },
    { path: "internal/imap/idle_test.go", status: "created" },
  ],
  status: "applied",
  created_at: new Date(2026, 8, 12, 14, 0).toISOString(),
};

function memoryProps(source: MemorySource) {
  return {
    workspace: WS,
    machine: "",
    source,
    changeSets: [memSet],
    tasks: [task({ label: "go test ./internal/imap", duration: 2_400_000_000 })],
    now: MEM_NOW,
    onError: () => {},
    onGotoLane: () => {},
    onGotoTasks: () => {},
    onHelp: () => {},
  };
}

describe("memory screen", () => {
  it("draws the page as five cells and the trail as one line per note", async () => {
    const { source } = memSource(memDoc());
    draw(<MemoryScreen {...memoryProps(source)} />);
    await settle();
    // The head says whose page it is and how full the trail is.
    expect(text()).toContain("2 notes, 40 archived");
    expect(text()).toContain("ChatGPT rewrote the page 2 h ago");
    // Counts are bytes for prose and items for lists, against the limit.
    expect(text()).toContain("Goal22 / 300");
    expect(text()).toContain("Decisions made2 / 8");
    expect(text()).toContain("Open questions1 / 8");
    // Today by the clock, older by the date; one recent, one earlier.
    expect(text()).toContain("14:02");
    expect(text()).toContain("09-09");
    expect(text()).toContain("RECENT1");
    expect(text()).toContain("EARLIER1");
    // The collapsed row names the source and the change set's size.
    expect(text()).toContain("ChatGPT·change set · 2 files");
    expect(host.querySelector(".fy-mem-dot-applied")).not.toBeNull();
    expect(host.querySelector(".fy-mem-dot-plain")).not.toBeNull();
  });

  it("opens a note into its body and ledger, and deletes from there", async () => {
    const { source, calls } = memSource(memDoc());
    draw(<MemoryScreen {...memoryProps(source)} />);
    await settle();
    const rows = Array.from(host.querySelectorAll(".fy-mem-rhead"));
    expect(host.querySelector('.fy-mem-row[data-open="true"]')).toBeNull();
    click(rows[0]);
    const open = host.querySelector('.fy-mem-row[data-open="true"]');
    expect(open).not.toBeNull();
    expect(open?.textContent).toContain("Gmail and Fastmail ran 30 minutes each.");
    expect(open?.textContent).toContain("2 files · idle.go and more");
    expect(open?.textContent).toContain("go test ./internal/imap");
    expect(open?.textContent).toContain("Today 14:02:00");
    click(button("Delete this note"));
    await settle();
    expect(calls.deleted).toEqual([12]);
    // The trail is read again after the delete, not patched locally.
    expect(calls.fetch.length).toBe(2);
  });

  it("asks twice before forgetting everything", async () => {
    const { source, calls } = memSource(memDoc());
    draw(<MemoryScreen {...memoryProps(source)} />);
    await settle();
    click(button("Clear memory"));
    expect(text()).toContain("Delete the page and all 42 notes?");
    click(button("Cancel"));
    expect(calls.cleared).toBe(0);
    expect(text()).not.toContain("Delete the page and all");
    click(button("Clear memory"));
    click(button("Clear"));
    await settle();
    expect(calls.cleared).toBe(1);
  });

  it("switches to the archived half and searches after a pause", async () => {
    vi.useFakeTimers();
    try {
      const { source, calls } = memSource(
        memDoc({
          notes: [
            ...memDoc().notes,
            {
              id: 3,
              workspace_id: "ws_1",
              title: "Old measurement",
              body: "",
              archived: true,
              created_at: new Date(2026, 7, 1).toISOString(),
            },
          ],
        }),
      );
      draw(<MemoryScreen {...memoryProps(source)} />);
      await settle();
      expect(text()).not.toContain("Old measurement");
      click(button("Archived40"));
      await settle();
      expect(calls.fetch.at(-1)?.archived).toBe(true);
      expect(text()).toContain("Old measurement");
      expect(text()).not.toContain("Push API dropped");

      click(button("Notes2"));
      await settle();
      const input = host.querySelector<HTMLInputElement>(".fy-mem-search input")!;
      act(() => {
        const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        set.call(input, "Push");
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      expect(calls.fetch.at(-1)?.query).toBeUndefined();
      await act(async () => {
        vi.advanceTimersByTime(300);
      });
      await settle();
      expect(calls.fetch.at(-1)?.query).toBe("Push");
      expect(text()).toContain("RESULTS1");
      expect(text()).toContain("Push API dropped");
      expect(text()).not.toContain("IDLE layer passed");
    } finally {
      vi.useRealTimers();
    }
  });

  it("edits a cell in the sheet and refuses what the Core would refuse", async () => {
    const { source, calls } = memSource(memDoc());
    draw(<MemoryScreen {...memoryProps(source)} />);
    await settle();
    const more = buttons().filter((b) => b.textContent === "View all");
    expect(more.length).toBe(5);
    click(more[3]);
    const sheet = host.querySelector('[role="dialog"]');
    expect(sheet?.textContent).toContain("Decisions made");
    expect(sheet?.textContent).toContain("IMAP IDLE, not push");
    click(button("Edit"));
    const area = host.querySelector<HTMLTextAreaElement>(".fy-mem-sheet-edit")!;
    const type = (value: string) =>
      act(() => {
        const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
        set.call(area, value);
        area.dispatchEvent(new Event("input", { bubbles: true }));
      });
    type(Array(9).fill("d").join("\n"));
    expect(text()).toContain("At most 8 items");
    expect(button("Save")?.disabled).toBe(true);
    type("IMAP IDLE, not push\n\nNo fallback switch\n");
    expect(button("Save")?.disabled).toBe(false);
    click(button("Save"));
    await settle();
    expect(calls.saved).toEqual([
      {
        goal: "Move sync to IMAP IDLE",
        progress: "Layer done",
        next: "Delete the poller",
        decisions: ["IMAP IDLE, not push", "No fallback switch"],
        open: ["iCloud heartbeat?"],
      },
    ]);
    expect(host.querySelector('[role="dialog"]')).toBeNull();
  });

  it("says when there is nothing, which machine it is on, and when it cannot read", async () => {
    const { source } = memSource({ state: null, notes: [], live: 0, archived: 0 });
    draw(<MemoryScreen {...memoryProps(source)} />);
    await settle();
    expect(text()).toContain("Nothing remembered yet");
    expect(host.querySelector(".fy-mem-search")).toBeNull();

    draw(<MemoryScreen {...memoryProps(memSource(memDoc()).source)} machine="vps-1" />);
    await settle();
    expect(host.querySelector(".fy-mchip")?.textContent).toBe("vps-1");
    expect(text()).toContain("on vps-1");

    draw(<MemoryScreen {...memoryProps(source)} workspace={null} />);
    await settle();
    expect(text()).toContain("No folder granted yet");

    const failing = memSource(memDoc(), {
      fetch: async () => {
        throw new Error("gone");
      },
    });
    draw(<MemoryScreen {...memoryProps(failing.source)} />);
    await settle();
    expect(text()).toContain("Couldn't read the memory.");
    expect(button("Retry")).not.toBeUndefined();

    // A remote Core from before this page answers 404 through the proxy:
    // that is a reason to say, not a failure to retry.
    const old = memSource(memDoc(), {
      fetch: async () => {
        throw new Error("core error (404): 404 page not found");
      },
    });
    draw(<MemoryScreen {...memoryProps(old.source)} machine="HK" />);
    await settle();
    expect(text()).toContain("Fylane on HK is older than this page.");
    expect(button("Retry")).toBeUndefined();
  });
});
