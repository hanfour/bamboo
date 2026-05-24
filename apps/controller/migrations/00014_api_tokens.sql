-- +goose Up
-- +goose StatementBegin

-- API tokens (§4 P2 commercial / SaaS integration).
--
-- Webhooks v1 (#180/#181) is the outbound integration story —
-- bamboo notifies external systems of audited events. API tokens
-- are the inbound dual: they let an external system (CI runner,
-- Terraform provider, ops script) authenticate as an admin
-- without an interactive OIDC dance.
--
-- v1 scope: every token is full tenant-admin. Fine-grained scopes
-- (e.g. "peers:read", "policy:write") are a v2 follow-up — they
-- want their own ADR on whether scopes are an enum, a JSON array,
-- or a separate table.
--
-- Token format: bat_<base32(id)>_<base32(random)> — mirrors the
-- preauth-key + invitation-token shape (see internal/auth/preauthkey.go).
-- The embedded id gives the authn middleware an O(1) row lookup;
-- the random suffix is the actual entropy. The DB stores only a
-- bcrypt hash, so a leaked snapshot doesn't yield usable tokens.
--
-- Why no name uniqueness constraint: a tenant minting two CI
-- tokens both called "github-actions" should not be a hard error.
-- The description column carries operator intent; uniqueness is
-- on the (id, secret) shape which the controller generates.
--
-- Cascade-on-tenant-delete is intentional: a tenant being dropped
-- should not leave dangling admin tokens. Note the pre-auth-keys
-- + webhook_subscriptions tables share the same CASCADE policy.

CREATE TABLE api_tokens (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Operator-facing label so the list page shows "github-actions"
    -- rather than a raw UUID. Free-text; the only validation is
    -- non-empty + length cap at the API edge.
    name          TEXT        NOT NULL,
    -- Optional free-text note (e.g. "CI runner for repo X").
    description   TEXT,
    -- bcrypt hash of the full plaintext (including bat_ prefix).
    -- The plaintext is shown to the minter once and never again.
    secret_hash   TEXT        NOT NULL,
    -- Optional self-expiry. NULL = never expires (the rotate path
    -- is "delete + re-mint" — same convention as webhooks).
    expires_at    TIMESTAMPTZ,
    -- Set on POST /api/v1/api-tokens/{id}/revoke. The authn
    -- middleware checks both expires_at + revoked_at; the row
    -- stays so the audit trail is preserved.
    revoked_at    TIMESTAMPTZ,
    -- Bumped on every successful authn so the list page can
    -- show "last used 3 hours ago" — operator wants to know which
    -- tokens are still actually in use before rotating.
    last_used_at  TIMESTAMPTZ,
    created_by    UUID                 REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX api_tokens_tenant_idx ON api_tokens (tenant_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS api_tokens;
-- +goose StatementEnd
