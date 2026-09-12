import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type {
  ChangeSet,
  MemoryDoc,
  MemoryNote,
  MemoryPage,
  MemorySource,
  TaskInfo,
  Workspace,
} from "../lib/core";
import { clock, displayWho } from "../lib/lane";
import {
  agoShort,
  canRollback,
  duration,
  entryStatusWord,
  startedAt,
} from "../lib/records";
import {
  fieldCount,
  fieldItems,
  fieldLimit,
  FIELD_LABELS,
  groupNotes,
  isList,
  LIST_FIELDS,
  noteTone,
  noteWhen,
  pageEmpty,
  PROSE_FIELDS,
  withField,
  type PageField,
} from "../lib/memory";
import { useT, type Translator } from "../lib/i18n";
import { Jelly } from "../components/Jelly";
import { MemorySheet } from "../components/MemorySheet";

// The memory view (Fylane-V3 board 17). What the connected AI wrote down
// about this folder through the memory tools: the state page it rewrites
// whole, and the trail of notes it appends. The user reads it, corrects the
// page by hand, exports it, deletes a note, or forgets everything.
//
// The head is one row. The state page is two rows of hairline-separated
// cells, three lines each, "view all" opening the sheet. The trail is the
// body of the page: one line per note, opening in place into the body and
// a ledger strip of what the note points at.

/** How long the search box waits after the last keystroke before asking. */
const SEARCH_MS = 250;

export interface MemoryProps {
  workspace: Workspace | null;
  /** The remote machine the folder is on; "" for this computer. */
  machine: string;
  source: MemorySource;
  changeSets: ChangeSet[];
  tasks: TaskInfo[];
  now: Date;
  onError: (message: string) => void;
  onGotoLane: () => void;
  onGotoTasks: () => void;
  onHelp: () => void;
}

type Filter = "live" | "archived";

