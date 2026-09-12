import { describe, expect, it } from "vitest";
import { translatorFor } from "./i18n";
import type { MemoryNote } from "./core";
import {
  byteLength,
  draftProblem,
  fieldCount,
  groupNotes,
  noteTone,
  noteWhen,
  pageEmpty,
  parseItems,
  withField,
} from "./memory";

const en = translatorFor("en");
const zh = translatorFor("zh");
const NOW = new Date(2026, 8, 12, 14, 30);

function note(over: Partial<MemoryNote>): MemoryNote {
  return { id: 1, workspace_id: "ws", title: "t", body: "", created_at: NOW.toISOString(), ...over };
}

describe("memory counts the way the Core does", () => {
  it("measures bytes, not characters", () => {
    expect(byteLength("abc")).toBe(3);
    expect(byteLength("收件箱")).toBe(9);
    expect(fieldCount({ goal: "收件箱" }, "goal")).toBe(9);
    expect(fieldCount({ decisions: ["a", "b"] }, "decisions")).toBe(2);
  });

  it("refuses what the Core would refuse, beside the text", () => {
    expect(draftProblem("goal", "x".repeat(300), en)).toBeNull();
    expect(draftProblem("goal", "x".repeat(301), en)).toBe("Over the limit of 300 bytes");
    expect(draftProblem("goal", "字".repeat(101), zh)).toBe("超过 300 字节的上限");
    expect(draftProblem("decisions", Array(8).fill("d").join("\n"), en)).toBeNull();
    expect(draftProblem("decisions", Array(9).fill("d").join("\n"), en)).toBe("At most 8 items");
    expect(draftProblem("open", "y".repeat(201), en)).toBe("An item is over 200 bytes");
    // Blank lines are not items.
    expect(draftProblem("open", "\n\n" + Array(8).fill("o").join("\n\n"), en)).toBeNull();
  });

  it("turns lines into items and trims prose", () => {
    expect(parseItems(" a \n\n b\n")).toEqual(["a", "b"]);
    expect(withField({ goal: "g", open: ["q"] }, "decisions", "x\ny")).toEqual({
      goal: "g",
      open: ["q"],
      decisions: ["x", "y"],
    });
    expect(withField({ goal: "g" }, "next", "  n  ").next).toBe("n");
  });

  it("knows an empty page from a page with whitespace on it", () => {
    expect(pageEmpty(undefined)).toBe(true);
    expect(pageEmpty({ goal: "  ", decisions: [] })).toBe(true);
    expect(pageEmpty({ open: ["q"] })).toBe(false);
  });
});

describe("memory rows", () => {
  it("colours the dot by what the note points at", () => {
    expect(noteTone(note({}))).toBe("plain");
    expect(noteTone(note({ run_id: "tsk_1" }))).toBe("plain");
    expect(noteTone(note({ change_set_id: "chg_1" }))).toBe("applied");
    expect(noteTone(note({ title: "Summary of notes up to #40 (from 2026-09-01)", change_set_id: "chg_1" }))).toBe("summary");
  });

  it("fills the time column with the clock, the word, or the date", () => {
    expect(noteWhen(new Date(2026, 8, 12, 9, 5).toISOString(), NOW, en)).toBe("09:05");
    expect(noteWhen(new Date(2026, 8, 11, 23, 0).toISOString(), NOW, zh)).toBe("昨天");
    expect(noteWhen(new Date(2026, 8, 11, 23, 0).toISOString(), NOW, en)).toBe("Yest.");
    expect(noteWhen(new Date(2026, 8, 10, 8, 0).toISOString(), NOW, en)).toBe("09-10");
    expect(noteWhen(new Date(2025, 11, 31, 8, 0).toISOString(), NOW, en)).toBe("2025-12-31");
    expect(noteWhen("nope", NOW, en)).toBe("—");
  });

  it("cuts recent from earlier at the calendar, not the clock", () => {
    const today = note({ id: 3, created_at: new Date(2026, 8, 12, 0, 10).toISOString() });
    const yesterday = note({ id: 2, created_at: new Date(2026, 8, 11, 0, 10).toISOString() });
    const older = note({ id: 1, created_at: new Date(2026, 8, 10, 23, 50).toISOString() });
    const { recent, earlier } = groupNotes([today, yesterday, older], NOW);
    expect(recent.map((n) => n.id)).toEqual([3, 2]);
    expect(earlier.map((n) => n.id)).toEqual([1]);
  });
});
