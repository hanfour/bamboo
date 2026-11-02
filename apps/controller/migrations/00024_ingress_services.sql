-- +goose Up
-- +goose StatementBegin

-- Public services ingress (docs/design/2026-11-02-public-services-ingress.md).
-- Additive only: two new tables and two SECURITY DEFINER lookups.
--
-- approved_by is the approving admin's user id; NULL means pending. It is
-- not a foreign key (same as audit_log.actor_id) so deleting that admin
-- never silently un-approves a service. uuid.Nil records an approval made
-- without a user identity (dev-fallback auth, API token without creator).
CREATE TABLE IF NOT EXISTS ingress_services (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id      UUID        NOT NULL REFERENCES peers(id)   ON DELETE CASCADE,
    label        TEXT        NOT NULL,
    target_port  INTEGER     NOT NULL,
    enabled      BOOLEAN     NOT NULL DEFAULT false,
    approved_by  UUID,
    approved_at  TIMESTAMPTZ,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Global, not per tenant: the label is a public DNS name.
    CONSTRAINT ingress_services_label_unique UNIQUE (label),
    CONSTRAINT ingress_services_label_format
        CHECK (label ~ '^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$'),
    CONSTRAINT ingress_services_label_reserved
        CHECK (label NOT IN ('www', 'api', 'admin', 'relay', 'controller', 'ingress', 'mail')),
    CONSTRAINT ingress_services_port_range
        CHECK (target_port BETWEEN 1024 AND 65535)
);
CREATE INDEX IF NOT EXISTS ingress_services_tenant_idx ON ingress_services (tenant_id);
CREATE INDEX IF NOT EXISTS ingress_services_peer_idx   ON ingress_services (peer_id);

-- One current ingress service token per tenant. Issuing a new token
-- replaces jti, which revokes the previous token.
CREATE TABLE IF NOT EXISTS ingress_tokens (
    tenant_id   UUID        PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id     UUID        NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    jti         UUID        NOT NULL,
    issued_by   UUID,
    issued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

GRANT SELECT, INSERT, UPDATE, DELETE ON ingress_services, ingress_tokens
    TO bamboo_app, bamboo_maintenance;

ALTER TABLE ingress_services ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingress_services FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ingress_services;
CREATE POLICY tenant_isolation ON ingress_services
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE ingress_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingress_tokens FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ingress_tokens;
CREATE POLICY tenant_isolation ON ingress_tokens
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- RLS bootstrap lookups (ADR 0014, project-goals P0 item 2). Labels are
-- unique across tenants, so "is this label taken" and "is this name live"
-- cannot run under a tenant GUC. Each function returns one boolean for a
-- presented label and nothing else. Owned by bamboo_maintenance
-- (BYPASSRLS, NOLOGIN) rather than the superuser migration role, so the
-- bypass is no wider than that role already is.
CREATE OR REPLACE FUNCTION ingress_label_taken(p_label TEXT) RETURNS BOOLEAN
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = public, pg_temp
AS $fn$
    SELECT EXISTS (SELECT 1 FROM ingress_services WHERE label = p_label);
$fn$;

CREATE OR REPLACE FUNCTION ingress_label_live(p_label TEXT) RETURNS BOOLEAN
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = public, pg_temp
AS $fn$
    SELECT EXISTS (
        SELECT 1
          FROM ingress_services s
          JOIN peers p ON p.id = s.peer_id
         WHERE s.label = p_label
           AND s.enabled
           AND s.approved_by IS NOT NULL
           AND p.approval_status = 'approved'
    );
$fn$;

ALTER FUNCTION ingress_label_taken(TEXT) OWNER TO bamboo_maintenance;
ALTER FUNCTION ingress_label_live(TEXT)  OWNER TO bamboo_maintenance;
REVOKE ALL ON FUNCTION ingress_label_taken(TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION ingress_label_live(TEXT)  FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ingress_label_taken(TEXT) TO bamboo_app, bamboo_maintenance;
GRANT EXECUTE ON FUNCTION ingress_label_live(TEXT)  TO bamboo_app, bamboo_maintenance;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS ingress_label_live(TEXT);
DROP FUNCTION IF EXISTS ingress_label_taken(TEXT);
DROP TABLE IF EXISTS ingress_tokens;
DROP TABLE IF EXISTS ingress_services;
-- +goose StatementEnd