export function MemoryScreen({
  workspace,
  machine,
  source,
  changeSets,
  tasks,
  now,
  onError,
  onGotoLane,
  onGotoTasks,
  onHelp,
}: MemoryProps) {
  const tr = useT();
  const { t } = tr;
  const wsID = workspace?.id ?? "";
  const [doc, setDoc] = useState<MemoryDoc | null>(null);
  // "old" is a remote Core from before this page existed: its proxy answers
  // 404 to the memory endpoint, which is a reason, not a failure to retry.
  const [failed, setFailed] = useState<false | "read" | "old">(false);
  const [filter, setFilter] = useState<Filter>("live");
  const [typed, setTyped] = useState("");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState<number | null>(null);
  const [sheet, setSheet] = useState<PageField | null>(null);
  const [asking, setAsking] = useState(false);
  const [more, setMore] = useState(false);
  const [exported, setExported] = useState(false);
  // Requests can overtake one another (a keystroke, then a filter click);
  // only the answer to the latest one may land.
  const serial = useRef(0);

  useEffect(() => {
    const id = window.setTimeout(() => setQuery(typed.trim()), SEARCH_MS);
    return () => window.clearTimeout(id);
  }, [typed]);

  const load = useCallback(async () => {
    if (!wsID) {
      setDoc(null);
      return;
    }
    const mine = ++serial.current;
    try {
      const next = await source.fetch(wsID, {
        archived: filter === "archived",
        query: query || undefined,
      });
      if (mine === serial.current) {
        setDoc(next);
        setFailed(false);
      }
    } catch (err) {
      if (mine === serial.current) setFailed(missingEndpoint(err) ? "old" : "read");
    }
  }, [source, wsID, filter, query]);

  useEffect(() => {
    setDoc(null);
    setOpen(null);
    void load();
  }, [load]);

  const act = useCallback(
    async (fn: () => Promise<void>) => {
      try {
        await fn();
        await load();
      } catch (err) {
        onError(err instanceof Error ? err.message : String(err));
      }
    },
    [load, onError],
  );

  const loadMore = async () => {
    if (!doc?.next_before_id || more) return;
    setMore(true);
    try {
      const page = await source.fetch(wsID, {
        archived: filter === "archived",
        before: doc.next_before_id,
      });
      setDoc((d) =>
        d
          ? { ...d, notes: [...d.notes, ...page.notes], next_before_id: page.next_before_id }
          : d,
      );
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setMore(false);
    }
  };

  const exportNow = async () => {
    try {
      const path = await source.export(wsID);
      if (path) {
        setExported(true);
        window.setTimeout(() => setExported(false), 1500);
      }
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err));
    }
  };

  const savePage = (field: PageField, text: string) =>
    act(async () => {
      const page: MemoryPage = withField(doc?.state?.page ?? {}, field, text);
      await source.savePage(wsID, page);
      setSheet(null);
    });

  if (!workspace) {
    return (
      <div className="fy-page fy-mem">
        <Head tr={tr} workspace={null} machine={machine} doc={null} now={now} />
        <Empty
          title={t("record.subtitleNoFolder")}
          body={t("memory.noFolderBody")}
          link={t("tasksV3.backToLane")}
          onLink={onGotoLane}
        />
      </div>
    );
  }

  const page = doc?.state?.page ?? {};
  const nothing =
    doc !== null && !doc.state && doc.live + doc.archived === 0 && !query;
  const total = (doc?.live ?? 0) + (doc?.archived ?? 0);

  return (
    <div className="fy-page fy-mem">
      <Head tr={tr} workspace={workspace} machine={machine} doc={doc} now={now}>
        {doc && !nothing && (
          <div className="fy-mem-tools">
            <label className="fy-mem-search">
              <svg viewBox="0 0 16 16" aria-hidden="true">
                <circle cx="7" cy="7" r="4.5" />
                <path d="M10.5 10.5 14 14" />
              </svg>
              <input
                type="search"
                value={typed}
                placeholder={t("memory.search")}
                aria-label={t("memory.search")}
                onChange={(e) => setTyped(e.target.value)}
              />
            </label>
            {(["live", "archived"] as Filter[]).map((f) => (
              <button
                key={f}
                type="button"
                className="fy-mem-filter"
                aria-pressed={filter === f}
                onClick={() => {
                  setFilter(f);
                  setOpen(null);
                }}
              >
                {t(f === "live" ? "memory.notes" : "memory.archived")}
                <span>{f === "live" ? doc.live : doc.archived}</span>
              </button>
            ))}
            <div className="fy-mem-acts">
              {asking ? (
                <>
                  <span className="fy-mem-ask">{t("memory.clearAsk", { n: total })}</span>
                  <button type="button" className="fy-mem-link" onClick={() => setAsking(false)}>
                    {t("memory.cancel")}
                  </button>
                  <button
                    type="button"
                    className="fy-mem-link fy-mem-brick"
                    onClick={() => {
                      setAsking(false);
                      void act(() => source.clear(wsID));
                    }}
                  >
                    {t("memory.clearConfirm")}
                  </button>
                </>
              ) : (
                <>
                  <button type="button" className="fy-mem-link" onClick={() => void exportNow()}>
                    {exported ? t("memory.exported") : t("memory.export")}
                  </button>
                  <button type="button" className="fy-quiet" onClick={() => setAsking(true)}>
                    {t("memory.clear")}
                  </button>
                </>
              )}
            </div>
          </div>
        )}
      </Head>

      {doc === null ? (
        failed === "old" ? (
          <div className="fy-mem-nonotes fy-mem-none">
            {t("memory.tooOld", { machine: machine || t("machine.local") })}
          </div>
        ) : failed ? (
          <div className="fy-mem-failed">
            {t("memory.errLoad")}
            <button type="button" className="fy-mem-link" onClick={() => void load()}>
              {t("memory.retry")}
            </button>
          </div>
        ) : (
          <div className="fy-mem-wait">
            <Jelly size={28} busyLabel={t("memory.title")} />
          </div>
        )
      ) : nothing ? (
        <Empty
          title={t("memory.emptyTitle")}
          body={t("memory.emptyBody")}
          link={t("memory.emptyHow")}
          onLink={onHelp}
        />
      ) : (
        <>
          {!pageEmpty(page) && (
            <div className="fy-mem-state">
              <div className="fy-grouphead fy-mem-grouphead">
                <span>{t("memory.stateHead")}</span>
                <i />
                <span>{t("memory.stateNote")}</span>
              </div>
              <div className="fy-mem-cells">
                {PROSE_FIELDS.map((f) => (
                  <Cell key={f} page={page} field={f} tr={tr} onOpen={() => setSheet(f)} />
                ))}
              </div>
              <div className="fy-mem-cells fy-mem-cells-two">
                {LIST_FIELDS.map((f) => (
                  <Cell key={f} page={page} field={f} tr={tr} onOpen={() => setSheet(f)} />
                ))}
              </div>
            </div>
          )}

          <Feed
            doc={doc}
            filter={filter}
            query={query}
            open={open}
            more={more}
            changeSets={changeSets}
            tasks={tasks}
            now={now}
            tr={tr}
            onToggle={(id) => setOpen((o) => (o === id ? null : id))}
            onDelete={(id) => void act(() => source.deleteNote(wsID, id))}
            onLoadMore={() => void loadMore()}
            onGotoTasks={onGotoTasks}
          />
        </>
      )}

      {sheet && (
        <MemorySheet
          page={page}
          field={sheet}
          tr={tr}
          onSave={(text) => savePage(sheet, text)}
          onClose={() => setSheet(null)}
        />
      )}
    </div>
  );
}

