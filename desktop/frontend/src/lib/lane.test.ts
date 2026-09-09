import { describe, expect, it } from "vitest";
import type { Approval, ChangeSet, Source, TaskInfo, Workspace } from "./core";
import { translatorFor } from "./i18n";
import {
  asksFor,
  humanBytes,
  impactNote,
  dayTally,
  laneBoard,
  pendingInfo,
  rowNotes,
  shortPath,
  taskDot,
  taskStateWord,
  toolOf,
  type LaneSnapshot,
  type PendingFile,
} from "./lane";

const NOW = new Date("2026-08-13T10:30:00");

const WS: Workspace = {
  id: "ws_1",
  name: "ai-workspace",
  mode: "read_write",
  exclude_rules: [],
  sensitive_rules: [],
  status: "active",
  created_at: "2026-07-01T09:00:00",
  root_path: "/Users/you/Projects/ai-workspace",
  availability: "available",
};

const SOURCES: Source[] = [
  { provider: "chatgpt", connected: true, lanes_carried: 3 },
  { provider: "claude", connected: true, lanes_carried: 1 },
  { provider: "grok", connected: false, lanes_carried: 0 },
];

function cs(over: Partial<ChangeSet> = {}): ChangeSet {
  return {
    id: "chg_1",
    workspace_id: "ws_1",
    provider: "claude",
    summary: "Update authentication handling",
    status: "applied",
    created_at: "2026-08-13T10:24:00",
    operations: [{ path: "src/auth.ts", status: "updated" }],
    ...over,
  };
}

