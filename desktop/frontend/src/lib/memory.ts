import { MEMORY_LIMITS, type MemoryNote, type MemoryPage } from "./core";
import type { Key, Translator } from "./i18n";

// What the memory screen (Fylane-V3 board 17) computes before it draws:
// which field is which, how full it is, what colour a note's dot is and
// under which heading it sits. Pure, so it is pinned by memory.test.ts.

/** The five cells of the state page, in the order the board lays them out:
 *  three prose fields on the first row, two lists on the second. */
export type PageField = "goal" | "progress" | "next" | "decisions" | "open";

export const PROSE_FIELDS: PageField[] = ["goal", "progress", "next"];
export const LIST_FIELDS: PageField[] = ["decisions", "open"];

export const FIELD_LABELS: Record<PageField, Key> = {
  goal: "memory.goal",
  progress: "memory.progress",
  next: "memory.next",
  decisions: "memory.decisions",
  open: "memory.open",
};

export function isList(field: PageField): field is "decisions" | "open" {
  return field === "decisions" || field === "open";
}

/** The limit the Core enforces on a field, in bytes for prose and in items
 *  for a list. The count shown beside the title is measured the same way. */
export function fieldLimit(field: PageField): number {
  switch (field) {
    case "goal":
      return MEMORY_LIMITS.goal;
    case "progress":
      return MEMORY_LIMITS.progress;
    case "next":
      return MEMORY_LIMITS.next;
    default:
      return MEMORY_LIMITS.listItems;
  }
}

const encoder = new TextEncoder();

/** UTF-8 bytes, because that is what the Core counts: the number beside a
 *  Chinese sentence has to agree with the limit it is measured against. */
export function byteLength(s: string): number {
  return encoder.encode(s).length;
}

/** What is in a field, as the text the sheet edits: a list is one item per
 *  line. */
export function fieldText(page: MemoryPage, field: PageField): string {
  if (isList(field)) {
    return (page[field] ?? []).join("\n");
  }
  return page[field] ?? "";
}

export function fieldItems(page: MemoryPage, field: PageField): string[] {
  if (isList(field)) {
    return page[field] ?? [];
  }
  const v = page[field] ?? "";
  return v ? [v] : [];
}

/** How full a field is, in the unit its limit is in. */
export function fieldCount(page: MemoryPage, field: PageField): number {
  return isList(field) ? (page[field] ?? []).length : byteLength(page[field] ?? "");
}

/** Lines typed into the sheet, as the list they would be saved as. */
export function parseItems(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** Why a draft cannot be saved, or null. The Core would refuse the same
 *  thing; saying it beside the textarea is what keeps the refusal from
 *  arriving as a toast after the user pressed save. */
export function draftProblem(
  field: PageField,
  text: string,
  { t }: Translator,
): string | null {
  if (isList(field)) {
    const items = parseItems(text);
    if (items.length > MEMORY_LIMITS.listItems) {
      return t("memory.tooManyItems", { n: MEMORY_LIMITS.listItems });
    }
    const long = items.find((it) => byteLength(it) > MEMORY_LIMITS.listItem);
    if (long !== undefined) {
      return t("memory.itemTooLong", { n: MEMORY_LIMITS.listItem });
    }
    return null;
  }
  const limit = fieldLimit(field);
  if (byteLength(text) > limit) {
    return t("memory.tooLong", { n: limit });
  }
  return null;
}

/** The page with one field replaced by what the sheet holds. */
export function withField(page: MemoryPage, field: PageField, text: string): MemoryPage {
  const next: MemoryPage = { ...page };
  if (isList(field)) {
    next[field] = parseItems(text);
  } else {
    next[field] = text.trim();
  }
  return next;
}

/** Whether the page has anything on it at all. */
export function pageEmpty(page: MemoryPage | undefined): boolean {
  if (!page) return true;
  return (
    !page.goal?.trim() &&
    !page.progress?.trim() &&
    !page.next?.trim() &&
    (page.decisions ?? []).length === 0 &&
    (page.open ?? []).length === 0
  );
}

/** The title memory_compact gives the note a batch is folded into. The
 *  board marks it with the ink dot; nothing else about the row differs. */
export const SUMMARY_TITLE = /^Summary of notes up to #\d+/;

export type NoteTone = "applied" | "summary" | "plain";

/** Board 17's three dots: sage for a note that points at a change set (it
 *  records something that was done), ink for a summary, an outline for the
 *  rest. */
export function noteTone(note: MemoryNote): NoteTone {
  if (SUMMARY_TITLE.test(note.title)) return "summary";
  if (note.change_set_id) return "applied";
  return "plain";
}

function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** The 44px time column: the clock for today, the word for yesterday, and
 *  the date after that (with the year once it is not this one). */
export function noteWhen(iso: string, now: Date, { t }: Translator): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  if (sameDay(d, now)) return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(d, yesterday)) return t("memory.yesterday");
  const md = `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}-${md}`;
}

/** Today and yesterday are "recent"; everything before is "earlier". The
 *  tasks feed cuts at an hour because commands are minutes apart; notes are
 *  a day's work apart, so the cut follows the calendar. */
export function groupNotes(
  notes: MemoryNote[],
  now: Date,
): { recent: MemoryNote[]; earlier: MemoryNote[] } {
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  const isRecent = (n: MemoryNote) => {
    const d = new Date(n.created_at);
    return sameDay(d, now) || sameDay(d, yesterday);
  };
  return {
    recent: notes.filter(isRecent),
    earlier: notes.filter((n) => !isRecent(n)),
  };
}