/** The shell reports a proxied answer as "core error (404): …": the Core on
 *  the other end predates the memory endpoints. */
function missingEndpoint(err: unknown): boolean {
  return err instanceof Error && err.message.includes("(404)");
}

function Head({
  tr,
  workspace,
  machine,
  doc,
  now,
  children,
}: {
  tr: Translator;
  workspace: Workspace | null;
  machine: string;
  doc: MemoryDoc | null;
  now: Date;
  children?: ReactNode;
}) {
  const { t } = tr;
  const state = doc?.state ?? null;
  const at = state ? new Date(state.updated_at).getTime() : 0;
  return (
    <div className="fy-mem-head">
      <h1 className="fy-display">{t("memory.title")}</h1>
      <div className="fy-mem-sub">
        {machine && <span className="fy-mchip">{machine}</span>}
        {workspace && <span className="fy-mem-name">{workspace.name}</span>}
        {doc && (
          <>
            <span>·</span>
            <span>{t("memory.counts", { live: doc.live, archived: doc.archived })}</span>
          </>
        )}
        {state && at > 0 && (
          <>
            <span>·</span>
            <span>
              {t("memory.rewroteBy", {
                who: state.provider === "user" ? t("memory.you") : displayWho(state.provider ?? ""),
                ago: agoShort(at, now, tr),
              })}
            </span>
          </>
        )}
        {machine && workspace && (
          <span className="fy-mem-onmachine">· {t("memory.onMachine", { machine })}</span>
        )}
      </div>
      {children}
    </div>
  );
}

function Cell({
  page,
  field,
  tr,
  onOpen,
}: {
  page: MemoryPage;
  field: PageField;
  tr: Translator;
  onOpen: () => void;
}) {
  const { t } = tr;
  const items = fieldItems(page, field);
  const count = fieldCount(page, field);
  const limit = fieldLimit(field);
  // Three lines are shown; the fade at the bottom only exists when there is
  // more, which the cell cannot measure before it is drawn. Bytes stand in:
  // a prose field under a line's worth has nothing to fade into, and a list
  // fits when it has three items or fewer.
  const short = isList(field) ? items.length <= 3 : count < 90;
  return (
    <div
      className={[
        "fy-mem-cell",
        short ? "fy-mem-short" : "",
        field === "open" ? "fy-mem-open" : "",
      ].join(" ")}
    >
      <div className="fy-mem-cell-title">
        <span>{t(FIELD_LABELS[field])}</span>
        <small>
          {count} / {limit}
        </small>
      </div>
      <div className="fy-mem-clamp">
        {isList(field) ? (
          items.length > 0 ? (
            <ul className="fy-mem-list">
              {items.map((it, i) => (
                <li key={i}>{it}</li>
              ))}
            </ul>
          ) : (
            <p className="fy-mem-none">{t("memory.emptyField")}</p>
          )
        ) : items.length > 0 ? (
          <p>{items[0]}</p>
        ) : (
          <p className="fy-mem-none">{t("memory.emptyField")}</p>
        )}
      </div>
      <button type="button" className="fy-mem-more" onClick={onOpen}>
        {t("memory.viewAll")}
      </button>
    </div>
  );
}

