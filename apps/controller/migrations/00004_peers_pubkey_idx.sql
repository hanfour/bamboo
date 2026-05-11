-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Promote the per-tenant pubkey uniqueness to a global UNIQUE
-- index.
--
-- The 00001 schema declared:
--     CONSTRAINT peers_tenant_pubkey_unique UNIQUE (tenant_id,
--                                                   wireguard_public_key)
-- which permits two tenants to share a pubkey. WireGuard's
-- cryptokey routing operates on pubkey alone, so a shared pubkey
-- across tenants would be operationally meaningless: the wg
-- interface couldn't tell which tenant a packet belongs to.
-- Promoting to global UNIQUE matches reality.
--
-- A side benefit: the wg-state reporter (see internal/wgsync) keys
-- updates by pubkey alone. Without a single-column index the query
-- falls back to a sequential scan; this index makes it an O(1)
-- lookup.
--
-- The CREATE will fail (with a clear "Key (...)=(...) is duplicated"
-- message) if existing data has cross-tenant duplicates, which is
-- the safe default — operators must dedup before migrating.

-- +goose Up
-- +goose StatementBegin

CREATE UNIQUE INDEX peers_pubkey_global_unique
    ON peers (wireguard_public_key);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS peers_pubkey_global_unique;

-- +goose StatementEnd
