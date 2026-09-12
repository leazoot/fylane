import { useState, type ReactNode } from "react";
import type { ChangeSet, TaskInfo, Workspace } from "../lib/core";
import { clock, displayWho, shortPath } from "../lib/lane";
import {
  acceptance,
  canAccept,
  canRollback,
  duration,
  entries,
  entryStatusColor,
  entryStatusWord,
  entryTitle,
  entryTone,
  filterCounts,
  FILTERS,
  groupEntries,
  isRunning,
  matchesFilter,
  outputOf,
  startedAt,
  type Acceptance,
  type Entry,
  type Filter,
} from "../lib/records";
import { formatBytes, outputFootprint } from "../lib/taskOutput";
import { useT, type Key, type Translator } from "../lib/i18n";
import { storeDensity, storedDensity, type Density } from "../lib/theme";

/** Roomy first: it is the design's own list, and the default must be the one
 *  someone gets without knowing this control exists. */
const DENSITIES: Density[] = ["comfortable", "compact"];

// The record (Fylane-V3, boards 05–07). An editorial feed rather than a
// table: time, a state dot, the command in mono, and the meta at the right
// edge. A row opens in place into the execution detail — command and output
// behind one 1px rule on the left, meta and actions on the right.
//
// It carries writes as well as commands. The design covers commands only;
// the user kept the ability to undo a write, and with the trace screen gone
// this list is the only place that ability can live.

const FILTER_LABELS: Record<Filter, Key> = {
  all: "tasksV3.all",
  running: "task.running",
  done: "task.done",
  failed: "tasksV3.notPassed",
  review: "tasksV3.toReview",
};

export interface TasksProps {
  tasks: TaskInfo[];
  changeSets: ChangeSet[];
  workspace: Workspace | null;
  now: Date;
  canStop: boolean;
  onCancel: (taskID: string) => void;
  onRollback: (changeSetID: string) => void;
  onAccept: (changeSetID: string) => void;
  onCopy: (text: string) => void;
  onGotoLane: () => void;
}