function approval(over: Partial<Approval> = {}): Approval {
  return {
    change_set_id: "chg_pending_0001",
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

function snap(over: Partial<LaneSnapshot> = {}): LaneSnapshot {
  return {
    online: true,
    status: {
      version: "0.0.1",
      pending_approvals: 0,
      tunnel: "connected",
      approval_mode: "safe",
    },
    workspace: WS,
    approvals: [],
    changeSets: [],
    sources: SOURCES,
    ...over,
  };
}

// The wording asserted below is the English one, so these tests pin a
// fixed translator instead of whatever the window happens to be set to.
const EN = translatorFor("en");
const ZH = translatorFor("zh");

describe("pendingInfo", () => {
  it("describes the change set held at the gate", () => {
    const p = pendingInfo([
      approval({
        operations: [
          { type: "update", path: "src/auth.ts" },
          { type: "create", path: "src/errors.ts" },
          { type: "delete", path: "src/old.ts" },
        ],
      }),
    ], EN);
    expect(p).not.toBeNull();
    expect(p!.who).toBe("Claude");
    expect(p!.files.map((f) => f.op)).toEqual(["M", "+", "−"]);
  });

  it("is null when the gate is open", () => {
    expect(pendingInfo([], EN)).toBeNull();
  });

  it("reads a Core that predates the kind field as a write", () => {
    // Write is the reading that shows the most and hides nothing.
    const a = approval();
    delete (a as { kind?: unknown }).kind;
    expect(pendingInfo([a], EN)!.kind).toBe("write");
  });
});

describe("the prompt asks the question it is actually about", () => {
  const cmd = approval({
    change_set_id: "cmd:abc",
    kind: "command",
    provider: "chatgpt",
    summary: "rm -rf build",
    operations: null,
    command: ["rm", "-rf", "build"],
    dir: "packages/web",
    rule: "recursive-delete",
    reason: "removes a directory and everything under it",
  });

  it("offers no diff for a command, because there is none behind it", () => {
    // The regression: "Review changes" was the primary button on a command
    // prompt and opened a layer with nothing in it.
    const p = pendingInfo([cmd], EN)!;
    expect(p.reviewable).toBe(false);
    expect(p.files).toEqual([]);
    expect(pendingInfo([approval()], EN)!.reviewable).toBe(true);
  });

  it("carries the argv, the directory and the rule's own reason", () => {
    const p = pendingInfo([cmd], EN)!;
    expect(p.command).toBe("rm -rf build");
    expect(p.dir).toBe("packages/web");
    expect(p.reason).toBe("removes a directory and everything under it");
  });

  it("names running, writing, sending, handing over and forwarding as different asks", () => {
    // these questions must not be asked as one. The board used to say
    // "asks to run" over a disclosure and over a delegation too. A proxied
    // call joined them: it is the only ask where Fylane cannot say
    // what the operation does, so it cannot borrow another one's sentence.
    const asked = (a: Approval) => EN.t(asksFor(pendingInfo([a], EN)!.kind));
    const words = [
      asked(approval()),
      asked(cmd),
      asked(approval({ ...cmd, kind: "disclosure" })),
      asked(approval({ ...cmd, kind: "delegation" })),
      asked(approval({ ...cmd, kind: "proxy" })),
    ];
    expect(new Set(words).size).toBe(5);
  });

  it("carries the fact that approving grants the whole workspace", () => {
    // Approving the first ordinary command authorizes every later one in the
    // folder, so the board has to be able to say so.
    expect(pendingInfo([approval({ ...cmd, grant: true })], EN)!.grant).toBe(true);
    expect(pendingInfo([cmd], EN)!.grant).toBe(false);
  });

  it("shows a sensitive read as leaving, not as a modification", () => {
    const p = pendingInfo([
      approval({
        change_set_id: "read:ws_1:.env",
        kind: "disclosure",
        summary: "Read sensitive file .env",
        operations: [{ type: "read", path: ".env", sensitive: true }],
        reason: "the contents of this file would be sent to claude",
      }),
    ], EN)!;
    expect(p.files.map((f) => f.op)).toEqual(["↗"]);
    expect(p.reviewable).toBe(false);
    expect(p.tool).toBe("read_file");
  });
});


// ── Desktop v2 lane ────────────────────────────────────────────────────────

function task(over: Partial<TaskInfo> = {}): TaskInfo {
  return {
    task_id: "tsk_1",
    state: "succeeded",
    label: "go test ./...",
    exit_code: 0,
    started_at: "2026-08-13T10:00:00Z",
    duration: 12,
    ...over,
  };
}

describe("laneBoard", () => {
  it("puts a waiting request ahead of a running task", () => {
    // The platform is blocked on the user; the running task is blocked on
    // nobody, so it does not get the board.
    const board = laneBoard([approval()], [task({ state: "running" })]);
    expect(board.state).toBe("request");
  });

  it("shows the running task when nothing is waiting", () => {
    const running = task({ task_id: "tsk_run", state: "running" });
    const board = laneBoard([], [running, task()]);
    expect(board.state).toBe("running");
    expect(board.running?.task_id).toBe("tsk_run");
  });

  it("is calm with neither", () => {
    expect(laneBoard([], []).state).toBe("calm");
  });

  it("takes the last finished task for the footer, not the running one", () => {
    const board = laneBoard([], [task({ task_id: "run", state: "running" }), task({ task_id: "done" })]);
    expect(board.last?.task_id).toBe("done");
  });
});

describe("task presentation", () => {
  it("colours success and failure apart, and leaves anything open neutral", () => {
    expect(taskDot("succeeded")).toContain("sage");
    expect(taskDot("failed")).toContain("red");
    expect(taskDot("timed_out")).toContain("red");
    expect(taskDot("running")).toContain("line");
  });

  it("keeps a timeout distinct from a failure", () => {
    // The design names six states and a timeout is not among them, but the
    // fix for a timeout is not the fix for a failure — the word stays.
    expect(taskStateWord("timed_out", EN)).not.toBe(taskStateWord("failed", EN));
  });

  it("does not call an interrupted command stopped, failed, or done", () => {
    // Cancelling is something a person did. Fylane restarting under a running
    // command is not, and the difference is the whole reason the state exists:
    // nobody knows whether it finished or what it left behind. Every switch on
    // this type has a default arm, so the word silently reads "Stopped" the
    // moment someone forgets one.
    for (const other of ["canceled", "failed", "succeeded", "timed_out"] as const) {
      expect(taskStateWord("interrupted", EN)).not.toBe(taskStateWord(other, EN));
    }
    expect(taskStateWord("interrupted", ZH)).not.toBe(taskStateWord("canceled", ZH));
  });

  it("marks an interrupted command for attention without calling it a failure", () => {
    // Amber is already this product's "look at this": the held shell, a
    // paused workspace. Red would say the command failed, which is a claim
    // nobody is in a position to make.
    expect(taskDot("interrupted")).toContain("amber");
    expect(taskDot("interrupted")).not.toBe(taskDot("failed"));
    expect(taskDot("interrupted")).not.toBe(taskDot("canceled"));
  });
});

describe("toolOf", () => {
  it("names the tool from what the request is, not from which fields are set", () => {
    expect(toolOf("command", true)).toBe("run_command");
    expect(toolOf("delegation", false)).toBe("code_task");
    expect(toolOf("write", false)).toBe("write_file");
    // A proxied call arrives with a command-shaped argv (provider, tool), so
    // naming it from the populated fields would call it run_command.
    expect(toolOf("proxy", true)).toBe("mcp_gateway");
  });

  it("tells a disclosed command apart from a disclosed file", () => {
    expect(toolOf("disclosure", true)).toBe("run_command");
    expect(toolOf("disclosure", false)).toBe("read_file");
  });
});

describe("shortPath", () => {
  it("keeps the last two segments so two folders can be told apart", () => {
    expect(shortPath("/Users/you/Projects/ai-workspace")).toBe("…/Projects/ai-workspace");
  });

  it("leaves a short path alone rather than decorating it", () => {
    expect(shortPath("/tmp")).toBe("/tmp");
  });
});

describe("dayTally", () => {
  // The two numbers under the calm scene. They count today only, and they
  // count what actually happened — not what was asked for.
  const noon = new Date("2026-08-13T12:00:00");
  const today = "2026-08-13T09:00:00";
  const yesterday = "2026-08-12T09:00:00";

  it("counts a task as passed, because a task only exists once it was let through", () => {
    expect(dayTally([task({ started_at: today })], [], noon)).toEqual({ passed: 1, rejected: 0 });
  });

  it("counts a write once it landed, and still counts it after it was undone", () => {
    const sets = [cs({ created_at: today }), cs({ id: "chg_2", created_at: today, status: "rolled_back" })];
    expect(dayTally([], sets, noon).passed).toBe(2);
  });

  it("does not count a failed task as rejected — it got through, then went wrong", () => {
    const tally = dayTally(
      [task({ started_at: today, state: "failed" })],
      [cs({ created_at: today, status: "denied" })],
      noon,
    );
    expect(tally).toEqual({ passed: 1, rejected: 1 });
  });

  it("counts a refused command as rejected, and not also as passed", () => {
    // The user rejected a command and the number stayed at zero, because a
    // refusal never became a task and the tally could only see change sets
    // (reported from a real machine, 2026-08-30).
    const tally = dayTally([task({ started_at: today, state: "denied" })], [], noon);
    expect(tally).toEqual({ passed: 0, rejected: 1 });
  });

  it("leaves yesterday out of today", () => {
    const tally = dayTally(
      [task({ started_at: yesterday })],
      [cs({ created_at: yesterday, status: "denied" })],
      noon,
    );
    expect(tally).toEqual({ passed: 0, rejected: 0 });
  });

  it("ignores a timestamp it cannot read rather than counting it as today", () => {
    expect(dayTally([task({ started_at: "" })], [], noon).passed).toBe(0);
  });
});

describe("how far a change reaches", () => {
  const EN = translatorFor("en");
  const file = (impact?: PendingFile["impact"]): PendingFile => ({
    op: "M",
    path: "src/auth.ts",
    recursive: false,
    sensitive: false,
    beyondUndo: false,
    impact,
  });

  it("tells a change nobody measured from one that reaches nothing", () => {
    // These are opposite kinds of nothing, and a blank row would say both.
    expect(impactNote(file(), EN)).toBeNull();
    expect(impactNote(file({ callers: 0 }), EN)).toBe("no other callers");
  });

  it("counts in the plural only when it should", () => {
    expect(impactNote(file({ callers: 1 }), EN)).toBe("1 caller elsewhere");
    expect(impactNote(file({ callers: 12 }), EN)).toBe("12 callers elsewhere");
  });

  it("says a floor is a floor", () => {
    // A number that ran out of budget must not read as a total, and that
    // holds at zero too: nothing was counted yet, which is not "nothing uses
    // this".
    expect(impactNote(file({ callers: 8, partial: true }), EN)).toBe(
      "at least 8 callers elsewhere",
    );
    expect(impactNote(file({ callers: 0, partial: true }), EN)).toBe(
      "at least 0 callers elsewhere",
    );
  });
});

describe("what a prompt says about the network", () => {
  it("carries the Core's word for a command and a delegation", () => {
    for (const kind of ["command", "delegation"] as const) {
      const p = pendingInfo([{
          change_set_id: "chg_1",
          workspace_id: "ws_1",
          workspace_name: "work",
          provider: "claude",
          summary: "npm test",
          created_at: "2026-09-02T10:00:00",
          operations: [],
          kind,
          command: ["npm", "test"],
        network: "partial",
      }], translatorFor("en"))!;
      expect(p.network).toBe("partial");
    }
  });

  it("says nothing about the network on a write", () => {
    // A write does not reach the network. A row answering a question nobody
    // asked is the noise that costs the rows above it their reader.
    const p = pendingInfo([{
        change_set_id: "chg_1",
        workspace_id: "ws_1",
        workspace_name: "work",
        provider: "claude",
        summary: "Update auth",
        created_at: "2026-09-02T10:00:00",
        operations: [{ type: "update", path: "src/auth.ts" }],
        kind: "write",
      network: "denied",
    }], translatorFor("en"))!;
    expect(p.network).toBe("");
  });

  it("reads an older Core, which sends no such field, as nothing to say", () => {
    const p = pendingInfo([{
        change_set_id: "chg_1",
        workspace_id: "ws_1",
        workspace_name: "work",
        provider: "claude",
        summary: "npm test",
        created_at: "2026-09-02T10:00:00",
        operations: [],
        kind: "command",
      command: ["npm", "test"],
    }], translatorFor("en"))!;
    expect(p.network).toBe("");
  });
});

describe("what a recursive delete says before it is allowed", () => {
  const EN2 = translatorFor("en");
  const dir = (over: Partial<PendingFile> = {}): PendingFile => ({
    op: "D",
    path: "packages/web",
    recursive: true,
    sensitive: false,
    beyondUndo: false,
    ...over,
  });

  it("carries the size, because that is what the second click is for", () => {
    // Without a number a node_modules and a src read identically, and a
    // second press on the same words is a reflex rather than a decision.
    const notes = rowNotes(dir({ tree: { files: 1284, bytes: 5 * 1024 * 1024 } }), EN2);
    expect(notes[0]).toContain("1284 files");
    expect(notes[0]).toContain("5 MB");
  });

  it("says at least when the count did not finish", () => {
    const notes = rowNotes(dir({ tree: { files: 50000, bytes: 1 << 30, partial: true } }), EN2);
    expect(notes[0]).toContain("at least");
    expect(notes[0]).toContain("50000 files");
  });

  it("falls back to the sentence alone when nobody counted", () => {
    // No number is its own answer. A zero here would say "an empty
    // directory", which is a different claim.
    const notes = rowNotes(dir(), EN2);
    expect(notes[0]).toBe("the whole directory and everything in it");
    expect(notes[0]).not.toContain("0");
  });

  it("says when the undo will not have anything behind it", () => {
    const notes = rowNotes(dir({ tree: { files: 9, bytes: 1 << 31 }, beyondUndo: true }), EN2);
    expect(notes.join(" ")).toContain("may not come back");
  });

  it("says nothing of the sort for an ordinary delete", () => {
    expect(rowNotes(dir({ recursive: false }), EN2)).toEqual([]);
  });

  it("reads sizes the way a person does", () => {
    expect(humanBytes(0)).toBe("0 B");
    expect(humanBytes(999)).toBe("999 B");
    expect(humanBytes(1024)).toBe("1 KB");
    expect(humanBytes(1536)).toBe("1.5 KB");
    expect(humanBytes(5 * 1024 * 1024)).toBe("5 MB");
    expect(humanBytes(3 * 1024 * 1024 * 1024)).toBe("3 GB");
  });
});
