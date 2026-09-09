import { fetchApprovals, resolveApproval, saveFiles } from "./core";
import type { Translator } from "./i18n";

// The first-run test write (the onboarding board,
// step 2 → step 3). Nothing here is simulated: the file goes through the
// Core's save endpoint, so it passes the path sandbox, the 14-step
// transaction and the approval service like any other write, lands as a
// real change set, and can be rolled back from Trace.

/** Written at the workspace root, so the user can find it in Finder. */
export const FIRST_FILE = "hello-from-fylane.md";

const FIRST_CONTENT = `# Hello from Fylane

Fylane wrote this file into your workspace during first run, so you can see
what an arrival looks like. It is a real change set — roll it back any time
from Trace.
`;

/** Byte length of what actually lands on disk; the onboarding copy quotes
 * this rather than a fixed "0.2 KB", so the two can never drift apart. */
export const FIRST_BYTES = new TextEncoder().encode(FIRST_CONTENT).length;

export function formatSize(bytes: number): string {
  return `${(bytes / 1024).toFixed(1)} KB`;
}

export type FirstWriteOutcome =
  | { status: "applied"; path: string; bytes: number }
  | { status: "held"; reason: string }
  | { status: "refused"; reason: string };

function base64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary);
}

/** Same shape as the Core's identifiers (store.NewID). */
function newChangeSetID(): string {
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  return "chg_" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Records the local decision the user already made. The "Send a test file"
 * button states the file, its size and its destination, so pressing it IS
 * the local approval — the same reasoning as the desktop rollback confirm
 * (companion/internal/ctlapi.handleRollback). The decision still goes
 * through the approval service; nothing skips the gate. */
async function claimApproval(changeSetID: string, settled: () => boolean): Promise<void> {
  // Whatever happens in here, the save result is the authority on the
  // outcome: a failed poll or an already-decided approval both surface as
  // that result, so this loop reports nothing of its own.
  try {
    for (let i = 0; i < 40 && !settled(); i++) {
      await sleep(120);
      if (settled()) return;
      const approvals = await fetchApprovals();
      if (!approvals.some((a) => a.change_set_id === changeSetID)) continue;
      await resolveApproval(changeSetID, true);
      return;
    }
  } catch {
    return;
  }
}

export async function runFirstWrite(
  workspaceID: string,
  { t }: Translator,
): Promise<FirstWriteOutcome> {
  const changeSetID = newChangeSetID();
  let done = false;
  const save = saveFiles({
    workspace_id: workspaceID,
    provider: "fylane",
    summary: "First run: send a test file down the lane",
    change_set_id: changeSetID,
    files: [{ path: FIRST_FILE, content_base64: base64(FIRST_CONTENT) }],
  });
  const claim = claimApproval(changeSetID, () => done);

  let result;
  try {
    result = await save;
  } catch (e) {
    done = true;
    await claim;
    return { status: "refused", reason: e instanceof Error ? e.message : t("firstwrite.failed") };
  }
  done = true;
  await claim;

  switch (result.status) {
    case "applied":
      return { status: "applied", path: result.operations?.[0]?.path ?? FIRST_FILE, bytes: FIRST_BYTES };
    case "pending_approval":
      return {
        status: "held",
        reason: t("firstwrite.held"),
      };
    case "conflict":
      return {
        status: "refused",
        reason:
          result.conflict?.reason ??
          t("firstwrite.conflict", { file: FIRST_FILE }),
      };
    default:
      return { status: "refused", reason: result.reason || t("firstwrite.failed") };
  }
}
