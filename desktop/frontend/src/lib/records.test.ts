import { describe, expect, it } from "vitest";
import { translatorFor } from "./i18n";
import type { ChangeSet, TaskInfo } from "./core";
import {
  acceptance,
  canAccept,
  canRollback,
  duration,
  durationExit,
  entries,
  entryStatusWord,
  entryTitle,
  entryTone,
  filterCounts,
  groupEntries,
  matchesFilter,
  outputOf,
  startedAt,
} from "./records";

const EN = translatorFor("en");
const NOW = new Date("2026-08-13T10:00:00Z");
const MS = 1e6;

function task(over: Partial<TaskInfo> = {}): TaskInfo {
  return {
    task_id: "tsk_1",
    state: "succeeded",
    label: "go test ./...",
    exit_code: 0,
    started_at: "2026-08-13T09:58:00Z",
    duration: 8400 * MS,
    ...over,
  };
}

function set(over: Partial<ChangeSet> = {}): ChangeSet {
  return {
    id: "chg_1",
    workspace_id: "ws_1",
    provider: "claude",
    summary: "Update auth",
    operations: [{ path: "src/auth.ts", status: "updated" }],
    status: "applied",
    created_at: "2026-08-13T09:50:00Z",
    ...over,
  };
}

describe("the record list", () => {
  it("puts commands and writes in one list, newest first", () => {
    const rows = entries(
      [task({ started_at: "2026-08-13T09:30:00Z" })],
      [set({ created_at: "2026-08-13T09:50:00Z" })],
    );
    expect(rows.map((r) => r.kind)).toEqual(["write", "task"]);
  });

  it("leaves a change set that is still at the gate out of the record", () => {
    // It has not happened yet — it is on the lane, waiting for a decision.
    expect(entries([], [set({ status: "pending" })])).toEqual([]);
    expect(entries([], [set({ status: "approved" })])).toEqual([]);
  });

  it("splits the last hour from everything before it", () => {
    const rows = entries(
      [
        task({ task_id: "a", started_at: "2026-08-13T09:35:00Z" }),
        task({ task_id: "b", started_at: "2026-08-13T08:41:00Z" }),
      ],
      [],
    );
    const { recent, earlier } = groupEntries(rows, NOW);
    expect(recent.map((r) => r.id)).toEqual(["a"]);
    expect(earlier.map((r) => r.id)).toEqual(["b"]);
  });
});

describe("what a row says", () => {
  it("names a write by its path, and counts the rest", () => {
    const one = entries([], [set()])[0];
    expect(entryTitle(one, EN)).toBe("src/auth.ts");
    const many = entries(
      [],
      [
        set({
          operations: [
            { path: "src/auth.ts", status: "updated" },
            { path: "src/errors.ts", status: "created" },
            { path: "src/index.ts", status: "updated" },
          ],
        }),
      ],
    )[0];
    expect(entryTitle(many, EN)).toContain("src/auth.ts");
    expect(entryTitle(many, EN)).toContain("2 more");
  });

  it("colours only a failure, so the page has one alarm", () => {
    expect(entryTone(entries([task({ state: "failed" })], [])[0])).toBe("bad");
    expect(entryTone(entries([task({ state: "timed_out" })], [])[0])).toBe("bad");
    expect(entryTone(entries([task({ state: "canceled" })], [])[0])).toBe("neutral");
    expect(entryTone(entries([task()], [])[0])).toBe("good");
  });

  it("gives an undone write its own word rather than calling it written", () => {
    const undone = entries([], [set({ status: "rolled_back" })])[0];
    const written = entries([], [set()])[0];
    expect(entryStatusWord(undone, EN)).not.toBe(entryStatusWord(written, EN));
  });
});

