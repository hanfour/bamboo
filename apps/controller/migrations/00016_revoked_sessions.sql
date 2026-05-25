-- +goose Up
-- +goose StatementBegin

-- Server-side session revocation denylist for the SOC 2 "session
-- terminations on demand" control. JWTs carry a jti (UUID) claim;
-- the REST + gRPC auth middleware rejects requests whose jti appears
-- in this table. The /auth/sign-out handler inserts a row, so a
-- leaked / stolen JWT can be invalidated before its natural TTL.
--
-- expires_at mirrors the JWT's exp claim — once that timestamp is in
-- the past, the JWT is rejected on its own merits and the row is
-- redundant. The hourly reaper deletes redundant rows so the table
-- doesn't grow without bound.
--
-- user_id is recorded for audit / "list revoked sessions for user X"
-- queries; ON DELETE CASCADE so a user erasure (PR #208) takes their
-- revocation rows with them.
CREATE TABLE revoked_sessions (
    jti        UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    revoked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

-- Reaper sweeps `WHERE expires_at < now()` — partial / covering
-- index keeps the scan cheap on a table that may grow to thousands
-- of rows on a tenant with churny short-TTL sessions.
CREATE INDEX idx_revoked_sessions_expires_at ON revoked_sessions(expires_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS revoked_sessions;

-- +goose StatementEnd
