import { useEffect, useRef, useState } from "react";
import type { MachineInfo, RemoteListing } from "../lib/core";
import { useT } from "../lib/i18n";
import { reasonText, targetLine } from "./AddMachineSheet";

// Granting a folder on a remote machine.
//
// A folder here has no picker to open, so the sheet is one: it starts in
// the machine's home, lists the folders inside, and each one steps in.
// Folders holding a repository are marked, since that is where the work
// is. The path line above the list is the folder that will be granted,
// and it can be typed into as well — `~/proj` is enough — but nothing is
// granted until the machine has confirmed the line is a folder, so a typo
// cannot be granted. Only directories are ever listed; no file is opened.

export interface RemoteFolderSheetProps {
  machine: MachineInfo;
  /** Lists the folders inside `path` on the machine; "" is its home. */
  browse: (path: string) => Promise<RemoteListing>;
  /** Grants the resolved path. Resolves when the Core accepted. */
  onSubmit: (path: string) => Promise<void>;
  onCancel: () => void;
}

type Phase =
  | { kind: "reading" }
  | { kind: "listed"; listing: RemoteListing; asked: string }
  | { kind: "refused"; reason?: string; detail?: string };

const TYPE_DELAY = 600;

function join(dir: string, name: string): string {
  return dir === "/" ? "/" + name : dir + "/" + name;
}

