import type { ChangeSet, TaskInfo, TaskState } from "./core";
import type { Translator } from "./i18n";
import { displayWho } from "./lane";

// The tasks page's list model (Desktop v2 §6). One page answers "what has
// Fylane done in this folder", so it carries two kinds of record: a command
// that ran, and a write that landed. They share a row because they are the
// same question asked of two different actions — the write half is the
// deviation from HANDOFF §1 the user asked for, so the undo it guards stays
// reachable (recorded deviation).

export type Entry =
  | { kind: "task"; id: string; at: number; task: TaskInfo }
  | { kind: "write"; id: string; at: number; set: ChangeSet };

/** Everything that happened, newest first. */
export function entries(tasks: TaskInfo[], sets: ChangeSet[]): Entry[] {
  const rows: Entry[] = [
    ...tasks.map(
      (task): Entry => ({ kind: "task", id: task.task_id, at: msOf(task.started_at), task }),
    ),
    // A change set still waiting on the gate is on the lane, not in the
    // record: it has not happened yet.
    ...sets
      .filter((set) => set.status !== "pending" && set.status !== "approved")
      .map((set): Entry => ({ kind: "write", id: set.id, at: msOf(set.created_at), set })),
  ];
  return rows.sort((a, b) => b.at - a.at);
}

/** The design's two groups. The boundary is an hour, which is where the
 *  prototype puts it and also how long the Core keeps a finished task. */
export const RECENT_MS = 60 * 60 * 1000;

export function groupEntries(
  rows: Entry[],
  now: Date,
): { recent: Entry[]; earlier: Entry[] } {
  const edge = now.getTime() - RECENT_MS;
  return {
    recent: rows.filter((r) => r.at >= edge),
    earlier: rows.filter((r) => r.at < edge),
  };
}

/** The words above the feed (Fylane-V3 board 05 draws four of them). They are
 *  a view of the same list, not separate lists: "not passed" gathers
 *  everything that ended without completing, which is what someone opens this
 *  page to find.
 *
 *  "review" is the fifth and the one the board does not draw. It is also the
 *  only one that is not part of the partition: an applied write nobody has
 *  looked at is *also* done, so it is counted twice on purpose. The other
 *  three still add up to all, and the test that says so is what keeps this
 *  from quietly becoming four overlapping words. */
export type Filter = "all" | "running" | "done" | "failed" | "review";

export const FILTERS: Filter[] = ["all", "running", "done", "failed", "review"];

export function matchesFilter(row: Entry, filter: Filter): boolean {
  switch (filter) {
    case "all":
      return true;
    case "running":
      return isRunning(row);
    case "done":
      return entryTone(row) === "good" && !isRunning(row);
    case "failed":
      return entryTone(row) !== "good";
    case "review":
      // Only a write can be waiting on a review, and only one that landed:
      // canAccept is the same question the detail panel's own button asks, so
      // the word above the feed and the button inside it cannot disagree.
      return row.kind === "write" && canAccept(row.set);
  }
}

/** How many rows each filter would show, for the counts beside the words. */
export function filterCounts(rows: Entry[]): Record<Filter, number> {
  return {
    all: rows.length,
    running: rows.filter((r) => matchesFilter(r, "running")).length,
    done: rows.filter((r) => matchesFilter(r, "done")).length,
    failed: rows.filter((r) => matchesFilter(r, "failed")).length,
    review: rows.filter((r) => matchesFilter(r, "review")).length,
  };
}

/** Colour of a row's status square. sage succeeded, red failed, neutral for
 *  anything that ended without either (HANDOFF §3). */
export function entryDot(row: Entry): { bg: string; dot: string; line: string } {
  const tone = entryTone(row);
  switch (tone) {
    case "good":
      return { bg: "var(--fy-sageBg)", dot: "var(--fy-sage)", line: "var(--fy-sage)" };
    case "bad":
      return { bg: "var(--fy-redBg)", dot: "var(--fy-red)", line: "var(--fy-red)" };
    default:
      return { bg: "var(--fy-surf)", dot: "transparent", line: "var(--fy-soft)" };
  }
}

export type Tone = "good" | "bad" | "neutral";

export function entryTone(row: Entry): Tone {
  if (row.kind === "task") {
    switch (row.task.state) {
      case "succeeded":
      case "running":
        return "good";
      case "failed":
      case "timed_out":
        return "bad";
      default:
        return "neutral";
    }
  }
  switch (row.set.status) {
    case "applied":
      return "good";
    case "failed":
      return "bad";
    default:
      return "neutral";
  }
}

/** Colour of the status word at the right edge. Only a failure is coloured;
 *  everything else stays ink or muted so the page has one alarm, not six. */
export function entryStatusColor(row: Entry): string {
  const tone = entryTone(row);
  if (tone === "bad") return "var(--fy-red)";
  return tone === "neutral" ? "var(--fy-muted)" : "var(--fy-ink2)";
}

export function isRunning(row: Entry): boolean {
  return row.kind === "task" && row.task.state === "running";
}

/** The mono line: the command that ran, or the path that was written. */
export function entryTitle(row: Entry, { t, tn }: Translator): string {
  if (row.kind === "task") return row.task.label?.trim() || t("task.unnamed");
  const ops = row.set.operations ?? [];
  if (ops.length === 0) return row.set.summary || t("task.unnamed");
  if (ops.length === 1) return ops[0].path;
  return `${ops[0].path}  +${tn("record.more", ops.length - 1)}`;
}

/** The word at the right edge. Six for a task, five for a write, and no
 *  further one invented for either. An accepted write reads "Accepted"
 *  rather than "Written": the same landed change, said more exactly. */
