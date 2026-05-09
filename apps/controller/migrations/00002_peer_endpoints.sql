-- +goose Up
-- +goose StatementBegin

-- Peer endpoint discovery (Phase 2 theme 4 follow-up).
--
-- A peer's endpoint is the host:port pair other peers use to dial it
-- directly via UDP. Currently populated by the client itself after
-- STUN-style discovery, reported on Register and refreshed on every
-- Heartbeat. The column is an array because a peer can plausibly
-- expose multiple candidates (LAN address + STUN-discovered public
-- address); the WireGuard side picks the one that handshakes first.
ALTER TABLE peers
    ADD COLUMN endpoints TEXT[] NOT NULL DEFAULT '{}';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE peers DROP COLUMN endpoints;

-- +goose StatementEnd
