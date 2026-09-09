import { useMemo, useState } from "react";
import type { TaskInfo } from "../lib/core";
import type { LaneSnapshot } from "../lib/lane";
import { taskCommand } from "../lib/lane";
import { useT } from "../lib/i18n";

// ⌘K (Desktop v2 §8). The one place the window offers a list of verbs. Items
// appear by context and there are no others: nothing here opens a screen the
// product no longer has, and nothing duplicates an action that already sits
// on a row.

export interface CommandPaletteProps {
  snapshot: LaneSnapshot;
  tasks: TaskInfo[];
  /** Whether a running task may be stopped (settings › execution). */
  canStop: boolean;
  onGoto: (screen: "lane" | "tasks" | "settings") => void;
  onClose: () => void;
  onApprove: (changeSetID: string) => void;
  onStopTask: (taskID: string) => void;
  onTogglePause: () => void;
  onChooseWorkspace: () => void;
  onStartCore: () => void;
}

interface Item {
  key: string;
  label: string;
  hint: string;
  run: () => void;
}

export function CommandPalette({
  snapshot,
  tasks,
  canStop,
  onGoto,
  onClose,
  onApprove,
  onStopTask,
  onTogglePause,
  onChooseWorkspace,
  onStartCore,
}: CommandPaletteProps) {
  const tr = useT();
  const { t } = tr;
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);

  const items = useMemo<Item[]>(() => {
    const out: Item[] = [];
    // Nothing else on this list matters while the Core is down.
    if (!snapshot.online) {
      out.push({ key: "start", label: t("cmd.start"), hint: t("cmd.hintOffline"), run: onStartCore });
    }
    // A held request is what the platform is blocked on, so it goes first.
    const held = snapshot.approvals[0];
    if (held) {
      out.push({
        key: "approve",
        label: t("cmd.approve"),
        hint: held.provider,
        run: () => onApprove(held.change_set_id),
      });
    }
    out.push({
      key: "workspace",
      label: t("cmd.workspace"),
      hint: snapshot.workspace?.name ?? t("shell.noWorkspace"),
      run: onChooseWorkspace,
    });
    if (snapshot.workspace) {
      out.push({
        key: "pause",
        label: snapshot.workspace.status === "paused" ? t("cmd.resume") : t("cmd.pause"),
        hint: t("nav.lane"),
        run: onTogglePause,
      });
    }
    out.push(
      { key: "go:lane", label: t("cmd.openLane"), hint: "⏎", run: () => onGoto("lane") },
      { key: "go:tasks", label: t("cmd.openTasks"), hint: "⏎", run: () => onGoto("tasks") },
      { key: "go:settings", label: t("cmd.openSettings"), hint: "⏎", run: () => onGoto("settings") },
    );
    const running = tasks.find((task) => task.state === "running");
    if (running && canStop) {
      out.push({
        key: "stop",
        label: t("cmd.stopTask"),
        hint: taskCommand(running, tr).split(" ")[0],
        run: () => onStopTask(running.task_id),
      });
    }
    return out;
  }, [snapshot, tasks, canStop, tr, onGoto, onApprove, onStopTask, onStartCore, onTogglePause, onChooseWorkspace]);

  const shown = items.filter((i) => i.label.toLowerCase().includes(query.trim().toLowerCase()));
  const at = Math.min(cursor, Math.max(shown.length - 1, 0));

  // Reachable and operable from the keyboard alone: arrows move, Enter runs,
  // Escape closes (handled by the window shell).
  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setCursor((c) => Math.min(c + 1, shown.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setCursor((c) => Math.max(c - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      shown[at]?.run();
    }
  }

  return (
    <>
      <button
        type="button"
        aria-label={t("cmd.close")}
        onClick={onClose}
        className="fy-scrim"
      />
      <div role="dialog" aria-label={t("cmd.title")} className="fy-palette">
        <div className="fy-palette-head">
          {/* The gate, at the size of a caret: this is the window's own list. */}
          <span className="fy-gatemark" aria-hidden="true">
            <span className="fy-gatemark-bar" style={{ height: 11, background: "var(--fy-ink)", opacity: 0.6 }} />
            <span className="fy-gatemark-bar" style={{ height: 11, background: "var(--fy-ink)", opacity: 0.6 }} />
          </span>
          <input
            autoFocus
            value={query}
            onKeyDown={onKeyDown}
            onChange={(e) => {
              setQuery(e.target.value);
              setCursor(0);
            }}
            placeholder={t("cmd.placeholder")}
            aria-label={t("cmd.search")}
          />
          <span style={{ font: "400 11.5px/1 var(--fy-sans)", color: "var(--fy-soft)" }}>
            {t("cmd.esc")}
          </span>
        </div>
        <div style={{ padding: 8, maxHeight: 320, overflowY: "auto" }}>
          {shown.length === 0 ? (
            <div style={{ padding: "18px 12px", font: "400 13px/1.4 var(--fy-sans)", color: "var(--fy-muted)" }}>
              {t("cmd.empty", { query })}
            </div>
          ) : (
            shown.map((c, i) => (
              <button
                key={c.key}
                type="button"
                className="fy-cmd-item"
                data-at={i === at ? "true" : undefined}
                onMouseEnter={() => setCursor(i)}
                onClick={c.run}
              >
                <span style={{ flex: 1, minWidth: 0 }}>{c.label}</span>
                <span style={{ flex: "none", font: "400 12px/1 var(--fy-sans)", color: "var(--fy-soft)" }}>
                  {c.hint}
                </span>
              </button>
            ))
          )}
        </div>
      </div>
    </>
  );
}
