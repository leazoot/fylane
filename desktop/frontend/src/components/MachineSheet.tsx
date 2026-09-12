import { useEffect, useRef, useState } from "react";
import { useT } from "../lib/i18n";

// A sheet that asks for a few words and hands them back. Two uses: naming
// a remote machine, and naming a folder on one. Neither is a decision the
// platform is blocked on, so unlike the pairing sheet there is no live
// state up top and no keyboard shortcut beyond the form's own: Enter
// submits, Esc cancels, and a submit that the Core refuses stays open with
// the Core's sentence under the fields.

export interface SheetField {
  key: string;
  label: string;
  placeholder?: string;
  /** Rendered half-width next to the previous half-width field. */
  half?: boolean;
  required?: boolean;
  /** Numeric input; the value handed back is still a string. */
  numeric?: boolean;
}

export interface MachineSheetProps {
  title: string;
  body: string;
  fields: SheetField[];
  action: string;
  /** Resolves when the Core accepted; throws with the reason otherwise. */
  onSubmit: (values: Record<string, string>) => Promise<void>;
  onCancel: () => void;
}

export function MachineSheet({
  title,
  body,
  fields,
  action,
  onSubmit,
  onCancel,
}: MachineSheetProps) {
  const { t } = useT();
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => first.current?.focus(), []);

  const complete = fields.every(
    (f) => !f.required || (values[f.key] ?? "").trim() !== "",
  );

  const submit = async () => {
    if (busy || !complete) return;
    setBusy(true);
    setError("");
    try {
      const trimmed: Record<string, string> = {};
      for (const f of fields) trimmed[f.key] = (values[f.key] ?? "").trim();
      await onSubmit(trimmed);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  // Fields are laid out in order; a half-width field joins the half-width
  // one before it on the same row.
  const rows: SheetField[][] = [];
  for (const f of fields) {
    const last = rows[rows.length - 1];
    if (f.half && last && last.length === 1 && last[0].half) last.push(f);
    else rows.push([f]);
  }

  const input = (f: SheetField, i: number) => (
    <div key={f.key}>
      <label htmlFor={`fy-sheet-${f.key}`}>{f.label}</label>
      <input
        id={`fy-sheet-${f.key}`}
        ref={i === 0 ? first : undefined}
        className="fy-field"
        inputMode={f.numeric ? "numeric" : undefined}
        placeholder={f.placeholder}
        value={values[f.key] ?? ""}
        disabled={busy}
        onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
      />
    </div>
  );

  return (
    <>
      <div className="fy-dim" onClick={onCancel} />
      <form
        className="fy-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={title}
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
        <div className="fy-sheet-title" style={{ marginTop: 0 }}>
          {title}
        </div>
        <div className="fy-sheet-guide" style={{ marginTop: 12 }}>
          {body}
        </div>
        <div className="fy-sheet-form">
          {rows.map((row, r) =>
            row.length === 2 ? (
              <div key={row[0].key} className="fy-sheet-form-row">
                {row.map((f, i) => input(f, r + i))}
              </div>
            ) : (
              input(row[0], r)
            ),
          )}
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
            disabled={busy || !complete}
          >
            {action}
          </button>
        </div>
      </form>
    </>
  );
}
