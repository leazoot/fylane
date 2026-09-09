-- Push pairing: auth requests carry a display-only verify code and,
-- once a device approves, the approving device and the hashed single-use
-- continuation nonce. Metadata only — the nonce is stored hashed
-- like every other credential.

ALTER TABLE auth_requests
    ADD COLUMN verify_code    TEXT NOT NULL DEFAULT '',
    ADD COLUMN device_id      TEXT NOT NULL DEFAULT '',
    ADD COLUMN continue_nonce TEXT NOT NULL DEFAULT '';
