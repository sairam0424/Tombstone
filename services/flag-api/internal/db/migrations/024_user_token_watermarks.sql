-- Migration 024: per-subject token revocation watermark (SEC-5).
--
-- SCIM deprovisioning (revokeUserRoles) already deletes every user_roles row
-- for a deprovisioned email, but a JWT issued before deprovisioning remains
-- cryptographically valid until it naturally expires (24h) -- nothing has
-- ever invalidated an already-issued Tombstone JWT early. Because
-- RequireProjectID/LoadRole re-query user_roles fresh on every request with
-- no cache, a deprovisioned user is already denied at the authorization
-- layer within one request cycle -- but a security team responding to a
-- suspected compromised session (account NOT being deprovisioned) has no
-- way to force that one session to re-authenticate.
--
-- This table lets validateJWT reject any token whose iat predecessor is
-- older than the subject's most recent forced-logout timestamp, without
-- needing a growing denylist or a cleanup job: one row per subject,
-- upserted, never deleted.
CREATE TABLE IF NOT EXISTS user_token_watermarks (
    user_email  TEXT PRIMARY KEY,
    valid_after TIMESTAMPTZ NOT NULL DEFAULT now()
);
