-- +goose Up
-- +goose StatementBegin

-- Per-tenant DNS configuration. One row per tenant; defaults are
-- applied on read when no row exists so the admin UI can render a
-- coherent settings page before anyone has touched DNS for that
-- tenant.
--
-- The columns here cover the Tailscale-equivalent surfaces in
-- /admin/dns: MagicDNS toggle, global nameservers + a search-domain
-- list, plus an override flag that determines whether the tenant's
-- nameservers REPLACE the per-device DNS (override=true) or are
-- merged with them (override=false). magic_dns_enabled drives
-- whether peers can resolve each other by short hostname under the
-- tenant's tailnet name.
CREATE TABLE tenant_dns_config (
    tenant_id            UUID         PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    magic_dns_enabled    BOOLEAN      NOT NULL DEFAULT true,
    global_nameservers   TEXT[]       NOT NULL DEFAULT '{}',
    search_domains       TEXT[]       NOT NULL DEFAULT '{}',
    override_dns_servers BOOLEAN      NOT NULL DEFAULT false,
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_by           UUID                  REFERENCES users(id) ON DELETE SET NULL
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tenant_dns_config;
-- +goose StatementEnd
