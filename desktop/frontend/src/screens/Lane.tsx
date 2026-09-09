import { useEffect, useRef, useState, type CSSProperties } from "react";
import type { TaskInfo, Workspace } from "../lib/core";
import {
  asksFor,
  clock,
  dayTally,
  displayWho,
  impactNote,
  laneBoard,
  pendingInfo,
  rowNotes,
  taskCommand,
  taskDot,
  taskStateWord,
  shortPath,
  type LaneSnapshot,
  type PendingFile,
  type PendingInfo,
} from "../lib/lane";
import { duration } from "../lib/records";
import { useT, type Key, type Translator } from "../lib/i18n";
import { Jelly } from "../components/Jelly";

// The lane (Fylane-V3, boards 01–04 and 13). One focus scene, vertically
// centred, answering exactly one question: something is waiting on you,
// something is running, or nothing is. The rail on the right holds the
// standing facts and does not move between those states — switching scene
// must not reflow the page.
//
// The command is the content. It sits at 21px mono on a very light tint that
// bleeds left to the window edge: no card, and no card inside a card.

export interface LaneProps {
  snapshot: LaneSnapshot;
  tasks: TaskInfo[];
  workspaces: Workspace[];
  /** Whether a running task may be stopped (Settings › execution). */
  canStop: boolean;
  onApprove: (changeSetID: string) => void;
  onReject: (changeSetID: string) => void;
  onSelectWorkspace: (id: string) => void;
  onChooseWorkspace: () => void;
  onOpenDir: (path: string) => void;
  onStopTask: (taskID: string) => void;
  onTogglePause: () => void;
  onStartCore: () => void;
  onGotoTasks: () => void;
}

export function LaneScreen(props: LaneProps) {
  const { snapshot, tasks } = props;
  const tr = useT();
  const { t } = tr;
  const pending = pendingInfo(snapshot.approvals, tr);
  const board = laneBoard(snapshot.approvals, tasks);

  return (
    <div className="fy-lane">
      <div className="fy-lanecol">
        {pending ? (
          <Request
            key={pending.changeSetID}
            pending={pending}
            tr={tr}
            onApprove={props.onApprove}
            onReject={props.onReject}
          />
        ) : board.state === "running" && board.running ? (
          <Running
            task={board.running}
            tr={tr}
            canStop={props.canStop}
            onStop={() => props.onStopTask(board.running!.task_id)}
          />
        ) : (
          <Calm {...props} tr={tr} />
        )}

        {board.last && (
          <button type="button" className="fy-recent" onClick={props.onGotoTasks}>
            <span className="fy-eyebrow" style={{ flex: "none" }}>
              {t("laneV2.recent")}
            </span>
            <span className="fy-dot fy-dot-sm" style={{ background: taskDot(board.last.state) }} />
            <span
              style={{
                font: "400 13px/1.4 var(--fy-mono)",
                color: "var(--fy-ink2)",
                whiteSpace: "nowrap",
                overflow: "hidden",
                textOverflow: "ellipsis",
              }}
            >
              {taskCommand(board.last, tr)}
            </span>
            <span style={{ flex: "none", fontSize: 12, color: "var(--fy-faint)" }}>
              {[
                taskStateWord(board.last.state, tr),
                duration(board.last.duration),
                clock(board.last.started_at),
              ]
                .filter(Boolean)
                .join(" · ")}
            </span>
          </button>
        )}
      </div>

      <Rail {...props} tr={tr} />
    </div>
  );
}

// ── the request ────────────────────────────────────────────────────────────

