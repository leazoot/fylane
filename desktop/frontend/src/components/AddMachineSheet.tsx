import { useEffect, useRef, useState } from "react";
import type { MachineInfo, MachineRequest, ProbeResult } from "../lib/core";
import { useT, type Translator } from "../lib/i18n";

// Bringing a remote machine in, or changing how one is reached.
//
// One line, written the way it is typed after `ssh`: an alias, `user@host`,
// `host:2222`, `-p 2222`. Under it the lane's own command band echoes the
// ssh command Fylane will run, and Fylane goes knocking as soon as the line
// parses. The gate mark — the two bars that stand for the gate everywhere
// else in the window — opens when the machine answers, and one sentence says
// what is on the other side: Fylane running, Fylane installed, nothing yet,
// or why there was no answer. The decision is made with the answer on
// screen, not before it. The name Fylane shows for the machine follows the
// host until it is typed over.

export interface AddMachineSheetProps {
  /** When set, the sheet edits this machine instead of adding one. */
  editing?: MachineInfo;
  probe: (m: MachineRequest) => Promise<ProbeResult>;
  onSubmit: (m: MachineRequest) => Promise<void>;
  onCancel: () => void;
}

/** What `ssh` would be given. */
export interface Target {
  host: string;
  user?: string;
  port?: number;
}

const namePattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/** parseTarget reads an ssh destination the way a person writes one:
 *  `alias`, `user@host`, `host:2222`, `user@host -p 2222`, with or without
 *  a leading `ssh`. Null until the line names a host. */
export function parseTarget(input: string): Target | null {
  const words = input.trim().split(/\s+/).filter(Boolean);
  if (words[0] === "ssh") words.shift();
  let port: number | undefined;
  let dest = "";
  for (let i = 0; i < words.length; i++) {
    const w = words[i];
    if (w === "-p" && i + 1 < words.length) {
      port = Number(words[++i]);
      continue;
    }
    if (w.startsWith("-p") && w.length > 2) {
      port = Number(w.slice(2));
      continue;
    }
    if (w.startsWith("-")) return null;
    if (dest) return null;
    dest = w;
  }
  if (!dest) return null;
  let user: string | undefined;
  const at = dest.lastIndexOf("@");
  if (at >= 0) {
    user = dest.slice(0, at);
    dest = dest.slice(at + 1);
  }
  const colon = dest.lastIndexOf(":");
  if (colon >= 0) {
    port = Number(dest.slice(colon + 1));
    dest = dest.slice(0, colon);
  }
  if (!namePattern.test(dest)) return null;
  if (user !== undefined && !namePattern.test(user)) return null;
  if (
    port !== undefined &&
    (!Number.isInteger(port) || port < 1 || port > 65535)
  )
    return null;
  return { host: dest, user, port };
}

/** suggestName is what a machine is called until it is called something
 *  else: the alias as typed, the first label of a host name, an address
 *  as it is. */
export function suggestName(host: string): string {
  if (/^\d+(\.\d+){3}$/.test(host)) return host;
  return host.split(".")[0] || host;
}

/** targetLine is the destination as ssh would be given it, for display. */
export function targetLine(t: Target): string {
  return `${t.user ? t.user + "@" : ""}${t.host}${t.port ? ":" + t.port : ""}`;
}

function sshCommand(t: Target): string {
  return `ssh ${t.port ? `-p ${t.port} ` : ""}${t.user ? t.user + "@" : ""}${t.host}`;
}

/** reasonText puts a link failure into the window's own words when it has
 *  them, and otherwise repeats the Core's sentence. */
export function reasonText(
  tr: Translator,
  reason: string | undefined,
  detail: string | undefined,
  version = "",
): string {
  switch (reason) {
    case "missing":
      return tr.t("machine.reason.missing");
    case "outdated":
      return tr.t("machine.reason.outdated", { version });
    case "install_failed":
      return tr.t("machine.reason.install_failed");
    case "start_failed":
      return tr.t("machine.reason.start_failed");
    case "no_answer":
      return tr.t("machine.reason.no_answer");
    case "lost":
      return tr.t("machine.reason.lost");
    case "host_key":
      return tr.t("machine.reason.host_key");
    case "auth":
      return tr.t("machine.reason.auth");
    case "resolve":
      return tr.t("machine.reason.resolve");
    case "unreachable":
      return tr.t("machine.reason.unreachable");
    default:
      return detail ?? "";
  }
}

type Knock =
  | { phase: "idle" }
  | { phase: "knocking" }
  | { phase: "answered"; result: ProbeResult }
  | { phase: "silent"; result: ProbeResult };

const KNOCK_DELAY = 650;