export function TasksScreen({
  tasks,
  changeSets,
  workspace,
  now,
  canStop,
  onCancel,
  onRollback,
  onAccept,
  onCopy,
  onGotoLane,
}: TasksProps) {
  const tr = useT();
  const { t, tn } = tr;
  const [open, setOpen] = useState<string | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  // Remembered on this machine only. It changes how much fits on a screen,
  // never what the list contains — every filter and count is identical in
  // both densities, so switching can never hide a task.
  const [density, setDensity] = useState<Density>(storedDensity);
  // Copy has no toast: the button says what it did for a moment and goes
  // back to its label.
  const [copied, setCopied] = useState<string | null>(null);

  const all = entries(tasks, changeSets);
  const counts = filterCounts(all);
  const rows = all.filter((r) => matchesFilter(r, filter));
  const { recent, earlier } = groupEntries(rows, now);

  const copy = (slot: string, text: string) => {
    onCopy(text);
    setCopied(slot);
    window.setTimeout(() => setCopied((c) => (c === slot ? null : c)), 1500);
  };

  const row = (entry: Entry) => (
    <Row
      key={entry.id}
      entry={entry}
      open={open === entry.id}
      copied={copied}
      workspace={workspace}
      now={now}
      canStop={canStop}
      tr={tr}
      onToggle={() => setOpen((id) => (id === entry.id ? null : entry.id))}
      onCancel={onCancel}
      onRollback={onRollback}
      onAccept={onAccept}
      onCopy={copy}
    />
  );

  return (
    <div className="fy-page">
      <div
        style={{
          flex: "none",
          display: "flex",
          alignItems: "flex-end",
          justifyContent: "space-between",
          gap: 28,
          flexWrap: "wrap",
        }}
      >
        <div>
          <h1 className="fy-display" style={{ fontSize: 28, lineHeight: 1.1 }}>
            {t("tasks.title")}
          </h1>
          <div
            style={{ marginTop: 7, fontSize: 12.5, color: "var(--fy-faint)" }}
          >
            {workspace
              ? t("record.subtitle", {
                  name: workspace.name,
                  count: tn("record.entries", all.length),
                })
              : t("record.subtitleNoFolder")}
          </div>
        </div>
        <div className="fy-filters">
          {FILTERS.map((f) => (
            <button
              key={f}
              type="button"
              className="fy-filter"
              aria-pressed={filter === f}
              onClick={() => {
                setFilter(f);
                setOpen(null);
              }}
            >
              <b>{t(FILTER_LABELS[f])}</b>
              <span>{counts[f]}</span>
            </button>
          ))}
          {/* Density sits with the filters because it belongs to the same
              question — what this list shows you — and is drawn in the
              settings page's own switch language rather than a new one. */}
          <div
            className="fy-density"
            role="group"
            aria-label={t("record.density")}
          >
            {DENSITIES.map((d) => (
              <button
                key={d}
                type="button"
                aria-pressed={density === d}
                onClick={() => {
                  setDensity(d);
                  storeDensity(d);
                }}
              >
                {t(d === "compact" ? "record.compact" : "record.roomy")}
              </button>
            ))}
          </div>
        </div>
      </div>

      {rows.length > 0 ? (
        <div className="fy-feed" data-density={density}>
          {recent.length > 0 && (
            <GroupHead text={t("record.recent")} count={recent.length} />
          )}
          {recent.map(row)}
          {earlier.length > 0 && (
            <GroupHead text={t("record.earlier")} count={earlier.length} />
          )}
          {earlier.map(row)}
        </div>
      ) : (
        <div className="fy-empty">
          <div className="fy-empty-inner">
            <span className="fy-empty-gate" aria-hidden="true">
              <i />
              <i />
            </span>
            <div style={{ maxWidth: 360 }}>
              <div
                className="fy-display"
                style={{ fontSize: 27, lineHeight: 1.2 }}
              >
                {all.length === 0
                  ? t("record.emptyTitle")
                  : t("tasksV3.emptyFilterTitle")}
              </div>
              <p
                style={{
                  margin: "13px 0 0",
                  font: "400 13.5px/1.7 var(--fy-sans)",
                  color: "var(--fy-muted)",
                  textWrap: "pretty",
                }}
              >
                {all.length === 0
                  ? t("record.emptyBody")
                  : t("tasksV3.emptyFilterBody")}
              </p>
              <button
                type="button"
                className="fy-underbtn"
                style={{ marginTop: 20 }}
                onClick={() =>
                  all.length === 0 ? onGotoLane() : setFilter("all")
                }
              >
                {all.length === 0
                  ? t("tasksV3.backToLane")
                  : t("tasksV3.showAll")}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function GroupHead({ text, count }: { text: string; count: number }) {
  return (
    <div className="fy-grouphead">
      <span>{text}</span>
      <i />
      <span>{count}</span>
    </div>
  );
}

interface RowProps {
  entry: Entry;
  open: boolean;
  copied: string | null;
  workspace: Workspace | null;
  now: Date;
  canStop: boolean;
  tr: Translator;
  onToggle: () => void;
  onCancel: (taskID: string) => void;
  onRollback: (changeSetID: string) => void;
  onAccept: (changeSetID: string) => void;
  onCopy: (slot: string, text: string) => void;
}

function Row({
  entry,
  open,
  copied,
  workspace,
  now,
  canStop,
  tr,
  onToggle,
  onCancel,
  onRollback,
  onAccept,
  onCopy,
}: RowProps) {
  const { t } = tr;
  const running = isRunning(entry);
  const tone = entryTone(entry);
  const title = entryTitle(entry, tr);
  const who =
    entry.kind === "task"
      ? displayWho(entry.task.provider ?? "")
      : displayWho(entry.set.provider);
  // The status word already says it is running; repeating it in the duration
  // slot said the same thing twice. The Core keeps a running task's duration
  // up to date, so this is the elapsed time.
  const took =
    entry.kind === "task"
      ? duration(entry.task.duration)
      : tr.tn("record.files", (entry.set.operations ?? []).length);

  return (
    <div className="fy-trow" data-open={open ? "true" : "false"}>
      <div
        className="fy-trow-head"
        role="button"
        tabIndex={0}
        aria-expanded={open}
        aria-label={open ? t("record.collapse") : t("record.expand")}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onToggle();
          }
        }}
      >
        <span className="fy-trow-time">{clock(startedISO(entry))}</span>
        <span
          className={running ? "fy-trow-dot fy-beat" : "fy-trow-dot"}
          style={{
            background:
              tone === "good"
                ? "var(--fy-sage)"
                : tone === "bad"
                  ? "var(--fy-brick)"
                  : "var(--fy-rule)",
          }}
          aria-hidden="true"
        />
        <span className="fy-trow-cmd" title={title}>
          {machineOf(entry) && (
            <span className="fy-mchip">{machineOf(entry)}</span>
          )}
          {title}
        </span>

        <span className="fy-trow-acts">
          {running && canStop && (
            <button
              type="button"
              className="fy-quiet"
              style={{ color: "var(--fy-brick)" }}
              onClick={(e) => {
                e.stopPropagation();
                onCancel(entry.id);
              }}
            >
              {t("laneV2.stop")}
            </button>
          )}
          <button
            type="button"
            className="fy-quiet"
            onClick={(e) => {
              e.stopPropagation();
              onCopy(`${entry.id}:c`, title);
            }}
          >
            {copied === `${entry.id}:c`
              ? t("record.copied")
              : t("record.copyCommand")}
          </button>
        </span>

        <span className="fy-trow-meta">
          <span style={{ color: entryStatusColor(entry) }}>
            {entryStatusWord(entry, tr)}
          </span>
          <span className="fy-trow-sep"> · </span>
          {who}
          <span className="fy-trow-sep"> · </span>
          <span style={{ fontVariantNumeric: "tabular-nums" }}>{took}</span>
        </span>
        <span className="fy-trow-chev" aria-hidden="true" />
      </div>

      {/* inert rather than hidden: display:none would kill the open
          transition, but a collapsed panel must not be in the tab order. */}
      <div className="fy-trow-panel" inert={!open}>
        {entry.kind === "task" ? (
          <TaskPanel
            task={entry.task}
            workspace={workspace}
            now={now}
            copied={copied}
            tr={tr}
            onCopy={onCopy}
          />
        ) : (
          <WritePanel
            set={entry.set}
            now={now}
            tr={tr}
            onRollback={onRollback}
            onAccept={onAccept}
          />
        )}
      </div>
      <div className="fy-trow-rule" />
    </div>
  );
}

/** The remote machine a record came from; empty for this computer, which
 *  is why the common row carries no chip at all. */
function machineOf(entry: Entry): string {
  return (entry.kind === "task" ? entry.task.machine : entry.set.machine) ?? "";
}

function startedISO(entry: Entry): string {
  return entry.kind === "task" ? entry.task.started_at : entry.set.created_at;
}

function TaskPanel({
  task,
  workspace,
  now,
  copied,
  tr,
  onCopy,
}: {
  task: TaskInfo;
  workspace: Workspace | null;
  now: Date;
  copied: string | null;
  tr: Translator;
  onCopy: (slot: string, text: string) => void;
}) {
  const { t } = tr;
  const output = outputOf(task) || task.error || "";
  const sent = outputFootprint(task);
  // The working directory is shown the way the user thinks of it — their
  // folder, then the part inside it the command ran in.
  const dir = [workspace ? shortPath(workspace.root_path) : "", task.dir]
    .filter(Boolean)
    .join("/");
  const running = task.state === "running";

  return (
    <div className="fy-tpanel">
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="fy-eyebrow">{t("record.hCommand")}</div>
        <div
          style={{
            marginTop: 8,
            font: "400 14.5px/1.55 var(--fy-mono)",
            letterSpacing: "-.015em",
            color: "var(--fy-ink)",
            wordBreak: "break-all",
          }}
        >
          {task.label?.trim() || t("task.unnamed")}
        </div>

        <div
          style={{
            display: "flex",
            alignItems: "baseline",
            justifyContent: "space-between",
            gap: 16,
            marginTop: 20,
            flexWrap: "wrap",
          }}
        >
          <span className="fy-eyebrow">{t("record.hOutput")}</span>
          {/* How much of this machine left it, and to whom. A preview without
              a size makes a whole process table look like three lines, so
              the size travels with the output, not with the row. */}
          {sent.bytes > 0 && (
            <span
              style={{
                font: `${sent.large ? 500 : 400} 11.5px/1 var(--fy-sans)`,
                color: sent.large ? "var(--fy-amber)" : "var(--fy-faint)",
              }}
            >
              {sent.provider
                ? t("tasks.sentTo", {
                    size: formatBytes(sent.bytes),
                    provider: displayWho(sent.provider),
                  })
                : t("tasks.sent", { size: formatBytes(sent.bytes) })}
            </span>
          )}
          {output && (
            <button
              type="button"
              className="fy-quiet"
              style={{ marginRight: -6 }}
              onClick={() => onCopy(`${task.task_id}:o`, output)}
            >
              {copied === `${task.task_id}:o`
                ? t("record.copied")
                : t("record.copyOutput")}
            </button>
          )}
        </div>
        {output ? (
          <pre className="fy-out">{output}</pre>
        ) : (
          <div
            style={{
              marginTop: 9,
              font: "400 12.5px/1.6 var(--fy-sans)",
              color: "var(--fy-faint)",
            }}
          >
            {t("record.noOutput")}
          </div>
        )}
      </div>

      <div className="fy-tpanel-side">
        <dl style={{ margin: 0 }}>
          <Pair k={t("record.hSource")} v={displayWho(task.provider ?? "")} />
          <Pair k={t("tasksV3.mStatus")} v={t(stateWordKey(task.state))} />
          <Pair
            k={t("tasksV3.mDuration")}
            v={running ? t("record.inFlight") : duration(task.duration)}
          />
          <Pair
            k={t("record.hStarted")}
            v={startedAt(task.started_at, now, tr)}
          />
          <Pair
            k={t("tasksV3.mExit")}
            v={running ? "—" : String(task.exit_code)}
          />
          <Pair k={t("record.hDir")} v={dir || "/"} />
        </dl>
      </div>
    </div>
  );
}

function stateWordKey(state: TaskInfo["state"]): Key {
  switch (state) {
    case "running":
      return "task.running";
    case "succeeded":
      return "task.done";
    case "failed":
      return "task.failed";
    case "timed_out":
      return "task.timedOut";
    case "interrupted":
      return "task.interrupted";
    default:
      return "task.canceled";
  }
}

function WritePanel({
  set,
  now,
  tr,
  onRollback,
  onAccept,
}: {
  set: ChangeSet;
  now: Date;
  tr: Translator;
  onRollback: (changeSetID: string) => void;
  onAccept: (changeSetID: string) => void;
}) {
  const { t } = tr;
  const ops = set.operations ?? [];
  const undoable = canRollback(set, now);
  // Acceptance sits in the meta list as a state and in the action list as a
  // verb, which is how board 06 splits this panel: what it was on the one
  // side, what you can do about it on the other.
  const review = acceptance(set, now);
  const acceptable = canAccept(set);

  return (
    <div className="fy-tpanel">
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="fy-eyebrow">{t("record.hCommand")}</div>
        <div
          style={{
            marginTop: 8,
            font: "400 14.5px/1.55 var(--fy-sans)",
            color: "var(--fy-ink)",
          }}
        >
          {set.summary || t("task.unnamed")}
        </div>
        <div style={{ marginTop: 20 }} className="fy-eyebrow">
          {t("record.hFiles")}
        </div>
        <pre className="fy-out">
          {ops.map((op) => op.path).join("\n") || "—"}
        </pre>
      </div>

      <div className="fy-tpanel-side">
        <dl style={{ margin: 0 }}>
          <Pair k={t("record.hSource")} v={displayWho(set.provider)} />
          <Pair
            k={t("tasksV3.mStatus")}
            v={entryStatusWord({ kind: "write", id: set.id, at: 0, set }, tr)}
          />
          <Pair
            k={t("record.hStarted")}
            v={startedAt(set.created_at, now, tr)}
          />
          {review !== "none" && (
            <Pair
              k={t("record.hAccepted")}
              v={reviewWord(review, set.accepted_at, tr)}
            />
          )}
        </dl>
        <div className="fy-hline" style={{ margin: "14px 0 13px" }} />
        {acceptable && (
          <button
            type="button"
            className="fy-underbtn"
            style={{ display: "block", marginBottom: 10 }}
            onClick={() => onAccept(set.id)}
          >
            {t("record.accept")}
          </button>
        )}
        <button
          type="button"
          className="fy-underbtn"
          disabled={!undoable}
          onClick={() => onRollback(set.id)}
        >
          {t("record.undo")}
        </button>
        <div
          style={{
            marginTop: 10,
            font: "400 11.5px/1.5 var(--fy-sans)",
            color: "var(--fy-faint)",
          }}
        >
          {undoable && set.rollback_deadline
            ? t("record.undoUntil", { time: clock(set.rollback_deadline) })
            : t("record.undoGone")}
        </div>
      </div>
    </div>
  );
}

// "Never reviewed" and "Accepted 14:02" are the two ends this column exists
// for; "not reviewed yet" is only the state in between.
function reviewWord(
  review: Acceptance,
  at: string | undefined,
  { t }: Translator,
): string {
  switch (review) {
    case "accepted":
      return at
        ? t("record.acceptedAt", { time: clock(at) })
        : t("record.accepted");
    case "awaiting":
      return t("record.acceptAwaiting");
    default:
      return t("record.acceptLapsed");
  }
}

function Pair({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="fy-tpanel-pair">
      <dt>{k}</dt>
      <dd>{v}</dd>
    </div>
  );
}
