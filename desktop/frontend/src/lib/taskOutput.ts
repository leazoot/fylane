import type { TaskInfo } from "./core";

// How much a command sent, and whether that amount deserves to be noticed.
//
// The screen used to show the first few lines of output and nothing else. A
// model ran `ps -eo pid,etime,stat,comm,args`; the row showed three lines and
// 83ms, while 944 lines — the whole process table, another process's tunnel
// token inside it — went to the platform. Nothing on screen distinguished it
// from `git status`. Showing a preview without showing the size is not a
// neutral omission: it makes a whole-machine dump look small.

/** Output at or above this many bytes is marked. The number is not a limit
 *  and nothing is blocked by it — it is the point where "some output" became
 *  "a body of data", and where a person should look before scrolling past. */
export const LARGE_OUTPUT_BYTES = 64 * 1024;

export interface OutputFootprint {
  /** Total bytes of stdout and stderr this task produced. */
  bytes: number;
  /** True once the amount is worth drawing attention to. */
  large: boolean;
  /** The platform the output was handed to, when the task recorded one. */
  provider?: string;
}

export function outputFootprint(task: TaskInfo): OutputFootprint {
  // The cursors are byte offsets into the captured streams, so they are the
  // true size even when the strings have since been trimmed for display —
  // and they count bytes rather than UTF-16 units, which is what actually
  // left the machine.
  const bytes = (task.stdout_cursor ?? 0) + (task.stderr_cursor ?? 0);
  // The Core writes "unknown" when it cannot infer a platform — a loopback
  // caller, or a client whose registration says nothing. Rendering that
  // verbatim would put "sent to unknown" on screen, which reads as a failure
  // rather than as an absent detail. The size alone is still the answer to
  // the question this line exists for.
  const named = task.provider && task.provider !== "unknown" ? task.provider : undefined;
  return { bytes, large: bytes >= LARGE_OUTPUT_BYTES, provider: named };
}

/** formatBytes is deliberately coarse. An exact byte count reads as precision
 *  that does not matter here; the question being answered is "how much of my
 *  machine went out", and the answer only needs an order of magnitude. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const kb = bytes / 1024;
  if (kb < 1024) return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`;
  const mb = kb / 1024;
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`;
}
