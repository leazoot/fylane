-- Which platform asked for a command (2026-08-30).
--
-- The task screen used to read the in-memory task list, so it forgot every
-- command whenever the Core restarted and could not show a command the user
-- had refused — a refusal never becomes a task. It now reads this table
-- instead, and a history row has to be able to say who asked, the way the
-- live list always could.
--
-- Rows written before this column existed keep an empty provider. That is the
-- honest value: nothing recorded who asked, and guessing would put a platform
-- name on a command it may not have run.
ALTER TABLE exec_events ADD COLUMN provider TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_exec_events_workspace ON exec_events (workspace_id, created_at);