describe("durations", () => {
  // The Core sends a Go time.Duration, which marshals to nanoseconds. Reading
  // it as seconds turned a 60ms command into a minute-long one.
  it("reads the Core's nanoseconds", () => {
    expect(duration(60 * MS)).toBe("60ms");
    expect(duration(8400 * MS)).toBe("8.4s");
    expect(duration(34_000 * MS)).toBe("34s");
    expect(duration(125_000 * MS)).toBe("2m 5s");
  });

  it("does not claim a running task has an exit code", () => {
    expect(durationExit(task({ state: "running" }), EN)).not.toContain("exit");
    expect(durationExit(task({ exit_code: 1, state: "failed" }), EN)).toContain("exit 1");
  });
});

describe("the sheet", () => {
  it("anchors the start time to the day", () => {
    // Offsets from NOW rather than fixed instants: "yesterday" is a local-time
    // question, and a UTC literal answers it differently in some zones.
    const back = (hours: number) => new Date(NOW.getTime() - hours * 3600_000).toISOString();
    expect(startedAt(back(1), NOW, EN)).toContain("Today");
    expect(startedAt(back(24), NOW, EN)).toContain("Yesterday");
    expect(startedAt(back(24 * 40), NOW, EN)).toMatch(/^\d{4}-\d{2}-\d{2} /);
  });

  it("shows both streams, because a failure usually only wrote to one", () => {
    expect(outputOf(task({ stdout: "building", stderr: "boom" }))).toBe("building\nboom");
    expect(outputOf(task({}))).toBe("");
  });
});

describe("undo", () => {
  it("offers the undo only while the window is open and the write stands", () => {
    const open = set({ rollback_deadline: "2026-08-13T11:00:00Z" });
    expect(canRollback(open, NOW)).toBe(true);
    expect(canRollback({ ...open, rollback_deadline: "2026-08-13T09:00:00Z" }, NOW)).toBe(false);
    expect(canRollback({ ...open, status: "rolled_back" }, NOW)).toBe(false);
    expect(canRollback(set(), NOW)).toBe(false);
  });
});

describe("the filter row", () => {
  // Four words over one list, not four lists. "Not passed" is the one a
  // person actually scans for, so it gathers everything that did not run to
  // completion — failed, timed out, stopped, rejected.
  const rows = entries(
    [
      task({ task_id: "a", state: "succeeded" }),
      task({ task_id: "b", state: "running" }),
      task({ task_id: "c", state: "failed" }),
      task({ task_id: "d", state: "timed_out" }),
      task({ task_id: "e", state: "canceled" }),
    ],
    [set({ id: "w1", status: "applied" }), set({ id: "w2", status: "denied" })],
  );

  it("counts every row under all", () => {
    expect(filterCounts(rows).all).toBe(7);
  });

  it("keeps a running task out of done and out of not-passed", () => {
    // It has not ended, so neither word is true of it yet.
    const running = rows.filter((r) => matchesFilter(r, "running"));
    expect(running).toHaveLength(1);
    expect(matchesFilter(running[0], "done")).toBe(false);
    expect(matchesFilter(running[0], "failed")).toBe(false);
  });

  it("gathers everything that ended without completing under not-passed", () => {
    const counts = filterCounts(rows);
    expect(counts.done).toBe(2); // the succeeded task and the applied write
    expect(counts.failed).toBe(4); // failed, timed out, stopped, denied
  });

  it("adds up: every row is in exactly one of running, done and not-passed", () => {
    // "review" is deliberately not in this sum. It is a cross-cut, and this
    // assertion is what stops it from quietly turning the partition into four
    // overlapping words.
    const counts = filterCounts(rows);
    expect(counts.running + counts.done + counts.failed).toBe(counts.all);
  });

  it("counts an applied write nobody reviewed under both done and to-review", () => {
    const counts = filterCounts(rows);
    expect(counts.review).toBe(1); // w1, applied and never accepted
    // The same row is still done. Making them exclusive would move a landed
    // write out of "done" for no reason other than that nobody had read it.
    const write = rows.find((r) => r.kind === "write" && r.set.id === "w1");
    expect(write).toBeDefined();
    expect(matchesFilter(write!, "done")).toBe(true);
    expect(matchesFilter(write!, "review")).toBe(true);
  });

  it("leaves a reviewed write, a rejected one and every command out of to-review", () => {
    const reviewed = entries(
      [],
      [set({ id: "w1", status: "applied", accepted_at: "2026-08-13T09:55:00Z" })],
    );
    expect(filterCounts(reviewed).review).toBe(0);

    // A write that never landed has nothing to review, whatever its status.
    const denied = entries([], [set({ id: "w2", status: "denied" })]);
    expect(filterCounts(denied).review).toBe(0);

    // A command is not a write. Nothing about it is waiting on a reading.
    const ran = entries([task({ task_id: "a", state: "succeeded" })], []);
    expect(filterCounts(ran).review).toBe(0);
  });

  it("asks the same question the accept button asks", () => {
    // The word above the feed and the button inside the panel must not be
    // able to disagree: one saying there is something to review while the
    // other offers no way to do it is the worst of both.
    for (const row of rows) {
      if (row.kind !== "write") {
        continue;
      }
      expect(matchesFilter(row, "review")).toBe(canAccept(row.set));
    }
  });
});

