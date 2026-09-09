import { useEffect, useRef, useState } from "react";
import type { PairClaim } from "../lib/core";
import { copyText } from "../lib/core";
import { useT } from "../lib/i18n";

// Device pairing. The browser's pairing page is
// waiting on this device, and the verify code is the whole point: approving
// without checking it links whatever asked. So the code is the largest thing
// on the layer, and the guidance says plainly what to do when it does not
// match — reject.
//
// One continuous surface. No footer band, no inset card, no second pale
// block: desktop → window → sheet is two levels, and that is all there is.
// No spinner, no pairing diagram, no success tick.

export interface PairClaimSheetProps {
  claim: PairClaim;
  onResolve: (approved: boolean) => void;
}

export function PairClaimSheet({ claim, onResolve }: PairClaimSheetProps) {
  const { t } = useT();
  const [done, setDone] = useState<"approved" | "rejected" | null>(null);
  const [copied, setCopied] = useState(false);
  const approveRef = useRef<HTMLButtonElement>(null);
  const timers = useRef<number[]>([]);
  useEffect(() => () => timers.current.forEach((id) => window.clearTimeout(id)), []);

  const resolve = (approved: boolean) => {
    if (done) return;
    setDone(approved ? "approved" : "rejected");
    // The answer goes now. What follows on screen is only what the eye sees:
    // the poll takes the claim away a moment later.
    onResolve(approved);
  };

  // Enter approves, Esc rejects — but never while someone is typing, and
  // never once the decision has been made.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (done) return;
      const el = e.target as HTMLElement | null;
      const tag = el?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || el?.isContentEditable) return;
      if (e.key === "Enter") {
        e.preventDefault();
        resolve(true);
      }
      if (e.key === "Escape") {
        e.preventDefault();
        resolve(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [done]);

  // The decision is the only thing on screen worth focusing first.
  useEffect(() => approveRef.current?.focus(), []);

  const copy = () => {
    void copyText(claim.verify_code);
    setCopied(true);
    timers.current.push(window.setTimeout(() => setCopied(false), 900));
  };

  const who = claim.client_name || t("pair.platform");

  return (
    <>
      <div className="fy-dim" />
      <div
        className="fy-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={t("pair.aria")}
        data-done={done ?? "no"}
      >
        <div className="fy-sheet-top">
          <span className="fy-sheet-who">{who}</span>
          <span style={{ flex: 1 }} />
          <span className="fy-sheet-state" aria-live="polite">
            <i
              className={done ? undefined : "fy-beat"}
              style={{
                background:
                  done === "approved"
                    ? "var(--fy-sage)"
                    : done
                      ? "var(--fy-faint)"
                      : "var(--fy-amber)",
              }}
            />
            {done === "approved"
              ? t("pair.doneApproved")
              : done === "rejected"
                ? t("pair.doneRejected")
                : t("pair.waiting")}
          </span>
        </div>

        <div className="fy-sheet-title">
          {done === "approved"
            ? t("pair.approved")
            : done === "rejected"
              ? t("pair.rejected")
              : t("pair.title")}
        </div>

        <div className="fy-sheet-coderow">
          <span
            className="fy-sheet-code"
            aria-label={t("pair.codeAria", { code: claim.verify_code.split("").join(" ") })}
          >
            {claim.verify_code}
          </span>
          {!done && (
            <button type="button" className="fy-sheet-copy" onClick={copy}>
              {copied ? t("pair.copied") : t("pair.copy")}
            </button>
          )}
        </div>

        {!done && (
          <>
            <div className="fy-sheet-guide">{t("pair.guide")}</div>
            <div className="fy-sheet-guide2">{t("pair.guide2")}</div>
          </>
        )}

        <div className="fy-sheet-acts">
          {!done && <span className="fy-sheet-keys">{t("pair.keys")}</span>}
          <span style={{ flex: 1 }} />
          {!done && (
            <button type="button" className="fy-sheet-reject" onClick={() => resolve(false)}>
              {t("pair.reject")}
            </button>
          )}
          <button
            ref={approveRef}
            type="button"
            className="fy-sheet-approve"
            onClick={() => (done ? undefined : resolve(true))}
            data-quiet={done ? "true" : "false"}
          >
            {done ? t("pair.close") : t("pair.approve")}
          </button>
        </div>
      </div>
    </>
  );
}
