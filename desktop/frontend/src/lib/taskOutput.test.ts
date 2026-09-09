import { describe, expect, it } from "vitest";
import { LARGE_OUTPUT_BYTES, formatBytes, outputFootprint } from "./taskOutput";
import type { TaskInfo } from "./core";

const TASK: TaskInfo = {
  task_id: "t1",
  state: "succeeded",
  label: "git status --short",
  exit_code: 0,
  started_at: "2026-08-29T10:00:00-07:00",
  duration: 24_000_000,
};

describe("outputFootprint", () => {
  it("reports what a whole-machine dump actually weighed", () => {
    // The regression this screen exists for: `ps -eo pid,etime,stat,comm,args`
    // showed three lines and 83ms in the UI while 217,768 bytes went to the
    // platform. The number has to be on screen, and it has to be marked.
    const ps: TaskInfo = { ...TASK, label: "ps -eo pid,etime,stat,comm,args", stdout_cursor: 217_768, provider: "chatgpt" };
    const got = outputFootprint(ps);
    expect(got.bytes).toBe(217_768);
    expect(got.large).toBe(true);
    expect(got.provider).toBe("chatgpt");
    expect(formatBytes(got.bytes)).toBe("213 KB");
  });

  it("counts both streams, because output is output wherever it came from", () => {
    expect(outputFootprint({ ...TASK, stdout_cursor: 400, stderr_cursor: 600 }).bytes).toBe(1000);
  });

  it("leaves ordinary commands unmarked", () => {
    const got = outputFootprint({ ...TASK, stdout_cursor: 13 });
    expect(got.large).toBe(false);
    expect(formatBytes(got.bytes)).toBe("13 B");
  });

  it("treats a task with no cursors as having sent nothing, not as unknown", () => {
    // A task still starting up has no counters yet; showing "0 B sent" beats
    // showing NaN, and the row hides the field at zero anyway.
    const got = outputFootprint(TASK);
    expect(got.bytes).toBe(0);
    expect(got.large).toBe(false);
    expect(got.provider).toBeUndefined();
  });

  it("drops the Core's 'unknown' rather than printing it", () => {
    // "sent to unknown" reads as something having gone wrong; the size on
    // its own is still the answer the line exists to give.
    const got = outputFootprint({ ...TASK, stdout_cursor: 100, provider: "unknown" });
    expect(got.provider).toBeUndefined();
    expect(got.bytes).toBe(100);
  });

  it("marks at the threshold, not just past it", () => {
    expect(outputFootprint({ ...TASK, stdout_cursor: LARGE_OUTPUT_BYTES - 1 }).large).toBe(false);
    expect(outputFootprint({ ...TASK, stdout_cursor: LARGE_OUTPUT_BYTES }).large).toBe(true);
  });
});

describe("formatBytes", () => {
  it("changes unit rather than growing the number", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(64 * 1024)).toBe("64 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(12 * 1024 * 1024)).toBe("12 MB");
  });
});
