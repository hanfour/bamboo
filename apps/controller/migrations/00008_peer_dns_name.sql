-- +goose Up
-- +goose StatementBegin

-- peer_dns_name is the short, DNS-safe label a peer is reachable
-- under inside the tenant's tailnet — e.g. `mac-mini.bamboo`. Stored
-- separately from `hostname` so the user-facing device name ("Mac
-- mini of 黃柏漢") can remain free-form while the resolvable label
-- stays ASCII / [a-z0-9-]. Nullable so legacy peers (registered
-- before this column existed) keep working until the next register
-- backfills them via the coordinator's auto-slug path; admins can
-- also rename via PATCH /api/v1/peers/{id}.
ALTER TABLE peers ADD COLUMN peer_dns_name TEXT;

-- Partial unique index: one peer_dns_name per tenant when the column
-- is set, but multiple NULLs are allowed so the backfill window
-- doesn't collide. The coordinator's ResolveDNSName logic is the
-- write-side guard; this constraint is the floor that catches races
-- and admin mistakes.
CREATE UNIQUE INDEX peers_tenant_dnsname_unique
    ON peers (tenant_id, peer_dns_name)
    WHERE peer_dns_name IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS peers_tenant_dnsname_unique;
ALTER TABLE peers DROP COLUMN IF EXISTS peer_dns_name;

-- +goose StatementEnd
