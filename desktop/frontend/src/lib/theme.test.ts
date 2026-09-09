import { describe, expect, it } from "vitest";
// Vite's ?raw gives the stylesheet as a string without pulling Node types in
// for one test.
import CSS from "../style.css?raw";

// The palette is declared three times: once on bare `:root` for light, once
// under the dark media query for people who never chose, and once under
// `[data-fy="dark"]` for people who did. The third exists because the media
// query cannot see an in-app choice — so every token the second block
// overrides, the third has to override too, or picking Dark by hand lands on
// the light value while following the system lands on the right one.
//
// That is not a difference a screenshot of one machine will show, and it is
// how `--fy-dock` shipped a near-white navigation pill on a dark page: it was
// pasted into the media block twice and into the explicit block never.
//
// Asserting the two dark blocks declare the same tokens is the whole guard.
// Values may legitimately differ; the set may not.

/** Token names declared between two line numbers, 1-based and inclusive. */
function tokensIn(from: number, to: number): Set<string> {
  const lines: string[] = CSS.split("\n").slice(from - 1, to);
  const names = new Set<string>();
  for (const line of lines) {
    const hit = /^\s*--(fy-[\w-]+)\s*:/.exec(line);
    if (hit) {
      names.add(hit[1]);
    }
  }
  return names;
}

/** The line a selector opens on, so a moved block does not silently pass. */
function blockAt(selector: string): { start: number; end: number } {
  const lines: string[] = CSS.split("\n");
  const start = lines.findIndex((l) => l.trim() === selector + " {");
  if (start < 0) {
    throw new Error(`no block for ${selector}`);
  }
  let end = start + 1;
  while (end < lines.length && lines[end].trim() !== "}") {
    end += 1;
  }
  return { start: start + 2, end };
}

describe("the dark palette", () => {
  it("declares the same tokens whether dark was chosen or inherited", () => {
    const inherited = blockAt(':root:not([data-fy="light"])');
    const chosen = blockAt(':root[data-fy="dark"]');
    const a = tokensIn(inherited.start, inherited.end);
    const b = tokensIn(chosen.start, chosen.end);

    expect([...a].filter((t) => !b.has(t))).toEqual([]);
    expect([...b].filter((t) => !a.has(t))).toEqual([]);
  });

  it("declares each token once per block, since a repeat is where one goes missing", () => {
    // The duplicate is not itself a bug — the second declaration wins with the
    // same value. It is evidence: `--fy-dock` appeared twice in one dark block
    // and zero times in the other, which is what a mis-aimed paste looks like.
    for (const selector of [':root:not([data-fy="light"])', ':root[data-fy="dark"]', ":root"]) {
      const { start, end } = blockAt(selector);
      const declared = (CSS.split("\n") as string[])
        .slice(start - 1, end)
        .map((l) => /^\s*--(fy-[\w-]+)\s*:/.exec(l)?.[1])
        .filter((n): n is string => Boolean(n));
      expect(declared.length, `${selector} repeats a token`).toBe(new Set(declared).size);
    }
  });
});

// Two rules that only a browser can actually break, and jsdom is not one: it
// runs no layout, so a render test cannot see a label come apart. Asserting
// the declaration is weaker than asserting the result — it proves the rule is
// there, not that it works — but it is the difference between a fix that can
// be deleted silently and one that cannot.
describe("rules that hold a layout together", () => {
  /** The body of the first rule whose selector matches exactly. */
  function ruleFor(selector: string): string {
    const at = CSS.indexOf(selector + " {");
    if (at < 0) {
      throw new Error(`no rule for ${selector}`);
    }
    return CSS.slice(at, CSS.indexOf("}", at));
  }

  it("never lets a detail label wrap or shrink", () => {
    // "\u5f00 / \u59cb" and "\u5de5\u4f5c / \u76ee\u5f55" in a 142px column: not a line break, a word
    // coming apart. The value is the half that can take the squeeze.
    const dt = ruleFor(".fy-tpanel-pair dt");
    expect(dt).toContain("white-space: nowrap");
    expect(dt).toContain("flex: none");
    expect(ruleFor(".fy-tpanel-pair dd")).toContain("min-width: 0");
  });

  it("sizes an open dock item by its label rather than by a number", () => {
    // 62px was two Chinese characters wide, so English clipped "Settings".
    expect(ruleFor('.fy-dock[data-open="true"] .fy-dock-item')).toContain("width: auto");
  });
});
