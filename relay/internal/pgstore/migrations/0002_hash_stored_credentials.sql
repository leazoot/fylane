-- Access tokens, refresh tokens, and authorization codes are now stored as
-- SHA-256 hashes (issued raw exactly once, like device secrets). Rows written
-- before this change hold raw values that no hashed lookup can match, so they
-- are dead weight: clear them and force re-authentication.
DELETE FROM access_tokens;
DELETE FROM refresh_tokens;
DELETE FROM auth_codes;