function Request({
  pending,
  tr,
  onApprove,
  onReject,
}: {
  pending: PendingInfo;
  tr: Translator;
  onApprove: (id: string) => void;
  onReject: (id: string) => void;
}) {
  const { t } = tr;
  const [details, setDetails] = useState(false);
  // A recursive delete is asked twice. The second press is not on
  // the same words: the first one turns the button into the sentence that
  // names what goes, which is the only thing that makes a second click worth
  // a person's attention rather than a reflex.
  const [armed, setArmed] = useState(false);
  const wipesATree = pending.files.some((f) => f.recursive);
  // The gate opens, then the scene leaves to the right. The decision itself
  // is sent on the first frame — the platform is blocked on it and must not
  // wait for an animation.
  const [signed, setSigned] = useState<"none" | "approved" | "rejected">("none");
  const timers = useRef<number[]>([]);
  useEffect(() => () => timers.current.forEach((id) => window.clearTimeout(id)), []);

  const decide = (verdict: "approved" | "rejected") => {
    if (signed !== "none") return;
    setSigned(verdict);
    setDetails(false);
    setArmed(false);
    if (verdict === "approved") onApprove(pending.changeSetID);
    else onReject(pending.changeSetID);
  };

  const command = pending.command || pending.what;
  const busy = signed !== "none";

  return (
    <div className="fy-scene" data-leaving={busy ? "true" : "false"}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 9,
          animation: "fyFade .22s ease-out both",
        }}
      >
        <span className="fy-dot" style={{ background: "var(--fy-amber)" }} />
        <span
          style={{
            fontSize: 12,
            letterSpacing: ".02em",
            color: "var(--fy-ink2)",
          }}
        >
          {t("task.waiting")}
        </span>
        <span style={{ fontSize: 12, color: "var(--fy-rule)" }}>·</span>
        <span
          style={{
            fontSize: 12,
            color: "var(--fy-faint)",
            fontVariantNumeric: "tabular-nums",
          }}
        >
          {clock(pending.createdAt)}
        </span>
      </div>

      <h1
        className="fy-display"
        style={{
          fontSize: 31,
          lineHeight: 1.15,
          marginTop: 13,
          animation: "fyLine .3s cubic-bezier(.2,.8,.24,1) both",
        }}
      >
        {pending.who} {t(asksFor(pending.kind))}
      </h1>

      <div className="fy-band">
        <span
          className="fy-gate"
          data-open={signed === "approved" ? "true" : "false"}
          aria-hidden="true"
        >
          <i />
          <i />
        </span>
        <div
          className="fy-band-cmd"
          style={{
            animation: "fyLine .32s cubic-bezier(.2,.8,.24,1) 40ms both",
          }}
        >
          {command}
        </div>
      </div>

      <div className="fy-metaline">
        <span
          style={{
            font: "400 13px/1.4 var(--fy-mono)",
            color: "var(--fy-muted)",
          }}
        >
          {pending.dir || "/"}
        </span>
        {/* The middle slot is a risk read: the rule table's own sentence when
            it stopped this on purpose, "ordinary" for a command it did not
            stop, and nothing for a write — there is no such thing as an
            ordinary write. */}
        {(pending.rule && pending.reason) || pending.kind === "command" ? (
          <>
            <span className="fy-vline" />
            <span
              style={{
                fontSize: 12.5,
                color: pending.rule ? "var(--fy-brick)" : "var(--fy-sage)",
              }}
            >
              {pending.rule && pending.reason ? pending.reason : t("laneV3.ordinary")}
            </span>
          </>
        ) : null}
        <span className="fy-vline" />
        <span
          style={{
            font: "400 12.5px/1.4 var(--fy-mono)",
            color: "var(--fy-faint)",
          }}
        >
          {pending.tool}
        </span>
      </div>

      {details && <Detail pending={pending} tr={tr} />}

      {/* The first press opens this, the second one acts on it. The size has
          to be here rather than only in the details panel, which is collapsed
          by default — a second click on words the person has not been shown
          is the reflex this whole step exists to avoid. Amber, not
          brick: the same reading the settings page uses when it asks a second
          time before dropping the undo copies. */}
      {armed && (
        <div
          className="fy-snote"
          style={{ marginTop: 14, color: "var(--fy-amber)", maxWidth: 620 }}
        >
          {pending.files
            .filter((f) => f.recursive)
            .map((f) => `${f.path} — ${rowNotes(f, tr).join("; ")}`)
            .join("  ·  ")}
        </div>
      )}

      <div className="fy-actions">
        <button
          type="button"
          className="fy-primary"
          data-busy={busy ? "true" : "false"}
          onClick={() => (wipesATree && !armed ? setArmed(true) : decide("approved"))}
          disabled={busy}
        >
          <span>{approveWord(pending, armed, tr)}</span>
          {signed === "approved" && (
            <Jelly size={20} color="var(--fy-bg)" busyLabel={t("laneV3.allow")} />
          )}
        </button>
        <button
          type="button"
          className="fy-outline"
          onClick={() => setDetails((v) => !v)}
          disabled={busy}
        >
          {details ? t("laneV2.hideDetails") : t("laneV2.details")}
        </button>
        <span style={{ flex: 1 }} />
        <button
          type="button"
          className="fy-reject"
          onClick={() => decide("rejected")}
          disabled={busy}
        >
          {t("laneV2.reject")}
        </button>
      </div>
    </div>
  );
}

