import { describe, expect, it } from "vitest";
import { DICT, translate, type Key } from "./i18n";

// The window is monolingual: whichever language is chosen, no string of the
// other one is allowed to show through. That is a property of the whole
// source tree, not of one module, so it is checked here.

const CJK = /[一-鿿]/;

// Read through vite rather than node's fs: the app's tsconfig has no node
// types, and this keeps the check running in the same resolver the build
// uses. The pattern is rooted at the project, so keys read "screens/Rules.tsx".
const SOURCES: Record<string, string> = Object.fromEntries(
  Object.entries(
    import.meta.glob("/src/**/*.{ts,tsx}", { query: "?raw", import: "default", eager: true }) as Record<
      string,
      string
    >,
  ).map(([path, text]) => [path.replace(/^\/src\//, ""), text]),
);

/** stripComments drops what only a developer reads. Comments explain
 * constraints and may quote the design; what must not happen is any of it
 * reaching the screen. */
function stripComments(text: string): string {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .split("\n")
    .filter((line) => !line.trim().startsWith("//"))
    .join("\n");
}

/** Files still carrying their original hard-coded copy. Empty, and it stays
 * that way: a new one shows up as a failure of the scan below, not as a
 * quiet exception here. */
const NOT_YET_TRANSLATED = new Set<string>([]);

describe("i18n dictionary", () => {
  it("says the same thing twice, and says something both times", () => {
    for (const [key, entry] of Object.entries(DICT)) {
      expect(entry.en.trim(), `${key} (en)`).not.toBe("");
      expect(entry.zh.trim(), `${key} (zh)`).not.toBe("");
    }
  });

  it("keeps Chinese out of the English side", () => {
    for (const [key, entry] of Object.entries(DICT)) {
      expect(CJK.test(entry.en), `${key} carries Chinese in its English text`).toBe(false);
    }
  });

  it("carries the same placeholders in both languages", () => {
    const slots = (text: string) => (text.match(/\{\w+\}/g) ?? []).sort().join(",");
    for (const [key, entry] of Object.entries(DICT)) {
      expect(slots(entry.zh), `${key} placeholders`).toBe(slots(entry.en));
    }
  });

  it("fills placeholders and leaves an unfilled one visible", () => {
    expect(translate("en", "lane.heldSubMany", { n: 2 })).toContain("2 change sets");
    expect(translate("zh", "lane.heldSubMany", { n: 2 })).toContain("2 个变更集");
    // A missing value must not silently vanish: a hole on screen is how a
    // wiring mistake gets noticed.
    expect(translate("en", "lane.heldSubMany")).toContain("{n}");
  });

  it("pairs every singular with its plural", () => {
    for (const key of Object.keys(DICT)) {
      if (key.endsWith("_one")) {
        expect(Object.keys(DICT)).toContain(`${key.slice(0, -4)}_other` as Key);
      }
    }
  });
});

describe("source tree", () => {
  it("keeps hard-coded Chinese out of every translated file", () => {
    const offenders = Object.entries(SOURCES)
      .filter(([rel]) => !rel.includes(".test."))
      .filter(([rel]) => rel !== "lib/i18n.ts" && !NOT_YET_TRANSLATED.has(rel))
      .filter(([, text]) => CJK.test(stripComments(text)))
      .map(([rel]) => rel);
    expect(offenders).toEqual([]);
  });

  // The check above only catches Chinese, so English wording sat in the JSX
  // untranslated for a while and showed through in the Chinese window. These
  // two scans read the markup instead of the language.
  //
  // Short tokens that are the same word in both languages, and are typed or
  // pressed rather than read. "Fylane" joins them as the product's name: a
  // Chinese window still says Fylane on its title bar.
  const TOKENS = new Set(["ANY", "ESC", "Fylane"]);

  // Key names are dictated by the keyboard, not by the interface language —
  // a Chinese Windows user still presses Ctrl K. They vary by platform, so
  // they cannot live in a language dictionary either.
  const KEY_LABELS = new Set(["Ctrl K"]);

  it("keeps English text nodes out of the markup", () => {
    const offenders: string[] = [];
    for (const [rel, text] of Object.entries(SOURCES)) {
      if (rel.includes(".test.") || !rel.endsWith(".tsx")) {
        continue;
      }
      // A JSX child that is plain text: between a tag's `>` and the matching
      // `</`, with no braces in between — an interpolated child would have
      // gone through t().
      for (const m of text.matchAll(/>\s*([A-Za-z][^<>{}]*?)\s*<\//g)) {
        const found = m[1].replace(/\s+/g, " ");
        if (!TOKENS.has(found)) {
          offenders.push(`${rel}: ${found}`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  // Multi-word CSS values a style prop legitimately carries. A literal made
  // only of these is a style, not a sentence.
  const CSS_WORDS = new Set([
    "center", "top", "bottom", "left", "right", "none", "auto", "both",
    "solid", "dashed", "dotted", "hidden", "ease", "in", "out", "forwards",
    "normal", "pre", "wrap", "nowrap", "break", "word", "flex", "grid",
    "block", "inline", "start", "end", "baseline", "stretch", "space",
    "between", "around", "evenly", "cover", "contain", "repeat", "scroll",
    "fixed", "absolute", "relative", "sticky", "static", "pointer",
    "default", "grabbing", "grab", "ellipsis", "clip", "visible", "column",
    "row", "reverse", "pretty", "balance", "border", "content", "box",
  ]);

  it("keeps English sentences out of string literals", () => {
    const offenders: string[] = [];
    for (const [rel, text] of Object.entries(SOURCES)) {
      if (rel.includes(".test.") || !rel.endsWith(".tsx")) {
        continue;
      }
      const code = stripComments(text);
      for (const m of code.matchAll(/"([^"\n]+)"/g)) {
        const value = m[1];
        // Two or more plain words. A single word ("Copy") cannot be told
        // apart from a style value, so this scan does not claim to catch
        // every case — the markup scan above is the thorough one.
        if (!/^[A-Za-z]+(?: [A-Za-z&]+)+$/.test(value)) {
          continue;
        }
        if (value.split(" ").every((w) => CSS_WORDS.has(w))) {
          continue;
        }
        if (KEY_LABELS.has(value)) {
          continue;
        }
        offenders.push(`${rel}: ${value}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("still lists the files that have not been converted", () => {
    // Guards the list itself: a file that no longer exists would make the
    // check above pass for the wrong reason.
    for (const rel of NOT_YET_TRANSLATED) {
      expect(Object.keys(SOURCES), rel).toContain(rel);
    }
  });
});
