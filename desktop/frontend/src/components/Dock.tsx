import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useT, type Key } from "../lib/i18n";

// The Node dock (Fylane-V3 board 03). Navigation lives at the bottom left as
// a floating mark, not as a row of pills in the title bar: the pages are
// somewhere you go occasionally, and the window's top row belongs to the one
// thing the window is currently about.
//
// Collapsed it is the mark alone. It opens on hover or on a click, and leaves
// 600ms after the pointer does — crossing it on the way somewhere else must
// not collapse it under the cursor. A click pins it, so a keyboard or a slow
// hand is not fighting a timer.

export interface DockPage<T extends string> {
  key: T;
  label: Key;
}

export interface DockProps<T extends string> {
  pages: DockPage<T>[];
  current: T;
  onGoto: (key: T) => void;
  /** Something is waiting on the user; the collapsed mark has to say so. */
  pending: boolean;
}

const LEAVE_MS = 600;
const CLOSED_W = 40;
/** `padding-left` + `padding-right` on an open item, which its scrollWidth
 *  cannot see because the item is collapsed to zero while closed. */
const ITEM_PAD = 22;
/** The shell's `gap`, once before each item. */
const GAP = 2;

export function Dock<T extends string>({ pages, current, onGoto, pending }: DockProps<T>) {
  const { t } = useT();
  const [pinned, setPinned] = useState(false);
  const [hover, setHover] = useState(false);
  const leaving = useRef<number>(0);
  useEffect(() => () => window.clearTimeout(leaving.current), []);

  // The open width used to be a constant, and the constant was measured off
  // two Chinese characters — so English clipped "Settings" to "Setting". It
  // is measured instead: an item is collapsed to zero width with the label
  // overflowing, and scrollWidth reports what the label actually needs, so
  // this works while the dock is shut and in any language.
  const shell = useRef<HTMLDivElement>(null);
  const [openWidth, setOpenWidth] = useState(0);
  const labels = pages.map((p) => t(p.label)).join("\u0000");
  useLayoutEffect(() => {
    const node = shell.current;
    if (!node) {
      return;
    }
    const items = Array.from(node.querySelectorAll<HTMLElement>(".fy-dock-item"));
    const content = items.reduce((sum, el) => sum + el.scrollWidth + ITEM_PAD + GAP, 0);
    setOpenWidth(CLOSED_W + content);
  }, [labels]);

  const open = pinned || hover;

  const enter = () => {
    window.clearTimeout(leaving.current);
    setHover(true);
  };
  const leave = () => {
    window.clearTimeout(leaving.current);
    leaving.current = window.setTimeout(() => setHover(false), LEAVE_MS);
  };

  return (
    <div
      className="fy-dock"
      data-open={open ? "true" : "false"}
      data-badge={pending && !open ? "true" : "false"}
      onMouseEnter={enter}
      onMouseLeave={leave}
      onFocus={enter}
      onBlur={leave}
    >
      <div
        className="fy-dock-shell"
        ref={shell}
        // Before the first measurement the dock stays shut rather than
        // opening to a guessed width: one frame closed is invisible, one
        // frame at the wrong width is the bug this replaced.
        style={{ width: open && openWidth > 0 ? openWidth : CLOSED_W }}
      >
        <button
          type="button"
          className="fy-dock-node"
          aria-expanded={open}
          aria-label={t(open ? "shell.navClose" : "shell.navOpen")}
          onClick={() => setPinned((v) => !v)}
        >
          <i />
          <b />
          <i />
        </button>
        {pages.map((p) => (
          <button
            key={p.key}
            type="button"
            className="fy-dock-item"
            aria-current={p.key === current ? "page" : undefined}
            // Closed, the labels are off the page rather than merely invisible:
            // a tab stop on a 0px button with unreadable text is a trap.
            tabIndex={open ? 0 : -1}
            aria-hidden={open ? undefined : true}
            onClick={() => onGoto(p.key)}
          >
            <i />
            <span>{t(p.label)}</span>
          </button>
        ))}
      </div>
      <span className="fy-dock-badge" aria-hidden="true" />
    </div>
  );
}
