-- Relay metadata only (allow list): devices, OAuth grant metadata,
-- online status, call counts, error codes, latency. Local absolute paths,
-- file/diff bodies, directory listings, chat content, tool response bodies,
-- and sensitive file names must never gain columns here.
CREATE TABLE devices (
    id             TEXT PRIMARY KEY,
    secret_hash    TEXT NOT NULL,
    name           TEXT NOT NULL DEFAULT '',
    version        TEXT NOT NULL DEFAULT '',
    online         BOOLEAN NOT NULL DEFAULT FALSE,
    last_online_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE pairing_codes (
    code       TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices (id),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE clients (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL DEFAULT '',
    redirect_uris JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);

CREATE TABLE auth_requests (
    id             TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL REFERENCES clients (id),
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE auth_codes (
    code           TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL REFERENCES clients (id),
    device_id      TEXT NOT NULL REFERENCES devices (id),
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE access_tokens (
    token      TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices (id),
    client_id  TEXT NOT NULL REFERENCES clients (id),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE refresh_tokens (
    token      TEXT PRIMARY KEY,
    family     TEXT NOT NULL,
    device_id  TEXT NOT NULL REFERENCES devices (id),
    client_id  TEXT NOT NULL REFERENCES clients (id),
    expires_at TIMESTAMPTZ NOT NULL,
    used       BOOLEAN NOT NULL DEFAULT FALSE,
    revoked    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family);

CREATE TABLE device_stats (
    device_id        TEXT PRIMARY KEY REFERENCES devices (id),
    call_count       BIGINT NOT NULL DEFAULT 0,
    error_count      BIGINT NOT NULL DEFAULT 0,
    last_error_code  TEXT NOT NULL DEFAULT '',
    total_latency_ms BIGINT NOT NULL DEFAULT 0,
    last_call_at     TIMESTAMPTZ
);
