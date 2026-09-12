import { useEffect, useRef, useState } from "react";
import type { MemoryPage } from "../lib/core";
import {
  byteLength,
  draftProblem,
  fieldCount,
  fieldItems,
  fieldLimit,
  fieldText,
  FIELD_LABELS,
  isList,
  parseItems,
  type PageField,
} from "../lib/memory";
import type { Translator } from "../lib/i18n";

// "View all" on a state cell (Fylane-V3 board 17). The one raised surface
// on the page: the whole field, and the same surface turned into an editor
// with the count beside the save button. The count is measured the way the
// Core measures it, so a draft it would refuse is refused here first, next
// to the text, instead of as a toast after the click.

export interface MemorySheetProps {
  page: MemoryPage;
  field: PageField;
  tr: Translator;
  onSave: (text: string) => Promise<void>;
  onClose: () => void;
}

export function MemorySheet({ page, field, tr, onSave, onClose }: MemorySheetProps) {
  const { t } = tr;
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(() => fieldText(page, field));
  const [saving, setSaving] = useState(false);
  const first = useRef<HTMLButtonElement>(null);
  const area = useRef<HTMLTextAreaElement>(null);

  // Focus lands on the sheet when it opens and goes back where it came
  // from when it closes; Escape is the way out from anywhere inside.
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    first.current?.focus();
    return () => before?.focus?.();
  }, []);
  useEffect(() => {
    if (editing) area.current?.focus();
  }, [editing]);

  const items = fieldItems(page, field);
  const limit = fieldLimit(field);
  const problem = draftProblem(field, draft, tr);
  const count = isList(field) ? parseItems(draft).length : byteLength(draft);
  const label = t(FIELD_LABELS[field]);

  const save = () => {
    if (problem || saving) return;
    setSaving(true);
    void onSave(draft).finally(() => setSaving(false));
  };

  return (
    <>
      <div className="fy-dim" onClick={onClose} />
      <div
        className="fy-sheet fy-mem-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="fy-mem-sheet-title"
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            onClose();
          }
        }}
      >
        <div className="fy-mem-sheet-head">
          <h2 id="fy-mem-sheet-title">{label}</h2>
          <small>
            {fieldCount(page, field)} / {limit}
          </small>
        </div>

        {editing ? (
          <>
            <textarea
              ref={area}
              className="fy-mem-sheet-edit"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              aria-label={label}
              placeholder={isList(field) ? t("memory.listHint") : undefined}
            />
            {problem && (
              <div className="fy-mem-problem" role="alert">
                {problem}
              </div>
            )}
          </>
        ) : isList(field) ? (
          items.length > 0 ? (
            <ul className={`fy-mem-sheet-full fy-mem-list ${field === "open" ? "fy-mem-open" : ""}`}>
              {items.map((it, i) => (
                <li key={i}>{it}</li>
              ))}
            </ul>
          ) : (
            <div className="fy-mem-sheet-full fy-mem-none">{t("memory.emptyField")}</div>
          )
        ) : items.length > 0 ? (
          <div className="fy-mem-sheet-full">{items[0]}</div>
        ) : (
          <div className="fy-mem-sheet-full fy-mem-none">{t("memory.emptyField")}</div>
        )}

        <div className="fy-mem-sheet-foot">
          {editing && (
            <span
              className="fy-mem-count"
              style={problem ? { color: "var(--fy-amber)" } : undefined}
            >
              {count} / {limit}
            </span>
          )}
          <span style={{ flex: 1 }} />
          {editing ? (
            <>
              <button
                type="button"
                className="fy-quiet"
                onClick={() => {
                  setDraft(fieldText(page, field));
                  setEditing(false);
                }}
              >
                {t("memory.cancel")}
              </button>
              <button
                type="button"
                className="fy-mem-btn"
                disabled={!!problem || saving}
                onClick={save}
              >
                {t("memory.save")}
              </button>
            </>
          ) : (
            <>
              <button
                ref={first}
                type="button"
                className="fy-mem-link"
                onClick={() => setEditing(true)}
              >
                {t("memory.edit")}
              </button>
              <button type="button" className="fy-mem-link" onClick={onClose}>
                {t("memory.close")}
              </button>
            </>
          )}
        </div>
      </div>
    </>
  );
}