function Feed({
  doc,
  filter,
  query,
  open,
  more,
  changeSets,
  tasks,
  now,
  tr,
  onToggle,
  onDelete,
  onLoadMore,
  onGotoTasks,
}: {
  doc: MemoryDoc;
  filter: Filter;
  query: string;
  open: number | null;
  more: boolean;
  changeSets: ChangeSet[];
  tasks: TaskInfo[];
  now: Date;
  tr: Translator;
  onToggle: (id: number) => void;
  onDelete: (id: number) => void;
  onLoadMore: () => void;
  onGotoTasks: () => void;
}) {
  const { t } = tr;
  if (doc.notes.length === 0) {
    return (
      <div className="fy-mem-feed">
        <div className="fy-mem-none fy-mem-nonotes">
          {query
            ? t("memory.noResults")
            : filter === "archived"
              ? t("memory.noArchived")
              : t("memory.noNotes")}
        </div>
      </div>
    );
  }
  const row = (note: MemoryNote) => (
    <NoteRow
      key={note.id}
      note={note}
      open={open === note.id}
      changeSets={changeSets}
      tasks={tasks}
      now={now}
      tr={tr}
      onToggle={() => onToggle(note.id)}
      onDelete={() => onDelete(note.id)}
      onGotoTasks={onGotoTasks}
    />
  );
  if (query) {
    return (
      <div className="fy-mem-feed">
        <GroupHead text={t("memory.results")} count={doc.notes.length} />
        {doc.notes.map(row)}
      </div>
    );
  }
  const { recent, earlier } = groupNotes(doc.notes, now);
  return (
    <div className="fy-mem-feed">
      {recent.length > 0 && <GroupHead text={t("record.recent")} count={recent.length} />}
      {recent.map(row)}
      {earlier.length > 0 && <GroupHead text={t("record.earlier")} count={earlier.length} />}
      {earlier.map(row)}
      {doc.next_before_id && (
        <button
          type="button"
          className="fy-mem-more fy-mem-loadmore"
          disabled={more}
          onClick={onLoadMore}
        >
          {t("memory.loadMore")}
        </button>
      )}
    </div>
  );
}

function GroupHead({ text, count }: { text: string; count: number }) {
  return (
    <div className="fy-grouphead fy-mem-grouphead">
      <span>{text}</span>
      <i />
      <span>{count}</span>
    </div>
  );
}