/** What the primary button says.
 *
 *  Armed, it says what it is about to do rather than "allow": a confirmation
 *  step whose two states read the same is a step people learn to click
 *  through, which is worse than not having one. */
function approveWord(pending: PendingInfo, armed: boolean, { t }: Translator): string {
  if (armed) {
    return t("laneV3.confirmDelete");
  }
  return pending.grant ? t("laneV2.allowHere") : t("laneV3.allow");
}

/** The four words readbox.Reach can produce, in the order they are read on
 *  screen. The map is here rather than inline so an unknown word from a newer
 *  Core falls back to something true rather than rendering as blank. */
const NETWORK_WORD: Record<string, Key> = {
  allowed: "set.netAllowed",
  denied: "set.netDenied",
  partial: "set.netPartial",
  unbounded: "set.netUnbounded",
};

function Detail({ pending, tr }: { pending: PendingInfo; tr: Translator }) {
  const { t } = tr;
  return (
    <dl className="fy-detail">
      <dt>{t("laneV2.mSource")}</dt>
      <dd>{t("laneV2.viaBrowser", { who: pending.who })}</dd>
      <dt>{t("laneV2.mAsked")}</dt>
      <dd>{clock(pending.createdAt)}</dd>
      <dt>{t("laneV2.mDir")}</dt>
      <dd style={{ fontFamily: "var(--fy-mono)" }}>{pending.dir || "/"}</dd>
      <dt>{t("laneV3.mBoundary")}</dt>
      <dd>{pending.reason || t("laneV2.safetyDefault")}</dd>
      {/* A statement about this run, in the panel the design already gives to
          boundaries — not on the face above it. Putting a network word on
          every command prompt would be noise, and noise is how the one
          sentence that matters loses its reader. */}
      {pending.network !== "" && (
        <>
          <dt>{t("laneV3.mNetwork")}</dt>
          <dd style={{ color: pending.network === "unbounded" ? "var(--fy-amber)" : undefined }}>
            {t(NETWORK_WORD[pending.network] ?? "set.netAllowed")}
          </dd>
        </>
      )}
      <dt>{t("laneV2.mID")}</dt>
      <dd style={{ fontFamily: "var(--fy-mono)" }}>{pending.id}</dd>

      {/* A write is the one request that has something to look at. It is
          shown here rather than behind a separate layer: a command's details
          and a write's details are the same question asked about different
          things, and one place to look is the point. */}
      {pending.reviewable && pending.files.length > 0 && (
        <>
          <dt>{t("laneV2.mChanges")}</dt>
          <dd>
            {pending.files.map((f) => (
              <div key={f.path} style={{ marginBottom: 10 }}>
                <div
                  style={{
                    display: "flex",
                    alignItems: "baseline",
                    gap: 8,
                    flexWrap: "wrap",
                  }}
                >
                  <span
                    style={{
                      font: "500 12px/1 var(--fy-mono)",
                      color: "var(--fy-faint)",
                    }}
                  >
                    {f.op}
                  </span>
                  <span
                    style={{
                      font: "400 12.5px/1.4 var(--fy-mono)",
                      wordBreak: "break-all",
                    }}
                  >
                    {f.to ? `${f.path} → ${f.to}` : f.path}
                  </span>
                  {/* Same brick-on-meta reading the line above uses for a rule
                      that stopped a command on purpose. The design has no
                      board for either flag, so this borrows the risk language
                      already on this screen rather than inventing one. */}
                  {rowNotes(f, tr).map((note) => (
                    <span
                      key={note}
                      style={{ fontSize: 12.5, color: "var(--fy-brick)" }}
                    >
                      · {note}
                    </span>
                  ))}
                  <Reach file={f} tr={tr} />
                </div>
                {f.diff && <pre className="fy-diff">{f.diff}</pre>}
              </div>
            ))}
          </dd>
        </>
      )}
    </dl>
  );
}

