-- +goose Up
-- +goose StatementBegin

-- ADR 0014. Two NOLOGIN roles:
--   bamboo_app         subject to RLS (the pool assumes this role)
--   bamboo_maintenance BYPASSRLS for bootstrap lookups and cross-tenant jobs
--
-- BYPASSRLS is not inherited, and bamboo_app is NOINHERIT, so membership
-- in bamboo_maintenance only allows SET ROLE. It does not turn the app
-- role into a bypass. Superusers ignore RLS even with FORCE, so the
-- pool's AfterConnect issues SET ROLE bamboo_app on every connection.
--
-- CREATE ROLE ... BYPASSRLS requires a superuser. The migration role in
-- local compose and CI is one.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bamboo_app') THEN
        CREATE ROLE bamboo_app NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bamboo_maintenance') THEN
        CREATE ROLE bamboo_maintenance NOLOGIN BYPASSRLS NOINHERIT;
    END IF;
END $$;

GRANT bamboo_app TO CURRENT_USER;
GRANT bamboo_maintenance TO CURRENT_USER;
GRANT bamboo_maintenance TO bamboo_app;

GRANT USAGE ON SCHEMA public TO bamboo_app, bamboo_maintenance;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO bamboo_app, bamboo_maintenance;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO bamboo_app, bamboo_maintenance;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO bamboo_app, bamboo_maintenance;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO bamboo_app, bamboo_maintenance;

-- Tables whose tenant_id column is the isolation key.
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'users',
        'user_groups',
        'peers',
        'tags',
        'acl_policies',
        'acl_policy_history',
        'pre_auth_keys',
        'audit_log',
        'user_invitations',
        'webhook_subscriptions',
        'api_tokens',
        'tenant_dns_config'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I
                USING (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::uuid)
                WITH CHECK (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::uuid)',
            t
        );
    END LOOP;
END $$;

-- Join tables have no tenant_id. Confine them through the parent row,
-- which is itself RLS-scoped by the same GUC.
ALTER TABLE peer_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE peer_tags FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON peer_tags;
CREATE POLICY tenant_isolation ON peer_tags
    USING (
        EXISTS (
            SELECT 1 FROM peers
            WHERE peers.id = peer_tags.peer_id
              AND peers.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM peers
            WHERE peers.id = peer_tags.peer_id
              AND peers.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
        )
    );

ALTER TABLE user_group_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_group_members FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON user_group_members;
CREATE POLICY tenant_isolation ON user_group_members
    USING (
        EXISTS (
            SELECT 1 FROM user_groups
            WHERE user_groups.id = user_group_members.group_id
              AND user_groups.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM user_groups
            WHERE user_groups.id = user_group_members.group_id
              AND user_groups.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
        )
    );

-- tenants, relay_servers, and revoked_sessions stay global.
-- revoked_sessions is keyed by JWT jti; hiding it behind a tenant GUC
-- would make a revoked token look valid.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP POLICY IF EXISTS tenant_isolation ON user_group_members;
ALTER TABLE user_group_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE user_group_members DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON peer_tags;
ALTER TABLE peer_tags NO FORCE ROW LEVEL SECURITY;
ALTER TABLE peer_tags DISABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'users',
        'user_groups',
        'peers',
        'tags',
        'acl_policies',
        'acl_policy_history',
        'pre_auth_keys',
        'audit_log',
        'user_invitations',
        'webhook_subscriptions',
        'api_tokens',
        'tenant_dns_config'
    ]
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END $$;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM bamboo_app, bamboo_maintenance;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM bamboo_app, bamboo_maintenance;
REVOKE USAGE ON SCHEMA public FROM bamboo_app, bamboo_maintenance;
REVOKE bamboo_maintenance FROM bamboo_app;
REVOKE bamboo_app FROM CURRENT_USER;
REVOKE bamboo_maintenance FROM CURRENT_USER;
DROP ROLE IF EXISTS bamboo_maintenance;
DROP ROLE IF EXISTS bamboo_app;

-- +goose StatementEnd
