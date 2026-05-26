-- +goose Up
-- +goose StatementBegin

-- Required extensions
CREATE EXTENSION IF NOT EXISTS "pgcrypto";  -- gen_random_uuid()

-- ----------------------------------------------------------------------
-- tenants
-- ----------------------------------------------------------------------
CREATE TABLE tenants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    slug        TEXT        NOT NULL,
    -- Per-tenant IP allocation pool. The server default for new
    -- tenants is 100.127.0.0/24 (top /24 of CGNAT 100.64.0.0/10),
    -- chosen to minimise collision with Tailscale / Headscale meshes
    -- on hosts that run both side-by-side — those clients allocate
    -- sequentially from the low end of /10. Operators wanting tighter
    -- isolation can override per-tenant; any CIDR carved out of
    -- 100.64.0.0/10 (or a private RFC 1918 range) is valid here.
    ip_pool     CIDR        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ,
    CONSTRAINT tenants_slug_unique UNIQUE (slug)
);

-- ----------------------------------------------------------------------
-- users
-- ----------------------------------------------------------------------
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email           TEXT        NOT NULL,
    display_name    TEXT,
    -- OIDC identity. Multiple providers per user are not supported in v1.
    oidc_provider   TEXT        NOT NULL,
    oidc_subject    TEXT        NOT NULL,
    is_admin        BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT users_tenant_email_unique UNIQUE (tenant_id, email),
    CONSTRAINT users_oidc_unique UNIQUE (tenant_id, oidc_provider, oidc_subject)
);
CREATE INDEX users_tenant_idx ON users (tenant_id);

-- ----------------------------------------------------------------------
-- user_groups (named to avoid SQL keyword `groups`)
-- ----------------------------------------------------------------------
CREATE TABLE user_groups (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_groups_tenant_name_unique UNIQUE (tenant_id, name)
);
CREATE INDEX user_groups_tenant_idx ON user_groups (tenant_id);

CREATE TABLE user_group_members (
    group_id  UUID        NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id   UUID        NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX user_group_members_user_idx ON user_group_members (user_id);

-- ----------------------------------------------------------------------
-- peers (devices)
-- ----------------------------------------------------------------------
CREATE TABLE peers (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id               UUID                 REFERENCES users(id)   ON DELETE SET NULL,
    hostname              TEXT        NOT NULL,
    -- Curve25519 public key, 32 bytes base64-encoded -> 44 characters.
    wireguard_public_key  TEXT        NOT NULL,
    ip                    INET        NOT NULL,
    os                    TEXT,
    client_version        TEXT,
    status                TEXT        NOT NULL DEFAULT 'offline',  -- online | offline | disabled
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at          TIMESTAMPTZ,
    CONSTRAINT peers_tenant_pubkey_unique UNIQUE (tenant_id, wireguard_public_key),
    CONSTRAINT peers_tenant_ip_unique     UNIQUE (tenant_id, ip),
    CONSTRAINT peers_status_valid         CHECK (status IN ('online', 'offline', 'disabled'))
);
CREATE INDEX peers_tenant_idx        ON peers (tenant_id);
CREATE INDEX peers_user_idx          ON peers (user_id);
CREATE INDEX peers_last_seen_idx     ON peers (last_seen_at);

-- ----------------------------------------------------------------------
-- tags + peer_tags
-- ----------------------------------------------------------------------
CREATE TABLE tags (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tags_tenant_name_unique UNIQUE (tenant_id, name)
);
CREATE INDEX tags_tenant_idx ON tags (tenant_id);

CREATE TABLE peer_tags (
    peer_id     UUID        NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    tag_id      UUID        NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (peer_id, tag_id)
);
CREATE INDEX peer_tags_tag_idx ON peer_tags (tag_id);

-- ----------------------------------------------------------------------
-- acl_policies (one policy per tenant; revision is monotonic)
-- ----------------------------------------------------------------------
CREATE TABLE acl_policies (
    tenant_id   UUID        PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    revision    BIGINT      NOT NULL DEFAULT 1,
    hcl_source  TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  UUID                 REFERENCES users(id) ON DELETE SET NULL
);

-- Append-only history for diff and rollback.
CREATE TABLE acl_policy_history (
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    revision    BIGINT      NOT NULL,
    hcl_source  TEXT        NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_by  UUID                 REFERENCES users(id) ON DELETE SET NULL,
    PRIMARY KEY (tenant_id, revision)
);

-- ----------------------------------------------------------------------
-- pre_auth_keys (for headless / CI registration)
-- ----------------------------------------------------------------------
CREATE TABLE pre_auth_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    description  TEXT,
    -- Argon2id hash of the secret. Plaintext is shown to the user once at creation.
    secret_hash  TEXT        NOT NULL,
    -- Tags auto-applied to peers registered with this key.
    tags         TEXT[]      NOT NULL DEFAULT '{}',
    reusable     BOOLEAN     NOT NULL DEFAULT false,
    ephemeral    BOOLEAN     NOT NULL DEFAULT false,
    created_by   UUID                 REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    use_count    BIGINT      NOT NULL DEFAULT 0
);
CREATE INDEX pre_auth_keys_tenant_idx  ON pre_auth_keys (tenant_id);
CREATE INDEX pre_auth_keys_expires_idx ON pre_auth_keys (expires_at) WHERE revoked_at IS NULL;

-- ----------------------------------------------------------------------
-- audit_log
-- ----------------------------------------------------------------------
CREATE TABLE audit_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- tenant_id may be null for global / cross-tenant events.
    tenant_id       UUID                 REFERENCES tenants(id) ON DELETE SET NULL,
    actor_id        UUID,                                                       -- user id; not FK so deleted users still appear
    actor_type      TEXT        NOT NULL,                                       -- user | system | api
    action          TEXT        NOT NULL,                                       -- e.g. peer.create, policy.update
    resource_type   TEXT        NOT NULL,                                       -- peer | policy | user | group | ...
    resource_id     UUID,
    diff            JSONB,                                                      -- before/after when meaningful
    ip_address      INET,
    user_agent      TEXT,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_log_actor_type_valid CHECK (actor_type IN ('user', 'system', 'api'))
);
CREATE INDEX audit_log_tenant_time_idx   ON audit_log (tenant_id, occurred_at DESC);
CREATE INDEX audit_log_actor_idx         ON audit_log (actor_id, occurred_at DESC);
CREATE INDEX audit_log_resource_idx      ON audit_log (resource_type, resource_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS pre_auth_keys;
DROP TABLE IF EXISTS acl_policy_history;
DROP TABLE IF EXISTS acl_policies;
DROP TABLE IF EXISTS peer_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS peers;
DROP TABLE IF EXISTS user_group_members;
DROP TABLE IF EXISTS user_groups;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;

-- pgcrypto is left in place (other tooling may rely on it).

-- +goose StatementEnd