// ── running ────────────────────────────────────────────────────────────────

function Running({
  task,
  tr,
  canStop,
  onStop,
}: {
  task: TaskInfo;
  tr: Translator;
  canStop: boolean;
  onStop: () => void;
}) {
  const { t } = tr;
  return (
    <div className="fy-scene">
      <div style={{ display: "flex", alignItems: "center", gap: 9 }}>
        <span className="fy-dot fy-beat" style={{ background: "var(--fy-ink)" }} />
        <span
          style={{
            fontSize: 12,
            letterSpacing: ".02em",
            color: "var(--fy-ink2)",
          }}
        >
          {t("task.running")}
        </span>
      </div>

      <h1 className="fy-display" style={{ fontSize: 31, lineHeight: 1.15, marginTop: 13 }}>
        {t("laneV3.runningHeadline")}
      </h1>

      <div className="fy-band">
        <span className="fy-gate" data-open="true" aria-hidden="true">
          <i />
          <i />
        </span>
        <div className="fy-band-cmd">{taskCommand(task, tr)}</div>
      </div>

      <div className="fy-metaline">
        <span style={{ fontSize: 12.5, color: "var(--fy-muted)" }}>
          {displayWho(task.provider ?? "")}
        </span>
        <span className="fy-vline" />
        <span
          style={{
            fontSize: 12.5,
            color: "var(--fy-faint)",
            fontVariantNumeric: "tabular-nums",
          }}
        >
          {duration(task.duration)}
        </span>
      </div>

      {canStop && (
        <div className="fy-actions">
          <button type="button" className="fy-outline" onClick={onStop}>
            {t("laneV2.stop")}
          </button>
        </div>
      )}
    </div>
  );
}

// ── nothing is waiting ─────────────────────────────────────────────────────

function Calm(props: LaneProps & { tr: Translator }) {
  const { snapshot, tasks, tr } = props;
  const { t } = tr;
  const ws = snapshot.workspace;
  const paused = ws?.status === "paused";

  const scene = !snapshot.online
    ? {
        dot: "var(--fy-faint)",
        tag: t("laneV3.offlineTag"),
        title: t("laneV2.offlineTitle"),
        body: t("laneV2.offlineBody"),
      }
    : !ws
      ? {
          dot: "var(--fy-faint)",
          tag: t("laneV2.noFolder"),
          title: t("laneV3.noFolderHeadline"),
          body: t("laneV3.noFolderBody"),
        }
      : paused
        ? {
            dot: "var(--fy-faint)",
            tag: t("laneV2.pausedShort"),
            title: t("laneV2.pausedTitle"),
            body: t("laneV2.pausedBody"),
          }
        : {
            dot: "var(--fy-sage)",
            tag: t("laneV2.calmTitle"),
            title: t("laneV3.calmHeadline"),
            body: t("laneV2.calmBody"),
          };

  const tally = dayTally(tasks, snapshot.changeSets, new Date());

  return (
    <div className="fy-scene" style={{ animation: "fyLine .3s cubic-bezier(.2,.8,.24,1) both" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 9 }}>
        <span className="fy-dot" style={{ background: scene.dot }} />
        <span
          style={{
            fontSize: 12,
            letterSpacing: ".02em",
            color: "var(--fy-ink2)",
          }}
        >
          {scene.tag}
        </span>
      </div>
      <h1
        className="fy-display"
        style={{
          fontSize: 32,
          lineHeight: 1.18,
          marginTop: 13,
          maxWidth: 520,
          letterSpacing: "-.012em",
        }}
      >
        {scene.title}
      </h1>
      <p
        style={{
          margin: "14px 0 0",
          maxWidth: 430,
          font: "400 13.5px/1.7 var(--fy-sans)",
          color: "var(--fy-muted)",
          textWrap: "pretty",
        }}
      >
        {scene.body}
      </p>

      <div
        style={{
          display: "flex",
          alignItems: "flex-end",
          gap: 34,
          marginTop: 34,
          flexWrap: "wrap",
        }}
      >
        {snapshot.online && ws && (
          <>
            <div>
              <div className="fy-eyebrow" style={{ marginBottom: 6 }}>
                {t("laneV3.passedToday")}
              </div>
              <div className="fy-figure">{tally.passed}</div>
            </div>
            <div>
              <div className="fy-eyebrow" style={{ marginBottom: 6 }}>
                {t("laneV3.rejectedToday")}
              </div>
              <div className="fy-figure">{tally.rejected}</div>
            </div>
          </>
        )}
        {!snapshot.online && (
          <button type="button" className="fy-primary" onClick={props.onStartCore}>
            <span>{t("laneV2.startCore")}</span>
          </button>
        )}
        {snapshot.online && !ws && (
          <button type="button" className="fy-primary" onClick={props.onChooseWorkspace}>
            <span>{t("laneV2.chooseFolder")}</span>
          </button>
        )}
        {snapshot.online && ws && paused && (
          <button type="button" className="fy-underbtn" onClick={props.onTogglePause}>
            {t("shell.resume")}
          </button>
        )}
      </div>
    </div>
  );
}

