-- Project memory (Batch S, D36, 2026-09-12): what a model should know when
-- a new conversation opens on a workspace it has worked in before.
--
-- Two layers, on purpose. memory_state is one page per workspace that is
-- rewritten, never appended to, so what a conversation starts from stays
-- the same size in its first week and its hundredth. memory_notes is the
-- append-only trail behind it; it is searched and paged, never returned
-- whole. A note may point at the change set or the command run it is
-- about, which is why this lives in the same database as those records.
--
-- Nothing here is a file in the user's project: the memory belongs to the
-- Companion that holds the workspace, and leaves it only as an export the
-- user asks for.
CREATE TABLE memory_state (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces (id),
    page         TEXT NOT NULL,
    provider     TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL
);

CREATE TABLE memory_notes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id),
    provider      TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    change_set_id TEXT NOT NULL DEFAULT '',
    run_id        TEXT NOT NULL DEFAULT '',
    -- archived marks a note whose content has been folded into the page
    -- by a compaction; it stays readable by id but is no longer listed.
    archived      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE INDEX memory_notes_workspace ON memory_notes (workspace_id, archived, id);