export function RemoteFolderSheet({
  machine,
  browse,
  onSubmit,
  onCancel,
}: RemoteFolderSheetProps) {
  const tr = useT();
  const { t, tn } = tr;
  const [line, setLine] = useState("");
  const [phase, setPhase] = useState<Phase>({ kind: "reading" });
  const [showHidden, setShowHidden] = useState(false);
  const [manual, setManual] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const field = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const seq = useRef(0);

  // look asks the machine what is at `path`. A newer ask makes an older
  // answer stale, whatever order they come back in. `settle` rewrites the
  // path line to what the machine resolved — right after a step, wrong
  // while the user is still typing.
  const look = (path: string, settle: boolean) => {
    const mine = ++seq.current;
    setPhase({ kind: "reading" });
    browse(path)
      .then((listing) => {
        if (seq.current !== mine) return;
        if (listing.reason || !listing.path) {
          setPhase({
            kind: "refused",
            reason: listing.reason,
            detail: listing.detail,
          });
          return;
        }
        setPhase({ kind: "listed", listing, asked: path });
        if (settle) setLine(listing.path);
      })
      .catch((e) => {
        if (seq.current !== mine) return;
        setPhase({
          kind: "refused",
          detail: e instanceof Error ? e.message : String(e),
        });
      });
  };

  useEffect(() => {
    look("", true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const listing = phase.kind === "listed" ? phase.listing : undefined;
  const typed = line.trim();
  // The line is confirmed when the last answer was for exactly it.
  const confirmed =
    phase.kind === "listed" &&
    (typed === phase.asked.trim() || typed === phase.listing.path);

  // A typed line is looked up once it settles.
  useEffect(() => {
    if (confirmed || typed === "") return;
    const id = window.setTimeout(() => look(typed, false), TYPE_DELAY);
    return () => window.clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [typed]);

  const step = (path: string) => look(path, true);

  // Opening the field puts the caret at the end of where the sheet stands.
  const toggleManual = (open: boolean) => {
    setManual(open);
    if (open) {
      window.setTimeout(() => {
        const el = field.current;
        if (!el) return;
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }, 0);
    }
  };

  const complete = confirmed && !busy;
  const submit = async () => {
    if (!complete || !listing?.path) return;
    setBusy(true);
    setError("");
    try {
      await onSubmit(listing.path);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const entries = listing?.entries ?? [];
  const hiddenCount = entries.filter((e) => e.hidden).length;
  const shown = entries.filter((e) => showHidden || !e.hidden);
  const atHome = listing?.home !== undefined && listing.path === listing.home;

  // Arrow keys walk the list; Enter steps into a row (its button's own
  // click), Backspace steps out.
  const onListKey = (e: React.KeyboardEvent) => {
    const rows = Array.from(
      list.current?.querySelectorAll<HTMLButtonElement>(".fy-dir") ?? [],
    );
    const at = rows.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const next = e.key === "ArrowDown" ? at + 1 : at - 1;
      rows[Math.max(0, Math.min(rows.length - 1, next))]?.focus();
    } else if (e.key === "Backspace" && listing?.parent) {
      e.preventDefault();
      step(listing.parent);
    }
  };

  const status = (() => {
    switch (phase.kind) {
      case "reading":
        return t("machine.browseReading");
      case "refused":
        return phase.reason === "nodir"
          ? t("machine.browseNoDir")
          : phase.reason === "denied"
            ? t("machine.browseDenied")
            : reasonText(tr, phase.reason, phase.detail);
      case "listed":
        return confirmed
          ? entries.length === 0
            ? t("machine.browseEmpty")
            : tn("machine.browseDirs", entries.length)
          : t("machine.browseTyping");
    }
  })();

  const dot =
    phase.kind === "reading"
      ? "var(--fy-amber)"
      : phase.kind === "refused"
        ? "var(--fy-brick)"
        : "var(--fy-sage)";

  return (
    <>
      <div className="fy-dim" onClick={onCancel} />
      <form
        className="fy-sheet fy-sheet-machine"
        role="dialog"
        aria-modal="true"
        aria-label={t("machine.folderTitle", { name: machine.name })}
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
          <span className="fy-dot fy-dot-sm" style={{ background: dot }} />
          <span className="fy-eyebrow">{targetLine(machine)}</span>
        </div>
        <div className="fy-sheet-title fy-display" style={{ fontSize: 26 }}>
          {t("machine.folderTitle", { name: machine.name })}
        </div>

        {/* Where the sheet stands, as one line. The field to type a path
            is folded under it and opens on request, so the usual way —
            stepping — is not asked to look at a blank box. */}
        <div className="fy-sheet-form">
          <div>
            <label htmlFor="fy-folder-path">{t("machine.fieldPath")}</label>
            <div className="fy-sheet-path">
              <span className="fy-sheet-path-now">
                {listing?.path ?? (phase.kind === "refused" ? typed : "")}
              </span>
              <button
                type="button"
                className="fy-textbtn"
                disabled={busy}
                onClick={() => toggleManual(!manual)}
              >
                {manual ? t("machine.browseTypeDone") : t("machine.browseType")}
              </button>
            </div>
            <div
              className="fy-sheet-path-field"
              data-open={manual ? "true" : "false"}
            >
              <div>
                <input
                  id="fy-folder-path"
                  ref={field}
                  className="fy-field"
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  placeholder={t("machine.fieldPathPlaceholder")}
                  value={line}
                  disabled={busy}
                  tabIndex={manual ? 0 : -1}
                  onChange={(e) => setLine(e.target.value)}
                />
              </div>
            </div>
          </div>
        </div>
        <div
          className="fy-sheet-knock fy-sheet-browse-status"
          role="status"
          aria-live="polite"
          style={{
            color:
              phase.kind === "refused"
                ? "var(--fy-brick)"
                : phase.kind === "listed" && confirmed
                  ? "var(--fy-ink2)"
                  : "var(--fy-faint)",
          }}
        >
          <span>
            {phase.kind === "reading" && (
              <span
                className="fy-dot fy-dot-sm fy-beat fy-sheet-browse-wait"
                aria-hidden="true"
              />
            )}
            {status}
          </span>
          {listing && confirmed && hiddenCount > 0 && (
            <button
              type="button"
              className="fy-textbtn"
              onClick={() => setShowHidden((v) => !v)}
            >
              {showHidden
                ? t("machine.browseHideHidden")
                : t("machine.browseShowHidden", { n: hiddenCount })}
            </button>
          )}
          {listing && !atHome && listing.home && (
            <button
              type="button"
              className="fy-textbtn"
              onClick={() => step(listing.home ?? "")}
            >
              {t("machine.browseHome")}
            </button>
          )}
        </div>

        {/* The folders inside. Rows are the workspace menu's rows, since a
            folder is what a workspace is; the repository mark sits where
            the menu puts a path. */}
        <div
          className="fy-sheet-dirs"
          ref={list}
          role="group"
          aria-label={listing?.path ?? ""}
          data-busy={phase.kind === "reading" ? "true" : "false"}
          onKeyDown={onListKey}
        >
          {listing?.parent && (
            <button
              type="button"
              className="fy-wsitem fy-dir fy-dir-up"
              disabled={busy}
              onClick={() => step(listing.parent ?? "")}
            >
              <span className="fy-wsitem-name">{t("machine.browseUp")}</span>
              <span className="fy-wsitem-path">..</span>
            </button>
          )}
          {shown.map((e) => (
            <button
              key={e.name}
              type="button"
              className="fy-wsitem fy-dir"
              data-repo={e.repo ? "true" : "false"}
              disabled={busy}
              onClick={() => listing && step(join(listing.path ?? "", e.name))}
            >
              <span className="fy-wsitem-name">{e.name}</span>
              {e.repo && (
                <span className="fy-wsitem-path">
                  {t("machine.browseRepo")}
                </span>
              )}
            </button>
          ))}
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
            {t("machine.folderAction")}
          </button>
        </div>
      </form>
    </>
  );
}
