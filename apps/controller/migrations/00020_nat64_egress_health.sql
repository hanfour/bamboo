-- +goose Up
-- +goose StatementBegin

-- NAT64 Phase C3 — per-peer egress translator health, for health-aware
-- single-active failover. The egress CLI self-reports its Tayga liveness
-- on the heartbeat (PR 1); the controller persists it here and skips a
-- confirmed-'unhealthy' (or stale, via last_seen_at) egress when picking
-- the active translator. See docs/design/2026-06-06-nat64-phase-c3-egress-health.md.
--
-- Status values (mirror the relay-health enum):
--   NULL / 'unknown' — never reported. Treated as ELIGIBLE so a fresh
--                      egress routes before its first heartbeat.
--   'healthy'        — last self-report said the translator is up.
--   'unhealthy'      — last self-report said it is down, OR (PR 3's
--                      reaper) the peer went stale. nat64_egress_health_reason
--                      carries the short admin-facing reason
--                      ('translator down' / 'stale').
-- Columns are nullable for the rollout; querying normalizes via the
-- isEgressEligible predicate (NULL = eligible).
ALTER TABLE peers
    ADD COLUMN nat64_egress_health_status TEXT,
    ADD COLUMN nat64_egress_health_reason TEXT;

ALTER TABLE peers
    ADD CONSTRAINT peers_nat64_egress_health_status_valid
        CHECK (nat64_egress_health_status IS NULL
            OR nat64_egress_health_status IN ('unknown', 'healthy', 'unhealthy'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE peers
    DROP CONSTRAINT IF EXISTS peers_nat64_egress_health_status_valid,
    DROP COLUMN IF EXISTS nat64_egress_health_status,
    DROP COLUMN IF EXISTS nat64_egress_health_reason;

-- +goose StatementEnd
