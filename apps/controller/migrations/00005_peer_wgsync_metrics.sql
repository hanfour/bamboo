-- +goose Up
-- +goose StatementBegin

-- Peer wgsync metrics (PR-2 follow-up to the wg-state-reporter
-- landing in #45). The reporter currently only mirrors handshake
-- liveness into peers.last_seen_at; this migration lets it
-- additionally surface what the host's wg dump knows about each
-- peer (endpoint the hub sees, byte counters, raw handshake time).
--
-- Why distinct from last_seen_at: last_seen_at is also bumped by
-- client Heartbeat over REST, which fires before any WG handshake
-- has happened. last_handshake_at is *strictly* the WG handshake
-- timestamp from the reporter, so the UI can show "尚未握手"
-- (never handshook) without ambiguity. NULL means the reporter
-- hasn't seen a non-zero handshake for this peer.
--
-- Why endpoint is nullable: wg dump prints "(none)" for peers
-- that have not handshook yet. The reporter passes empty, which
-- the repo turns into NULL via COALESCE so a stale dump doesn't
-- erase a previously-observed endpoint.
--
-- Why bytes default to 0 NOT NULL: the wg counters are always
-- present (even pre-handshake they read as 0). Removing the
-- nullable case keeps the JSON wire shape simpler — the UI
-- guards on last_handshake_at IS NULL alone.
ALTER TABLE peers
    ADD COLUMN wg_endpoint       TEXT,
    ADD COLUMN rx_bytes          BIGINT      NOT NULL DEFAULT 0,
    ADD COLUMN tx_bytes          BIGINT      NOT NULL DEFAULT 0,
    ADD COLUMN last_handshake_at TIMESTAMPTZ;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE peers
    DROP COLUMN wg_endpoint,
    DROP COLUMN rx_bytes,
    DROP COLUMN tx_bytes,
    DROP COLUMN last_handshake_at;

-- +goose StatementEnd
