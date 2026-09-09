import type {
  Approval,
  ApprovalKind,
  ChangeSet,
  CoreStatusInfo,
  OpImpact,
  OpTree,
  Source,
  TaskInfo,
  TaskState,
  Workspace,
} from "./core";
import type { Key, Translator } from "./i18n";

// View model for the lane and the record. It maps the Core's documents onto
// the design's vocabulary so the screen components stay presentation only.

export type Provider = "chatgpt" | "claude" | "grok";

export const PROVIDERS: Provider[] = ["chatgpt", "claude", "grok"];

export const PROVIDER_NAMES: Record<Provider, string> = {
  chatgpt: "ChatGPT",
  claude: "Claude",
  grok: "Grok",
};

/** One poll of the Core. `online` is control-API reachability. */
export interface LaneSnapshot {
  online: boolean;
  status: CoreStatusInfo | null;
  workspace: Workspace | null;
  approvals: Approval[];
  changeSets: ChangeSet[];
  sources: Source[];
}

export const OFFLINE_SNAPSHOT: LaneSnapshot = {
  online: false,
  status: null,
  workspace: null,
  approvals: [],
  changeSets: [],
  sources: [],
};

function providerOf(raw: string): Provider | null {
  const k = (raw || "").toLowerCase();
  return (PROVIDERS as string[]).includes(k) ? (k as Provider) : null;
}

export function displayWho(raw: string): string {
  const p = providerOf(raw);
  if (p) return PROVIDER_NAMES[p];
  if (!raw) return "\u2014";
  return raw.charAt(0).toUpperCase() + raw.slice(1);
}

export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

const OP_MARKS: Record<string, string> = {
  create: "+",
  update: "M",
  delete: "−",
  move: "→",
  // A sensitive read is the one entry that changes nothing; it leaves.
  read: "↗",
};

export interface PendingFile {
  op: string;
  path: string;
  /** A move's destination. Carried because the Core marks a move sensitive
   *  when either end is (engine.go), so a row showing only the source can put
   *  the sensitive note next to a path that does not look sensitive. */
  to?: string;
  diff?: string;
  /** The Core sends one operation for a whole tree, not one per file, so
   *  without this a directory delete and an empty-directory delete are the
   *  same line. security.md requires the recursive case be told apart. */
  recursive: boolean;
  /** The path matched the sensitive set (.env*, *.pem, id_rsa...). Writing
   *  one carries a high-risk warning (security.md). */
  sensitive: boolean;
  /** How far the change reaches, when a language server was warm enough to
   *  say. Undefined means nobody asked. */
  impact?: OpImpact;
  /** How much a recursive delete takes. Undefined means nobody counted. */
  tree?: OpTree;
  /** The undo copy will not fit in the recycle area. */
  beyondUndo: boolean;
}

export interface PendingInfo {
  changeSetID: string;
  id: string;
  who: string;
  what: string;
  files: PendingFile[];
  kind: ApprovalKind;
  /** argv rendered for reading, empty when the prompt is not about one. */
  command: string;
  /** Workspace-relative working directory, empty at the workspace root. */
  dir: string;
  /** The Core's sentence about why this stopped here. */
  reason: string;
  /** Approving also authorizes every ordinary command in this workspace. */
  grant: boolean;
  /** Whether there is a diff behind the prompt. Only a write has one; the
   *  screen used to offer "Review changes" as the primary action for command
   *  prompts too, which opened a layer with nothing in it. */
  reviewable: boolean;
  /** Stable rule id from the Core's table. Present means the rule table
   *  stopped this on purpose, which is what the request board draws in red. */
  rule: string;
  /** When the platform asked, ISO-8601 as the Core recorded it. */
  createdAt: string;
  /** The MCP tool behind the request, named as the caller named it. */
  tool: string;
  /** What this run gets from the outbound boundary, as the Core computed it.
   *  Empty for a prompt that is not about running a program. */
  network: string;
}

/** The tool a prompt came through. The Core does not send a tool name — it
 *  sends what the request is — so this is derived from that rather than
 *  guessed from which fields happen to be filled. */
export function toolOf(kind: ApprovalKind, hasCommand: boolean): string {
  switch (kind) {
    case "command":
      return "run_command";
    case "delegation":
      return "code_task";
    case "proxy":
      return "mcp_gateway";
    case "disclosure":
      return hasCommand ? "run_command" : "read_file";
    default:
      return "write_file";
  }
}

