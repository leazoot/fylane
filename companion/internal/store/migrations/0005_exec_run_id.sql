-- Pair the start of a command with its end (2026-08-30).
--
-- Until now a row was written only when a command reached an outcome. That is
-- fine while the Core lives: the engine always reaches one. It is exactly
-- wrong when the Core does not — a restart kills the child process, the
-- engine's call never returns, and the command leaves no trace anywhere. The
-- task screen showed nothing, and task_status answered "unknown task id" for
-- work that really had run and really had touched the disk.
--
-- So a run now writes twice: 'started' when the process is up, and its real
-- outcome when it ends. A 'started' row with no sibling is a command Fylane
-- was running when it stopped, and startup turns each one into an
-- 'interrupted' row. The table stays append-only — the orphan is answered,
-- never edited.
--
-- Rows written before this column existed keep an empty run_id. They are
-- complete records of finished commands; they simply predate the pairing, and
-- reconciliation ignores them because none of them is 'started'.
ALTER TABLE exec_events ADD COLUMN run_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_exec_events_run ON exec_events (run_id);
