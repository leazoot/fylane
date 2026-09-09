import { useState, type CSSProperties } from "react";
import type { CommandRung, Source, Workspace } from "../lib/core";
import { setCommandRung } from "../lib/core";
import { FIRST_FILE, type FirstWriteOutcome } from "../lib/firstwrite";
import { useT, type Key } from "../lib/i18n";

// First run (Fylane-V3). It uses the lane's own skeleton — focus scene on the
// left, a rail of standing facts on the right — so the first screen a new user
// sees and the screen they live on afterwards are visibly one product. Nothing
// here is a new component: the band, the gate, the rung rows and the buttons
// are the ones the other three screens already draw.
//
// Four steps, not v1's six: "in transit" and "arrived" were two frames of one
// action, and counting an animation as a step made the walkthrough look a
// third longer than it is.

export type FirstRunStep = "folder" | "write" | "rung" | "done";

const ORDER: FirstRunStep[] = ["folder", "write", "rung", "done"];

const STEPS: { key: FirstRunStep; label: Key }[] = [
  { key: "folder", label: "fr.s1" },
  { key: "write", label: "fr.s2" },
  { key: "rung", label: "fr.s3" },
  { key: "done", label: "fr.s4" },
];

// Only the two rungs that still ask. The open rung needs an explicit
// acknowledgement and leaves a standing warning on the settings page;
// first run is not where someone should be nudged into turning approvals off,
// so it is named as existing and left there.
const RUNGS: { key: CommandRung; label: Key; note: Key }[] = [
  { key: "strict", label: "set.rungStrict", note: "set.rungStrictNote" },
  { key: "workspace", label: "set.rungWorkspace", note: "set.rungWorkspaceNote" },
];

export interface OnboardingProps {
  /** The granted folder, once one exists. */
  workspace: Workspace | null;
  /** Real per-source connection state, for the last step. */
  sources: Source[];
  /** Opens the native folder picker. null means the user cancelled. */
  onChooseFolder: () => Promise<Workspace | null>;
  /** Sends the one real test file down the lane. */
  onTestWrite: (workspaceID: string) => Promise<FirstWriteOutcome>;
  /** Leaves first run for good, landing on the named screen. */
  onFinish: (screen: "lane" | "connect") => void;
  /** Stores the chosen rung. Injected so the harness can draw this screen
   *  without a Core, the way the settings page's readers are. */
  onSetRung?: (rung: CommandRung) => Promise<unknown>;
  /** Opening step. The app always starts at the beginning; the harness uses
   *  this to render one step for a screenshot. */
  step?: FirstRunStep;
}

