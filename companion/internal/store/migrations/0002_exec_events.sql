-- Execution audit (a separate table rather than a
-- generic detail column on audit_events: a command's fields barely overlap a
-- file change's, and merging them would give both sides a row of nullable
-- columns and a query that dispatches on event_type).
--
-- No absolute path is ever stored: dir_relative is workspace-relative and
-- argv is the argv the caller wrote, not the resolved program path.
--
-- Refused attempts are rows too. A command that was blocked is the more
-- interesting security record, not the less.
CREATE TABLE exec_events (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT REFERENCES workspaces (id),
    dir_relative TEXT NOT NULL DEFAULT '',
    argv         TEXT NOT NULL,
    outcome      TEXT NOT NULL,
    exit_code    INTEGER NOT NULL DEFAULT 0,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    rule_id      TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_exec_events_created ON exec_events (created_at);
