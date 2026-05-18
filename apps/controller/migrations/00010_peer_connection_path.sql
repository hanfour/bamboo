-- +goose Up
-- +goose StatementBegin

-- Connection-path diagnostics (issue #138).
--
-- Peers report whether their data plane is currently using a direct
-- WG tunnel or the relay fallback. The controller stores the most-
-- recent reported value (and when it was reported) so the Web UI can
-- show a ⚡ / 🔄 indicator alongside the regular online/offline
-- status. Distinct from peers.status which is liveness; this is
-- "how is the peer reaching the mesh, currently".
--
-- Values: 'direct' (P2P WG handshake succeeded), 'relay' (NAT
-- traversal failed; using the relay fallback), 'unknown' (legacy /
-- not yet reported / pre-#138 controller). NULL is treated as
-- 'unknown' for back-compat.
--
-- This intentionally stays on the peers row rather than a separate
-- rolling-window table — admin-time visibility of "where is this
-- peer reaching the mesh from" is the v1 use case; a real connection
-- timeline lands as a ClickHouse-backed follow-up if usage warrants
-- it.
ALTER TABLE peers ADD COLUMN connection_path TEXT
    CHECK (connection_path IS NULL OR connection_path IN ('direct', 'relay', 'unknown'));
ALTER TABLE peers ADD COLUMN connection_path_at TIMESTAMPTZ;
ALTER TABLE peers ADD COLUMN connection_latency_ms INTEGER;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE peers DROP COLUMN IF EXISTS connection_latency_ms;
ALTER TABLE peers DROP COLUMN IF EXISTS connection_path_at;
ALTER TABLE peers DROP COLUMN IF EXISTS connection_path;

-- +goose StatementEnd