export function AddMachineSheet({
  editing,
  probe,
  onSubmit,
  onCancel,
}: AddMachineSheetProps) {
  const tr = useT();
  const { t } = tr;
  const [line, setLine] = useState(() => (editing ? targetLine(editing) : ""));
  const [name, setName] = useState(editing?.name ?? "");
  const [named, setNamed] = useState(Boolean(editing));
  const [knock, setKnock] = useState<Knock>({ phase: "idle" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const first = useRef<HTMLInputElement>(null);
  const seq = useRef(0);
  useEffect(() => first.current?.focus(), []);

  const target = parseTarget(line);
  const shown = target ? targetLine(target) : "";

  // Knock once the line has settled. A newer line makes an older answer
  // stale, whatever order they come back in.
  useEffect(() => {
    if (!target) {
      setKnock({ phase: "idle" });
      return;
    }
    const mine = ++seq.current;
    setKnock({ phase: "knocking" });
    const id = window.setTimeout(() => {
      probe({ name: name || suggestName(target.host), ...target })
        .then((result) => {
          if (seq.current !== mine) return;
          setKnock(
            result.reachable
              ? { phase: "answered", result }
              : { phase: "silent", result },
          );
        })
        .catch((e) => {
          if (seq.current !== mine) return;
          setKnock({
            phase: "silent",
            result: {
              reachable: false,
              running: false,
              compatible: false,
              detail: e instanceof Error ? e.message : String(e),
            },
          });
        });
    }, KNOCK_DELAY);
    return () => window.clearTimeout(id);
    // The probe follows the destination only; retyping the name must not
    // knock again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown]);

  const changeLine = (v: string) => {
    setLine(v);
    if (!named) {
      const p = parseTarget(v);
      setName(p ? suggestName(p.host) : "");
    }
  };

  const complete = Boolean(target) && name.trim() !== "" && !busy;
  const submit = async () => {
    if (!complete || !target) return;
    setBusy(true);
    setError("");
    try {
      await onSubmit({ name: name.trim(), ...target });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const dot =
    knock.phase === "answered"
      ? "var(--fy-sage)"
      : knock.phase === "knocking"
        ? "var(--fy-amber)"
        : knock.phase === "silent"
          ? "var(--fy-brick)"
          : "var(--fy-faint)";

  const word = (() => {
    switch (knock.phase) {
      case "idle":
        return "";
      case "knocking":
        return t("machine.knocking");
      case "answered": {
        const r = knock.result;
        const args = { target: shown, version: r.version ?? "" };
        if (!r.version) return t("machine.answeredBare", args);
        if (!r.compatible) return t("machine.answeredOld", args);
        return t(
          r.running ? "machine.answeredRunning" : "machine.answeredInstalled",
          args,
        );
      }
      case "silent":
        return reasonText(tr, knock.result.reason, knock.result.detail);
    }
  })();

  return (
    <>
      <div className="fy-dim" onClick={onCancel} />
      <form
        className="fy-sheet fy-sheet-machine"
        role="dialog"
        aria-modal="true"
        aria-label={
          editing
            ? t("machine.editTitle", { name: editing.name })
            : t("machine.addTitle")
        }
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            onCancel();
          }
        }}
      >
        <div className="fy-sheet-top">
          <span
            className={`fy-dot fy-dot-sm${knock.phase === "knocking" ? " fy-beat" : ""}`}
            style={{ background: dot }}
          />
          <span className="fy-eyebrow">{t("machine.sheetEyebrow")}</span>
        </div>
        <div className="fy-sheet-title fy-display" style={{ fontSize: 26 }}>
          {editing
            ? t("machine.editTitle", { name: editing.name })
            : t("machine.addTitle")}
        </div>
        <div className="fy-sheet-guide">{t("machine.addBody")}</div>

        <div className="fy-sheet-form">
          <div>
            <label htmlFor="fy-machine-target">
              {t("machine.fieldTarget")}
            </label>
            <input
              id="fy-machine-target"
              ref={first}
              className="fy-field"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              placeholder={t("machine.fieldTargetPlaceholder")}
              value={line}
              disabled={busy}
              onChange={(e) => changeLine(e.target.value)}
            />
          </div>
        </div>

        {/* The command Fylane will run, in the lane's own band, with the
            gate in front of it. Open means the machine answered. */}
        <div className="fy-band fy-sheet-band">
          <span
            className="fy-gate"
            data-open={knock.phase === "answered" ? "true" : "false"}
            aria-hidden="true"
          >
            <i />
            <i />
          </span>
          <div
            className="fy-band-cmd"
            style={{ color: target ? "var(--fy-ink)" : "var(--fy-faint)" }}
          >
            {target ? sshCommand(target) : "ssh …"}
          </div>
        </div>
        <div
          className="fy-sheet-knock"
          role="status"
          aria-live="polite"
          style={{
            color:
              knock.phase === "silent"
                ? "var(--fy-brick)"
                : knock.phase === "answered"
                  ? "var(--fy-ink2)"
                  : "var(--fy-faint)",
          }}
        >
          {word}
        </div>

        <div className="fy-sheet-form" style={{ marginTop: 18 }}>
          <div style={{ maxWidth: 300 }}>
            <label htmlFor="fy-machine-name">{t("machine.fieldName")}</label>
            <input
              id="fy-machine-name"
              className="fy-field"
              style={{ fontFamily: "var(--fy-sans)" }}
              autoComplete="off"
              value={name}
              disabled={busy}
              onChange={(e) => {
                setName(e.target.value);
                setNamed(true);
              }}
            />
          </div>
        </div>

        {error && (
          <div className="fy-sheet-error" role="alert">
            {error}
          </div>
        )}
        <div className="fy-sheet-acts">
          <span style={{ flex: 1 }} />
          <button
            type="button"
            className="fy-sheet-reject"
            onClick={onCancel}
            disabled={busy}
          >
            {t("machine.cancel")}
          </button>
          <button
            type="submit"
            className="fy-sheet-approve"
            disabled={!complete}
          >
            {editing ? t("machine.saveAction") : t("machine.addAction")}
          </button>
        </div>
      </form>
    </>
  );
}