/** What the request is asking for, as the board's one-line verb. These
 *  questions must not be asked as one: a command runs, a write
 *  lands, a disclosure leaves this machine, a delegation hands the
 *  workspace to an agent, and a proxy hands the call to a tool Fylane
 *  cannot inspect. */
export function asksFor(kind: ApprovalKind): Key {
  switch (kind) {
    case "command":
      return "laneV2.asksToRun";
    case "disclosure":
      return "laneV2.asksToSend";
    case "delegation":
      return "laneV2.asksToDelegate";
    case "proxy":
      return "laneV2.asksToForward";
    default:
      return "laneV2.asksToWrite";
  }
}

/** What a change row has to say beyond its path. The Core marks both of these
 *  on the operation and the screen showed neither, which left a whole-tree
 *  delete and an empty-directory delete rendering as the same line and a write
 *  to .env reading like a write to any other file. Order is by consequence:
 *  what the row reaches comes before what the row is. */
/** The impact line: how many places outside this file use what the change
 *  touches, or null when no language server was warm enough to say.
 *
 *  It is deliberately not one of rowNotes. Those are risk markers and render
 *  in brick; reach is information, not danger, and letting the risk colour
 *  also mean "this is widely used" would weaken the one thing it says. Zero is
 *  reported rather than hidden, so an absent line means one thing only:
 *  nobody asked. */
export function impactNote(f: PendingFile, { t, tn }: Translator): string | null {
  const impact = f.impact;
  if (!impact) return null;
  if (impact.callers === 0 && !impact.partial) return t("laneV3.opNoCallers");
  if (impact.partial) return t("laneV3.opCallersAtLeast", { n: String(impact.callers) });
  return tn("laneV3.opCallers", impact.callers);
}

export function rowNotes(f: PendingFile, tr: Translator): string[] {
  const { t } = tr;
  const notes: string[] = [];
  if (f.recursive) notes.push(treeNote(f, tr));
  if (f.sensitive) notes.push(t("laneV3.opSensitive"));
  // Said last because it is the part that cannot be undone, and it is said
  // only when it is known: an unfinished count whose floor is under the limit
  // is not evidence, and a warning about a maybe is how warnings stop being
  // read.
  if (f.beyondUndo) notes.push(t("laneV3.opBeyondUndo"));
  return notes;
}

/** What a recursive delete takes, in the row that asks about it.
 *
 *  The sentence carries the number because without it a node_modules and a
 *  src read identically, and the second click F19 adds would be a click on
 *  the same words rather than on a fact. No number is its own sentence:
 *  "nobody counted" must not render as an empty directory. */
export function treeNote(f: PendingFile, { t, tn }: Translator): string {
  const tree = f.tree;
  if (!tree) {
    return t("laneV3.opRecursive");
  }
  const files = tn("laneV3.opFiles", tree.files);
  const size = humanBytes(tree.bytes);
  return tree.partial
    ? t("laneV3.opRecursiveAtLeast", { files, size })
    : t("laneV3.opRecursiveCount", { files, size });
}

/** Bytes as a person reads them. Whole units above a kilobyte: the decision
 *  being made is "is this the tree I meant", and a byte-exact figure is
 *  harder to read without being more useful. */
export function humanBytes(bytes: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let n = Math.max(0, bytes);
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  const shown = i === 0 || n >= 100 ? Math.round(n) : Math.round(n * 10) / 10;
  return `${shown} ${units[i]}`;
}

/** The change set held at the gate, or null when nothing is waiting. */
export function pendingInfo(approvals: Approval[], { t }: Translator): PendingInfo | null {
  const a = approvals[0];
  if (!a) return null;
  // A prompt from a Core that predates the kind field still has to render;
  // write is the reading that shows the most and hides nothing.
  const kind: ApprovalKind = a.kind ?? "write";
  return {
    changeSetID: a.change_set_id,
    id: a.change_set_id.length > 16 ? a.change_set_id.slice(0, 16) : a.change_set_id,
    who: displayWho(a.provider),
    what: a.summary || t("laneScreen.defaultWhat"),
    files: (a.operations ?? []).map((o) => ({
      op: OP_MARKS[o.type] ?? "M",
      path: o.path,
      to: o.to,
      diff: o.diff,
      recursive: o.recursive_delete ?? false,
      sensitive: o.sensitive ?? false,
      impact: o.impact,
      tree: o.tree,
      beyondUndo: o.beyond_undo ?? false,
    })),
    kind,
    // Delegation's argv is [agent, prompt] rather than a command line, so it
    // is left to the summary, which reads as the sentence it is.
    command: kind === "command" || kind === "disclosure" ? (a.command ?? []).join(" ") : "",
    dir: a.dir ?? "",
    reason: a.reason ?? "",
    grant: a.grant ?? false,
    reviewable: kind === "write",
    rule: a.rule ?? "",
    createdAt: a.created_at,
    tool: toolOf(kind, (a.command ?? []).length > 0),
    // Only the prompts that start a program: a write does not reach the
    // network, and a row saying so would be answering a question nobody asked.
    network: kind === "command" || kind === "delegation" ? (a.network ?? "") : "",
  };
}