export function OnboardingScreen({
  workspace,
  sources,
  onChooseFolder,
  onTestWrite,
  onFinish,
  onSetRung = (rung) => setCommandRung(rung, false),
  step: initial,
}: OnboardingProps) {
  const { t, tn } = useT();
  const [step, setStep] = useState<FirstRunStep>(initial ?? "folder");
  const [granted, setGranted] = useState<Workspace | null>(null);
  const [busy, setBusy] = useState(false);
  const [receipt, setReceipt] = useState("");
  const [rung, setRung] = useState<CommandRung>("workspace");

  const ws = granted ?? workspace;
  const at = ORDER.indexOf(step);
  const connected = sources.filter((s) => s.connected);

  const choose = async () => {
    setBusy(true);
    try {
      const picked = await onChooseFolder();
      if (picked) {
        setGranted(picked);
        setStep("write");
      }
    } finally {
      setBusy(false);
    }
  };

  const write = async () => {
    if (!ws) return;
    setBusy(true);
    try {
      const outcome = await onTestWrite(ws.id);
      // Held or refused is still an answer, and it is one worth reading: the
      // gate stopping the very first write is the product working.
      setReceipt(
        outcome.status === "applied" ? t("fr.wrote", { path: outcome.path }) : outcome.reason,
      );
    } finally {
      setBusy(false);
      setStep("rung");
    }
  };

  const pick = (next: CommandRung) => {
    setRung(next);
    // Best effort: a rung the Core refused is reported on the settings page,
    // and stopping first run on it would strand someone at step three.
    void onSetRung(next).catch(() => {});
  };

  return (
    <div className="fr">
      <div className="fr-col">
        <div className="fr-scene" key={step}>
          <div className="fr-tag">
            <span className="fy-dot" style={{ background: "var(--fy-sage)" }} />
            {t("fr.step", { n: at + 1 })}
          </div>

          <h1 className="fy-display fr-title">{t(`fr.t${at + 1}` as Key)}</h1>
          <p className="fr-body">{t(`fr.b${at + 1}` as Key)}</p>

          {step === "folder" && (
            <div className="fr-act">
              <button
                type="button"
                className="fy-primary"
                disabled={busy}
                onClick={() => void choose()}
              >
                <span>{busy ? t("fr.choosing") : t("fr.choose")}</span>
              </button>
              <span className="fr-hint">{t("fr.picker")}</span>
            </div>
          )}

          {step === "write" && (
            <>
              <div className="fy-band">
                <span className="fy-gate" aria-hidden="true">
                  <i />
                  <i />
                </span>
                <div className="fy-band-cmd">{FIRST_FILE}</div>
              </div>
              <div className="fr-act">
                <button
                  type="button"
                  className="fy-primary"
                  disabled={busy}
                  onClick={() => void write()}
                >
                  <span>{busy ? t("fr.writing") : t("fr.write")}</span>
                </button>
                <button
                  type="button"
                  className="fy-outline"
                  disabled={busy}
                  onClick={() => setStep("rung")}
                >
                  {t("fr.skipWrite")}
                </button>
              </div>
            </>
          )}

          {step === "rung" && (
            <>
              {receipt && <div className="fr-receipt">{receipt}</div>}
              <div className="fr-rungs" role="radiogroup" aria-label={t("set.ordinary")}>
                {RUNGS.map((r) => (
                  <button
                    key={r.key}
                    type="button"
                    role="radio"
                    className="fy-policy"
                    aria-checked={rung === r.key}
                    onClick={() => pick(r.key)}
                  >
                    <span style={{ flex: 1, minWidth: 0 }}>
                      <span className="fy-policy-label">{t(r.label)}</span>
                      <span className="fy-policy-note">{t(r.note)}</span>
                    </span>
                    <svg
                      className="fy-policy-check"
                      width="14"
                      height="14"
                      viewBox="0 0 14 14"
                      aria-hidden="true"
                    >
                      <path
                        d="M2.6 7.5 5.4 10.2 11.4 3.9"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="1.7"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                      />
                    </svg>
                  </button>
                ))}
              </div>
              <div className="fr-act">
                <button type="button" className="fy-primary" onClick={() => setStep("done")}>
                  <span>{t("fr.s4")}</span>
                </button>
                <span className="fr-hint" style={{ maxWidth: 300 }}>
                  {t("fr.rungLater")}
                </span>
              </div>
            </>
          )}

          {step === "done" && (
            <div className="fr-act">
              <button type="button" className="fy-primary" onClick={() => onFinish("lane")}>
                <span>{t("fr.open")}</span>
              </button>
              <button type="button" className="fy-outline" onClick={() => onFinish("connect")}>
                {t("fr.howTo")}
              </button>
              {connected.length > 0 && (
                <span className="fy-sagepill">
                  <i />
                  {tn("fr.connected", connected.length)}
                </span>
              )}
            </div>
          )}
        </div>

        <div className="fr-foot">
          {step === "done" ? (
            <span className="fr-quiet">{t("fr.nothingElse")}</span>
          ) : (
            <button type="button" className="fy-underbtn" onClick={() => onFinish("lane")}>
              {t("fr.skip")}
            </button>
          )}
        </div>
      </div>

      {/* The rail the lane already has, carrying the four steps instead of a
          workspace. It does not move between steps. */}
      <div className="fr-rail">
        <div className="fy-eyebrow" style={{ marginBottom: 14 }}>
          {t("fr.setup")}
        </div>
        <div className="fr-steps">
          {STEPS.map((s, i) => (
            <div
              key={s.key}
              className="fr-step"
              data-state={i < at ? "done" : i === at ? "now" : "later"}
            >
              <span className="fr-mark">{i < at ? <i className="fr-tick" /> : i + 1}</span>
              <span className="fr-step-label">{t(s.label)}</span>
            </div>
          ))}
        </div>

        {ws && (
          <div className="fr-ws" style={{ "--fy-reveal-h": "44px" } as CSSProperties}>
            <div className="fy-eyebrow" style={{ marginBottom: 7 }}>
              {t("laneV2.workspace")}
            </div>
            <div className="fy-display" style={{ fontSize: 19, lineHeight: 1.2 }}>
              {ws.name}
            </div>
          </div>
        )}

        <div style={{ flex: 1, minHeight: 16 }} />

        {/* The gate at rail size: the drawing the product is named after, and
            the one thing on this screen that is there to be looked at. */}
        <div className="fr-gate" aria-hidden="true">
          <i />
          <i />
        </div>
        <div className="fr-gate-note">{t("fr.gateNote")}</div>
      </div>
    </div>
  );
}