function NoteRow({
  note,
  open,
  changeSets,
  tasks,
  now,
  tr,
  onToggle,
  onDelete,
  onGotoTasks,
}: {
  note: MemoryNote;
  open: boolean;
  changeSets: ChangeSet[];
  tasks: TaskInfo[];
  now: Date;
  tr: Translator;
  onToggle: () => void;
  onDelete: () => void;
  onGotoTasks: () => void;
}) {
  const { t, tn } = tr;
  const tone = noteTone(note);
  const who = displayWho(note.provider ?? "");
  const set = note.change_set_id
    ? (changeSets.find((c) => c.id === note.change_set_id) ?? null)
    : null;
  const task = note.run_id ? (tasks.find((k) => k.task_id === note.run_id) ?? null) : null;
  const meta = [
    who,
    tone === "summary"
      ? t("memory.summaryMeta")
      : set
        ? t("memory.setMeta", { count: tn("record.files", (set.operations ?? []).length) })
        : note.change_set_id
          ? t("memory.changeSet")
          : (task?.label ?? ""),
  ].filter(Boolean);

  return (
    <div
      className={`fy-mem-row${note.archived ? " fy-mem-archived" : ""}`}
      data-open={open ? "true" : "false"}
    >
      <div
        className="fy-mem-rhead"
        role="button"
        tabIndex={0}
        aria-expanded={open}
        aria-label={open ? t("record.collapse") : t("record.expand")}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onToggle();
          }
        }}
      >
        <span className="fy-mem-time">{noteWhen(note.created_at, now, tr)}</span>
        <span className={`fy-mem-dot fy-mem-dot-${tone}`} aria-hidden="true" />
        <span className="fy-mem-title" title={note.title}>
          {note.title}
        </span>
        <span className="fy-mem-meta">
          {meta.map((m, i) => (
            <span key={i}>
              {i > 0 && <span className="fy-mem-sep">·</span>}
              {m}
            </span>
          ))}
        </span>
        <span className="fy-mem-chev" aria-hidden="true" />
      </div>

      <div className="fy-mem-panel" inert={!open}>
        <div className="fy-mem-body">{note.body || t("memory.emptyField")}</div>
        <div className="fy-mem-ledger">
          <div>
            <span className="fy-mem-k">{t("record.hSource")}</span>
            <div className="fy-mem-v">{who}</div>
            <div className="fy-mem-d">{startedAt(note.created_at, now, tr)}</div>
          </div>
          {note.change_set_id && (
            <div>
              <span className="fy-mem-k">{t("memory.hChangeSet")}</span>
              <div className="fy-mem-v">
                {set ? (
                  <button type="button" className="fy-mem-ref" onClick={onGotoTasks}>
                    {setFiles(set, tr)}
                  </button>
                ) : (
                  t("memory.changeSet")
                )}
              </div>
              <div className="fy-mem-d">
                {set ? setState(set, now, tr) : t("memory.recordGone")}
              </div>
            </div>
          )}
          {note.run_id && (
            <div>
              <span className="fy-mem-k">{t("record.hCommand")}</span>
              <div className="fy-mem-v">
                {task ? (
                  <button type="button" className="fy-mem-ref" onClick={onGotoTasks}>
                    {task.label?.trim() || t("task.unnamed")}
                  </button>
                ) : (
                  t("record.hCommand")
                )}
              </div>
              <div
                className="fy-mem-d"
                style={task?.state === "succeeded" ? { color: "var(--fy-sage)" } : undefined}
              >
                {task ? taskState(task, tr) : t("memory.recordGone")}
              </div>
            </div>
          )}
          <button type="button" className="fy-quiet fy-mem-delete" onClick={onDelete}>
            {t("memory.delete")}
          </button>
        </div>
      </div>
      <div className="fy-mem-rule" />
    </div>
  );
}

/** "4 files · idle.go and more": the count, then the first path's name. */
function setFiles(set: ChangeSet, { t, tn }: Translator): string {
  const ops = set.operations ?? [];
  const count = tn("record.files", ops.length);
  if (ops.length === 0) return count;
  const name = ops[0].path.split("/").pop() ?? ops[0].path;
  return ops.length > 1
    ? t("memory.filesAndMore", { count, file: name })
    : t("memory.files", { count, file: name });
}

function setState(set: ChangeSet, now: Date, tr: Translator): string {
  const word = entryStatusWord({ kind: "write", id: set.id, at: 0, set }, tr);
  if (canRollback(set, now) && set.rollback_deadline) {
    return `${word} · ${tr.t("memory.undoUntil", { time: clock(set.rollback_deadline) })}`;
  }
  return word;
}

function taskState(task: TaskInfo, tr: Translator): string {
  const word = entryStatusWord({ kind: "task", id: task.task_id, at: 0, task }, tr);
  return task.state === "running" ? word : `${word} · ${duration(task.duration)}`;
}

function Empty({
  title,
  body,
  link,
  onLink,
}: {
  title: string;
  body: string;
  link: string;
  onLink: () => void;
}) {
  return (
    <div className="fy-empty fy-mem-empty">
      <div className="fy-empty-inner">
        <span className="fy-empty-gate" aria-hidden="true">
          <i />
          <i />
        </span>
        <div style={{ maxWidth: 420 }}>
          <div className="fy-display fy-mem-empty-title">{title}</div>
          <p className="fy-mem-empty-body">{body}</p>
          <button type="button" className="fy-underbtn" style={{ marginTop: 12 }} onClick={onLink}>
            {link}
          </button>
        </div>
      </div>
    </div>
  );
}