// ── the rail ───────────────────────────────────────────────────────────────

function Rail(props: LaneProps & { tr: Translator }) {
  const { snapshot, workspaces, tr } = props;
  const { t } = tr;
  const [menu, setMenu] = useState(false);
  const ws = snapshot.workspace;
  const paused = ws?.status === "paused";

  return (
    <div className="fy-rail">
      <div className="fy-revealer" style={{ position: "relative" }}>
        <div className="fy-eyebrow" style={{ marginBottom: 9 }}>
          {t("laneV2.workspace")}
        </div>
        <div className="fy-display" style={{ fontSize: 23, lineHeight: 1.15 }}>
          {ws ? ws.name : t("shell.noWorkspace")}
        </div>
        {ws && (
          <div className="fy-reveal" style={{ "--fy-reveal-h": "44px" } as CSSProperties}>
            <div
              style={{
                paddingTop: 7,
                font: "400 12px/1.5 var(--fy-mono)",
                color: "var(--fy-muted)",
                wordBreak: "break-all",
              }}
            >
              {ws.root_path}
            </div>
          </div>
        )}
        <div
          style={{
            marginTop: 9,
            display: "flex",
            alignItems: "center",
            gap: 8,
          }}
        >
          <span
            className="fy-dot fy-dot-sm"
            style={{
              background: ws ? (paused ? "var(--fy-amber)" : "var(--fy-sage)") : "var(--fy-rule)",
            }}
          />
          <span style={{ fontSize: 11.5, color: "var(--fy-faint)" }}>
            {ws
              ? paused
                ? t("laneV2.pausedShort")
                : t("laneV2.localConnected")
              : t("laneV2.noFolder")}
          </span>
        </div>

        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 14,
            marginTop: 16,
            flexWrap: "wrap",
          }}
        >
          {workspaces.length > 0 ? (
            <button
              type="button"
              className="fy-underbtn"
              aria-expanded={menu}
              onClick={() => setMenu((v) => !v)}
            >
              {t("laneV2.switch")}
            </button>
          ) : (
            <button type="button" className="fy-underbtn" onClick={props.onChooseWorkspace}>
              {t("laneV2.chooseFolder")}
            </button>
          )}
          {ws && (
            <button
              type="button"
              className="fy-underbtn"
              onClick={() => props.onOpenDir(ws.root_path)}
            >
              {t("laneV2.openDir")}
            </button>
          )}
        </div>

        {menu && (
          <div className="fy-wsmenu">
            <div className="fy-eyebrow" style={{ padding: "7px 9px 8px" }}>
              {t("laneV2.grantedFolders")}
            </div>
            {workspaces.map((w) => (
              <button
                key={w.id}
                type="button"
                className="fy-wsitem"
                data-current={w.id === ws?.id}
                onClick={() => {
                  setMenu(false);
                  props.onSelectWorkspace(w.id);
                }}
              >
                <span
                  className="fy-dot fy-dot-sm"
                  style={{
                    background: "var(--fy-sage)",
                    opacity: w.id === ws?.id ? 1 : 0.25,
                  }}
                />
                <span className="fy-wsitem-name">{w.name}</span>
                <span className="fy-wsitem-path" title={w.root_path}>
                  {shortPath(w.root_path)}
                </span>
              </button>
            ))}
            <button type="button" className="fy-wsitem" onClick={props.onChooseWorkspace}>
              <span
                style={{
                  flex: "none",
                  width: 5,
                  textAlign: "center",
                  color: "var(--fy-faint)",
                }}
              >
                +
              </span>
              <span className="fy-wsitem-name">{t("laneV2.addFolder")}</span>
            </button>
          </div>
        )}
      </div>

      <div className="fy-hline" style={{ margin: "26px 0 20px" }} />

      <div className="fy-eyebrow" style={{ marginBottom: 14 }}>
        {t("laneV2.connectedAI")}
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 13 }}>
        {snapshot.sources.length === 0 && (
          <div style={{ fontSize: 12, color: "var(--fy-faint)" }}>{t("laneV2.notConnected")}</div>
        )}
        {snapshot.sources.map((s) => {
          const name = displayWho(s.provider);
          const asking = snapshot.approvals.some((a) => a.provider === s.provider);
          // Work in flight is not only work that stopped to ask. A command
          // running under the open rung never raises an approval, and the
          // rail stayed still while that platform was busy on this machine —
          // which is exactly the moment the dot is meant to report.
          const working = props.tasks.some(
            (k) => k.state === "running" && k.provider === s.provider,
          );
          const busy = asking || working;
          return (
            <div
              key={s.provider}
              className="fy-revealer"
              style={{ display: "flex", alignItems: "baseline", gap: 10 }}
            >
              {/* Three states, three signals. Connected is a filled green dot
                  and holds still — board 04 calls this rail static, and the
                  beat everywhere else in the window means something is
                  happening right now. It beats here for the same reason: this
                  source is asking. Not connected is a hollow ring, because two
                  filled dots a few percent apart in lightness are the same dot
                  to anyone not comparing them side by side. */}
              <span
                className={
                  s.connected
                    ? `fy-dot fy-dot-sm${busy ? " fy-beat" : ""}`
                    : "fy-dot fy-dot-sm fy-dot-hollow"
                }
                style={{
                  transform: "translateY(-2px)",
                  background: s.connected ? "var(--fy-live)" : undefined,
                }}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div
                  style={{
                    fontSize: 13,
                    color: s.connected ? "var(--fy-ink)" : "var(--fy-muted)",
                  }}
                >
                  {name}
                </div>
                {/* What a source is doing right now is not a detail at rest —
                    it is the reason the window is in front of you, so it does
                    not hide behind a hover. */}
                <div
                  className={busy ? undefined : "fy-reveal"}
                  style={{ "--fy-reveal-h": "22px" } as CSSProperties}
                >
                  <div
                    style={{
                      paddingTop: 3,
                      fontSize: 11.5,
                      color: "var(--fy-faint)",
                    }}
                  >
                    {asking
                      ? t("laneV2.asking")
                      : working
                        ? t("task.running")
                        : s.connected
                          ? t("laneV2.connected")
                          : t("laneV2.notConnected")}
                  </div>
                </div>
              </div>
            </div>
          );
        })}
      </div>

      <div style={{ flex: 1, minHeight: 20 }} />
    </div>
  );
}

/** How far a change reaches, when a language server was warm enough to say.
 *
 *  It takes the faint meta colour this screen already uses rather than the
 *  brick of the risk notes above it. Brick says "be careful"; a widely used
 *  function is not thereby dangerous to change, and letting one colour mean
 *  both would weaken the warning that has to keep working. */
function Reach({ file, tr }: { file: PendingFile; tr: Translator }) {
  const note = impactNote(file, tr);
  if (!note) return null;
  return <span style={{ fontSize: 12.5, color: "var(--fy-faint)" }}>· {note}</span>;
}