// ---------------------------------------------------------------------------
// Desktop v2 lane. The board answers one question at a time: something is
// waiting on you, something is running, or nothing is. Everything else on the
// screen is context for that one thing.

export type LaneState = "request" | "running" | "calm";

export interface LaneBoard {
  state: LaneState;
  /** The task on the running bar; null unless state is "running". */
  running: TaskInfo | null;
  /** Most recent finished task, for the one-line footer. */
  last: TaskInfo | null;
}

export function laneBoard(approvals: Approval[], tasks: TaskInfo[]): LaneBoard {
  const running = tasks.find((t) => t.state === "running") ?? null;
  const last = tasks.find((t) => t.state !== "running") ?? null;
  // A request outranks a running task: the platform is blocked on the user,
  // the running one is not blocked on anybody.
  const state: LaneState = approvals.length > 0 ? "request" : running ? "running" : "calm";
  return { state, running, last };
}

/** The two numbers under the calm scene (Fylane-V3 board 04): how much got
 *  through the gate today, and how much did not.
 *
 *  A task that ran was let through, so it counts as passed; a write counts
 *  once it landed, whether or not it was later undone. Rejected covers both
 *  sides of the gate: a denied change set and a refused command. The refused
 *  command only became visible here once the screen started reading the audit
 *  log — before that the number could only ever be the change sets. */
export function dayTally(
  tasks: TaskInfo[],
  sets: ChangeSet[],
  now: Date,
): { passed: number; rejected: number } {
  const today = (iso: string) => {
    const d = new Date(iso);
    return !Number.isNaN(d.getTime()) && sameDay(d, now);
  };
  // A command the user refused is a refusal, not a task that happened to end.
  // Counting it as passed was the visible half of the task screen reading a
  // list that could not contain refusals at all (reported 2026-08-30).
  const todayTasks = tasks.filter((t) => today(t.started_at));
  const passed =
    todayTasks.filter((t) => t.state !== "denied").length +
    sets.filter(
      (s) => (s.status === "applied" || s.status === "rolled_back") && today(s.created_at),
    ).length;
  return {
    passed,
    rejected:
      todayTasks.filter((t) => t.state === "denied").length +
      sets.filter((s) => s.status === "denied" && today(s.created_at)).length,
  };
}

function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

/** Colour of a task's status dot. Semantics are fixed (HANDOFF §3): sage is
 *  success, red is failure, and anything still open is neutral. */
export function taskDot(state: TaskState): string {
  switch (state) {
    case "succeeded":
      return "var(--fy-sage)";
    case "failed":
    case "timed_out":
      return "var(--fy-red)";
    case "interrupted":
      // Amber is already this product's "something here wants your eyes"
      // (the held shell, a paused workspace, an oversized send). It is the
      // right one here for the same reason red is wrong: the command did not
      // fail, it stopped being observed, and only the user can find out what
      // it left behind.
      return "var(--fy-amber)";
    default:
      return "var(--fy-line)";
  }
}

/** What a task line says it is. Six words and no seventh (HANDOFF §6). */
export function taskStateWord(state: TaskState, { t }: Translator): string {
  switch (state) {
    case "running":
      return t("task.running");
    case "succeeded":
      return t("task.done");
    case "failed":
      return t("task.failed");
    case "timed_out":
      return t("task.timedOut");
    case "interrupted":
      return t("task.interrupted");
    default:
      return t("task.canceled");
  }
}

/** The command a task ran, as one line. */
export function taskCommand(task: TaskInfo, { t }: Translator): string {
  return task.label?.trim() || t("task.unnamed");
}

/** Last two segments of a path — enough to tell two granted folders apart
 *  without printing someone's home directory across a 280px menu. */
export function shortPath(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts.length <= 2 ? path : "…/" + parts.slice(-2).join("/");
}
