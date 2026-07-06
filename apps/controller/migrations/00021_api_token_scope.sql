-- +goose Up
-- +goose StatementBegin

-- API token scopes (audit M-4). v1 granted every token full tenant-admin;
-- a leaked CI/monitoring token was therefore full admin. `scope` lets a
-- token be minted read-only so it can drive read endpoints without the
-- power to mutate policy, revoke keys, or delete peers.
--
--   'admin'     — full tenant-admin (the v1 behavior; the default so
--                 every existing token keeps working unchanged).
--   'read-only' — authenticates + passes read endpoints, but requireAdmin
--                 rejects it, so all mutations 403.
--
-- Additive with a DEFAULT, so the migration backfills existing rows to
-- 'admin' with no data touch-up. The CHECK bounds the value at the DB so
-- a bad write can't smuggle in an unrecognized scope.
ALTER TABLE api_tokens
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'admin'
        CHECK (scope IN ('admin', 'read-only'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE api_tokens DROP COLUMN scope;
-- +goose StatementEnd
