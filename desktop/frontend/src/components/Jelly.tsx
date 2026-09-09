import { useId, type CSSProperties } from "react";

// Jelly — the product's one loading mark (design board `Jelly`
// and the spec page beside it). Two soft balls pull apart and come back while
// the pair turns 90° every half round: 0.8s per deformation, 1.6s per round.
// They are merged by an SVG gooze filter, which is why the two halves read as
// one substance rather than as two dots.
//
// Three sizes and nothing between them:
//   20 — inside a button, or on a task row
//   28 — a popover, a connection row
//   40 — the page is waiting
//
// It never covers the page. It appears next to the thing that is not ready
// yet, and in a button it takes the label's place without changing the
// button's size.

export type JellySize = 20 | 28 | 40;

export interface JellyProps {
  size?: JellySize;
  /** One short line under the mark. Omitted, the mark stands on its own. */
  label?: string;
  /** Ink by default. On a filled button pass the button's own foreground,
   *  because ink on ink is invisible. */
  color?: string;
  /** What is being waited for, for a screen reader. Required when there is
   *  no visible label — a spinner with nothing to read is a blank region. */
  busyLabel?: string;
}

export function Jelly({ size = 40, label, color, busyLabel }: JellyProps) {
  // The filter is referenced by id, so two Jellies on one screen must not
  // share one: the second would inherit the first's blur radius.
  const filterID = `jl${useId().replace(/[^a-zA-Z0-9]/g, "")}`;
  const style = {
    "--jl-size": `${size}px`,
    ...(color ? { "--jl-color": color } : null),
  } as CSSProperties;

  return (
    <span
      className="fy-jelly"
      data-size={size}
      style={style}
      role="status"
      aria-label={label ? undefined : busyLabel}
    >
      <svg width="0" height="0" aria-hidden="true" focusable="false" className="fy-jelly-defs">
        <defs>
          <filter id={filterID}>
            <feGaussianBlur in="SourceGraphic" stdDeviation={size * 0.075} result="blur" />
            <feColorMatrix
              in="blur"
              type="matrix"
              values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  0 0 0 18 -7"
              result="ooze"
            />
            <feBlend in="SourceGraphic" in2="ooze" />
          </filter>
        </defs>
      </svg>
      <span className="fy-jelly-stage" style={{ filter: `url(#${filterID})` }} aria-hidden="true">
        <i />
        <i />
      </span>
      {label && <span className="fy-jelly-label">{label}</span>}
    </span>
  );
}