export function entryStatusWord(row: Entry, { t }: Translator): string {
  if (row.kind === "task") return taskWord(row.task.state, t);
  switch (row.set.status) {
    case "applied":
      return row.set.accepted_at ? t("record.accepted") : t("record.written");
    case "rolled_back":
      return t("record.rolledBack");
    case "denied":
      return t("task.rejected");
    case "failed":
      return t("task.failed");
    default:
      return row.set.status.replace(/_/g, " ");
  }
}

function taskWord(state: TaskState, t: Translator["t"]): string {
  switch (state) {
    case "running":
      return t("task.running");
    case "succeeded":
      return t("task.done");
    case "failed":
      return t("task.failed");
    case "timed_out":
      return t("task.timedOut");
    case "interrupted":
      return t("task.interrupted");
    default:
      return t("task.canceled");
  }
}

/** The second line: who asked, how it ended, how long it took, when. */
export function entryMeta(row: Entry, now: Date, tr: Translator): string {
  const { t } = tr;
  const parts: string[] = [];
  if (row.kind === "task") {
    parts.push(displayWho(row.task.provider ?? ""));
    parts.push(taskWord(row.task.state, t));
    parts.push(row.task.state === "running" ? t("record.inFlight") : duration(row.task.duration));
  } else {
    parts.push(displayWho(row.set.provider));
    parts.push(entryStatusWord(row, tr));
    parts.push(tr.tn("record.files", (row.set.operations ?? []).length));
  }
  parts.push(agoShort(row.at, now, tr));
  return parts.join(" · ");
}

/** Elapsed time, in the unit that reads: a 60ms `git log` and an eight-minute
 *  build both have to be legible in the same column. */
export function duration(nanos: number): string {
  // The Core sends a Go time.Duration, which marshals to nanoseconds.
  const ms = Math.max(0, Math.round(nanos / 1e6));
  if (ms < 1000) return `${ms}ms`;
  const secs = ms / 1000;
  if (secs < 60) return `${secs < 10 ? secs.toFixed(1) : Math.round(secs)}s`;
  const mins = Math.floor(secs / 60);
  return `${mins}m ${Math.round(secs - mins * 60)}s`;
}

/** Duration plus exit code, the way the sheet pairs them. */
export function durationExit(task: TaskInfo, { t }: Translator): string {
  if (task.state === "running") return t("record.inFlight");
  return `${duration(task.duration)} · ${t("record.exit", { code: task.exit_code })}`;
}

/** How long ago, in the shortest true words. Exported because a standing
 *  authorization is judged by its age more than by its date: "42 d ago" is
 *  what makes someone withdraw one. */
export function agoShort(at: number, now: Date, { t }: Translator): string {
  const mins = Math.floor((now.getTime() - at) / 60000);
  if (!Number.isFinite(mins) || mins < 1) return t("record.justNow");
  if (mins < 60) return t("record.minsAgo", { n: mins });
  const hours = Math.floor(mins / 60);
  if (hours < 24) return t("record.hoursAgo", { n: hours });
  return t("record.daysAgo", { n: Math.floor(hours / 24) });
}

/** When it started, as a clock reading anchored to the day. */
export function startedAt(iso: string, now: Date, { t }: Translator): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const clock = [d.getHours(), d.getMinutes(), d.getSeconds()]
    .map((n) => String(n).padStart(2, "0"))
    .join(":");
  const days = dayIndex(now) - dayIndex(d);
  if (days === 0) return t("record.today", { time: clock });
  if (days === 1) return t("record.yesterday", { time: clock });
  const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return `${date} ${clock}`;
}

/** The full stream a command produced, stdout then stderr. */
export function outputOf(task: TaskInfo): string {
  return [task.stdout ?? "", task.stderr ?? ""].filter(Boolean).join("\n").replace(/\s+$/, "");
}

/** Whether a write can still be undone, which is the whole reason the write
 *  half of this list exists. */
export function canRollback(set: ChangeSet, now: Date): boolean {
  return (
    set.status === "applied" &&
    !!set.rollback_deadline &&
    new Date(set.rollback_deadline).getTime() > now.getTime()
  );
}

/** Where a landed write stands with the user. Derived rather than stored:
 *  `status` records what the engine did, `accepted_at` what the user did
 *  afterwards, and the undo deadline says which kind of "not yet" this is
 *  (migration 0006).
 *
 *  - `accepted`   — a human read it and said it was right
 *  - `awaiting`   — nobody has yet, and the undo window is still open
 *  - `unreviewed` — the window closed without anyone looking
 *  - `none`       — the change never landed, so there is nothing to accept
 *
 *  The distinction between the last two is the whole point of the column:
 *  a write nobody ever saw must not pass for one somebody approved of. */
export type Acceptance = "accepted" | "awaiting" | "unreviewed" | "none";

export function acceptance(set: ChangeSet, now: Date): Acceptance {
  if (set.status !== "applied") return "none";
  if (set.accepted_at) return "accepted";
  return canRollback(set, now) ? "awaiting" : "unreviewed";
}

/** Accepting stays available after the undo window closes: reviewing a write
 *  late is still reviewing it, and a dead end there would leave the record
 *  permanently unable to say what happened. */
export function canAccept(set: ChangeSet): boolean {
  return set.status === "applied" && !set.accepted_at;
}

function dayIndex(d: Date): number {
  return Math.floor(new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() / 86400000);
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function msOf(iso: string): number {
  const ms = new Date(iso).getTime();
  return Number.isNaN(ms) ? 0 : ms;
}
