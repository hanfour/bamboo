-- +goose Up
-- +goose StatementBegin

-- Relay server registry.
--
-- A relay server is a publicly-reachable bamboo deployment that
-- proxies WireGuard packets between peers behind symmetric NAT (where
-- a direct STUN-discovered tunnel cannot form). See ADR 0013 for the
-- full design.
--
-- Rows are tenant-agnostic: every relay can serve every tenant the
-- controller knows about, because the relay verifies tenant scoping
-- via JWT claims at connection time. This keeps the registry simple
-- and lets us share infrastructure across tenants.
CREATE TABLE relay_servers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    region      TEXT        NOT NULL,                  -- e.g. "ap-northeast-1"
    hostname    TEXT        NOT NULL,                  -- DNS name; clients dial wss://<hostname>:<port>/relay
    port        INTEGER     NOT NULL,                  -- TLS port
    public_key  TEXT        NOT NULL,                  -- relay's Curve25519 pubkey, base64
    enabled     BOOLEAN     NOT NULL DEFAULT true,     -- soft-disable for maintenance
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ,
    CONSTRAINT relay_servers_hostname_port_unique UNIQUE (hostname, port),
    CONSTRAINT relay_servers_port_valid           CHECK (port BETWEEN 1 AND 65535)
);

CREATE INDEX relay_servers_enabled_region_idx
    ON relay_servers (region)
    WHERE enabled = true AND deleted_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE relay_servers;

-- +goose StatementEnd
