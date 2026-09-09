-- OAuth/pairing metadata only. This schema mirrors the relay's PostgreSQL
-- store: devices, grant metadata, credential hashes. File bodies, diffs,
-- listings, absolute paths, and sensitive file names must never gain a column
-- here, whichever process owns the file.
--
-- Every timestamp is TEXT in RFC3339Nano UTC, matching the Companion store.
CREATE TABLE devices (
    id          TEXT PRIMARY KEY,
    secret_hash TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE TABLE pairing_codes (
    code       TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices (id),
    expires_at TEXT NOT NULL
);

CREATE TABLE clients (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL DEFAULT '',
    redirect_uris TEXT NOT NULL DEFAULT '[]',
    created_at    TEXT NOT NULL
);

CREATE TABLE auth_requests (
    id             TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL REFERENCES clients (id),
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT '',
    verify_code    TEXT NOT NULL DEFAULT '',
    device_id      TEXT NOT NULL DEFAULT '',
    continue_nonce TEXT NOT NULL DEFAULT '',
    expires_at     TEXT NOT NULL
);

CREATE TABLE auth_codes (
    code           TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL REFERENCES clients (id),
    device_id      TEXT NOT NULL REFERENCES devices (id),
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    expires_at     TEXT NOT NULL
);

CREATE TABLE access_tokens (
    token      TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES devices (id),
    client_id  TEXT NOT NULL REFERENCES clients (id),
    expires_at TEXT NOT NULL
);

CREATE TABLE refresh_tokens (
    token      TEXT PRIMARY KEY,
    family     TEXT NOT NULL,
    device_id  TEXT NOT NULL REFERENCES devices (id),
    client_id  TEXT NOT NULL REFERENCES clients (id),
    expires_at TEXT NOT NULL,
    used       INTEGER NOT NULL DEFAULT 0,
    revoked    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family);
