-- Beta metrics: connection counts and per-error-code breakdowns.
-- Both stay inside the allowed categories (call counts, error codes);
-- no content-bearing columns.

ALTER TABLE device_stats
    ADD COLUMN connect_count BIGINT NOT NULL DEFAULT 0;

CREATE TABLE device_error_codes (
    device_id  TEXT NOT NULL REFERENCES devices (id),
    error_code TEXT NOT NULL,
    hits       BIGINT NOT NULL DEFAULT 0,
    last_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (device_id, error_code)
);
