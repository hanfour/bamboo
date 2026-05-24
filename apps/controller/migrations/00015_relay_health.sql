-- +goose Up
-- +goose StatementBegin

-- Relay health-check state for the §4 P2 multi-relay registry.
--
-- The controller probes each enabled relay every relayHealthInterval
-- (default 30s) via HTTP GET /healthz. Result lands here; the relay
-- list served to clients in RegisterResponse + the WatchPeers
-- RelaysChanged event filters out anything flipped to 'unhealthy'.
--
-- Status values:
--   'unknown'   — newly inserted, never probed yet. Treated as
--                 ELIGIBLE so a fresh relay reaches clients before
--                 its first probe lands. Defensive: a stuck reaper
--                 must not silently hide every new relay.
--   'healthy'   — last probe returned 2xx within deadline.
--   'unhealthy' — last probe failed (timeout, non-2xx, TLS error).
--                 last_health_error carries the short reason so the
--                 admin UI / Web can surface "why is this relay
--                 down?" without grepping controller logs.
--
-- Columns are nullable for the rollout: existing rows get NULLs on
-- migration up; the reaper backfills on first probe. Querying with
-- COALESCE(last_health_status, 'unknown') normalizes.
ALTER TABLE relay_servers
    ADD COLUMN last_health_check_at TIMESTAMPTZ,
    ADD COLUMN last_health_status   TEXT,
    ADD COLUMN last_health_error    TEXT;

-- Bounded set of legal statuses. Mirrors the controller's enum but
-- enforced at the DB so a stray UPDATE from a future ops script can
-- not poison the table with a typo'd 'helathy'.
ALTER TABLE relay_servers
    ADD CONSTRAINT relay_servers_health_status_valid
        CHECK (last_health_status IS NULL
            OR last_health_status IN ('unknown', 'healthy', 'unhealthy'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE relay_servers
    DROP CONSTRAINT IF EXISTS relay_servers_health_status_valid,
    DROP COLUMN IF EXISTS last_health_check_at,
    DROP COLUMN IF EXISTS last_health_status,
    DROP COLUMN IF EXISTS last_health_error;

-- +goose StatementEnd
