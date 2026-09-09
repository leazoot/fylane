-- Per-workspace authorization to run commands without asking each time
-- (middle rung). One row means "the user said yes to commands in
-- this workspace"; deleting the row is the revoke.
--
-- Grants do not expire. A grant that lapsed every day or every restart would
-- interrupt the user on a schedule, and the rung people reach for when a tool
-- nags them is the open one — which is strictly worse. The cost of "approved
-- and forgot" is paid instead by showing the grant permanently in the desktop
-- UI with a revoke button.
--
-- rung records which rung was in force when the grant was made, so a grant
-- taken under one policy is not silently reused under a laxer one.
CREATE TABLE command_grants (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces (id),
    rung         TEXT NOT NULL,
    granted_at   TEXT NOT NULL
);
