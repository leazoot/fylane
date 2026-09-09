-- root_path exists only in this local database and
-- must never be serialized into MCP responses, Relay traffic, or logs.
CREATE TABLE workspaces (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    root_path       TEXT NOT NULL,
    mode            TEXT NOT NULL CHECK (mode IN ('read_only', 'read_write')),
    exclude_rules   TEXT NOT NULL DEFAULT '[]',
    sensitive_rules TEXT NOT NULL DEFAULT '[]',
    created_at      TEXT NOT NULL,
    last_used_at    TEXT,
    status          TEXT NOT NULL
);

-- token_reference is a keychain/encrypted-store lookup key, never a raw
-- OAuth token or secret.
CREATE TABLE connectors (
    id                  INTEGER PRIMARY KEY,
    provider            TEXT NOT NULL,
    remote_connector_id TEXT NOT NULL,
    status              TEXT NOT NULL,
    capabilities        TEXT NOT NULL DEFAULT '[]',
    last_connected_at   TEXT,
    last_tool_call_at   TEXT,
    token_reference     TEXT NOT NULL DEFAULT '',
    UNIQUE (provider, remote_connector_id)
);

CREATE TABLE change_sets (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces (id),
    provider          TEXT NOT NULL,
    summary           TEXT NOT NULL,
    operations        TEXT NOT NULL,
    status            TEXT NOT NULL,
    before_hashes     TEXT NOT NULL DEFAULT '{}',
    after_hashes      TEXT NOT NULL DEFAULT '{}',
    backup_location   TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    approved_at       TEXT,
    applied_at        TEXT,
    rollback_deadline TEXT
);

CREATE INDEX idx_change_sets_workspace ON change_sets (workspace_id, created_at);

CREATE TABLE audit_events (
    id            INTEGER PRIMARY KEY,
    change_set_id TEXT REFERENCES change_sets (id),
    event_type    TEXT NOT NULL,
    path_relative TEXT NOT NULL DEFAULT '',
    result        TEXT NOT NULL,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_audit_events_change_set ON audit_events (change_set_id);
