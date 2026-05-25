-- +goose Up
-- +goose StatementBegin

-- Per-user session version counter for the slice-3b admin
-- force-sign-out-user control. Each issued JWT carries the
-- user's current session_version as a "sv" claim; the auth
-- middleware rejects the token when claims.sv < users.session_version.
-- Bumping the column therefore invalidates every outstanding
-- session for that user in a single UPDATE — useful for incident
-- response ("a teammate left, kill all their devices now") and
-- for the SOC 2 "terminate user sessions on demand" control.
--
-- Per-jti revocation (PR #212 / migration 00016) remains the path
-- for "sign me out of this device"; the version counter is the
-- per-user nuke. Both checks are AND-ed by the middleware.
--
-- Pre-slice-3b tokens minted without an sv claim default to 0; an
-- existing user row defaults to session_version=0, so old tokens
-- keep working until the first bump. After the first
-- sign-out-all call, every pre-existing session is invalidated —
-- operators should be aware of that "first bump kills all legacy
-- tokens" effect on rollout.
ALTER TABLE users
    ADD COLUMN session_version INTEGER NOT NULL DEFAULT 0;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE users
    DROP COLUMN IF EXISTS session_version;

-- +goose StatementEnd