describe("acceptance", () => {
  const open = "2026-08-13T16:00:00Z"; // the undo window is still running
  const shut = "2026-08-13T09:00:00Z"; // it closed an hour ago

  it("tells a write nobody reviewed in time apart from one nobody has reviewed yet", () => {
    // This distinction is the entire reason the column exists. Before it, a
    // write whose window quietly expired was indistinguishable from one a
    // person had read and approved of.
    expect(acceptance(set({ rollback_deadline: open }), NOW)).toBe("awaiting");
    expect(acceptance(set({ rollback_deadline: shut }), NOW)).toBe("unreviewed");
  });

  it("says accepted once a human has said so, whichever side of the window", () => {
    const at = "2026-08-13T09:55:00Z";
    expect(acceptance(set({ rollback_deadline: open, accepted_at: at }), NOW)).toBe("accepted");
    expect(acceptance(set({ rollback_deadline: shut, accepted_at: at }), NOW)).toBe("accepted");
  });

  it("has nothing to say about a change that never landed", () => {
    for (const status of ["pending", "approved", "denied", "failed", "rolled_back"]) {
      expect(acceptance(set({ status, rollback_deadline: open }), NOW)).toBe("none");
      expect(canAccept(set({ status }))).toBe(false);
    }
  });

  it("does not let the undo window decide whether a write can be reviewed", () => {
    // Reviewing a write late is still reviewing it; a dead end here would
    // leave the record permanently unable to say what happened. That holds
    // for a closed window and for a write with no window on record at all —
    // backups cleared, or a row that predates the deadline.
    expect(canAccept(set({ rollback_deadline: shut }))).toBe(true);
    expect(canAccept(set({ rollback_deadline: open }))).toBe(true);
    expect(canAccept(set())).toBe(true);
    expect(canAccept(set({ accepted_at: "2026-08-13T09:55:00Z" }))).toBe(false);
  });

  it("leaves the undo alone: accepting is not giving up the ability to undo", () => {
    const accepted = set({ rollback_deadline: open, accepted_at: "2026-08-13T09:55:00Z" });
    expect(canRollback(accepted, NOW)).toBe(true);
  });

  it("says Accepted rather than Written at the right edge once it is", () => {
    const rows = entries([], [set({ accepted_at: "2026-08-13T09:55:00Z" })]);
    expect(entryStatusWord(rows[0], EN)).toBe("Accepted");
    expect(entryStatusWord(entries([], [set()])[0], EN)).toBe("Written");
    // Still a landed write, so still the same tone — the word got more
    // exact, the meaning did not change.
    expect(entryTone(rows[0])).toBe("good");
  });
});
