-- +goose Up
-- +goose StatementBegin

-- NAT64 Phase A — overlay IPv6 foundation.
-- Each peer gains a deterministic IPv6 ULA derived from its IPv4 (the
-- v4's 32 bits embedded in the low 32 bits of the tenant's /64 pool).
-- Also lands the three Phase C translator hooks up-front so the
-- translator work does not force a second peers migration later.
-- See docs/design/2026-05-28-nat64-phase-a-overlay-ipv6.md.

ALTER TABLE tenants ADD COLUMN ip6_pool CIDR NOT NULL DEFAULT 'fdba:1100::/64';
ALTER TABLE peers   ADD COLUMN ip6 INET;   -- nullable until backfill below

-- Phase C hooks (umbrella §5) — present now, exercised in Phase C.
ALTER TABLE peers   ADD COLUMN nat64_egress_capable  BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE peers   ADD COLUMN nat64_egress_approved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tenants ADD COLUMN nat64_prefix          TEXT;   -- NULL = well-known 64:ff9b::/96

-- Eager backfill: derive each existing peer's v6 by adding the v4's
-- 32-bit integer offset to the tenant's v6 pool network address.
-- PostgreSQL inet arithmetic: (inet - inet) -> bigint, (inet + bigint)
-- -> inet. 100.64.0.5 offset = 0x64400005 ; fdba:1100:: + that =
-- fdba:1100::6440:5. The /64 pool's low 64 host bits are zero, so the
-- v4 offset lands in the low 32 bits with no carry into the prefix.
UPDATE peers p
SET ip6 = (host(t.ip6_pool)::inet + (p.ip - '0.0.0.0'::inet))::inet
FROM tenants t
WHERE p.tenant_id = t.id;

-- ip6 stays NULLABLE: production always sets it (register → NextFreeDual),
-- and existing rows are backfilled above, but the repo Insert primitive is
-- exercised by tests/tools without an ip6, so a NOT NULL here would reject
-- those valid inserts. The (tenant_id, ip6) unique index still guarantees
-- no two real peers collide (Postgres treats NULLs as distinct).
ALTER TABLE peers ADD CONSTRAINT peers_tenant_ip6_unique UNIQUE (tenant_id, ip6);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE peers   DROP CONSTRAINT IF EXISTS peers_tenant_ip6_unique;
ALTER TABLE tenants DROP COLUMN IF EXISTS nat64_prefix;
ALTER TABLE peers   DROP COLUMN IF EXISTS nat64_egress_approved;
ALTER TABLE peers   DROP COLUMN IF EXISTS nat64_egress_capable;
ALTER TABLE peers   DROP COLUMN IF EXISTS ip6;
ALTER TABLE tenants DROP COLUMN IF EXISTS ip6_pool;

-- +goose StatementEnd
