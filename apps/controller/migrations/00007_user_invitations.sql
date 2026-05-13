-- +goose Up
-- +goose StatementBegin

-- User invitations — admin-issued bindings of (tenant, email, role)
-- to a one-time token. Phase A here is the table + audit shape only;
-- email delivery, the redeem flow, and OIDC-callback-side "claim
-- pending invitation" logic land in follow-ups.
--
-- Status derivation lives in the renderer (no enum column):
--   - revoked_at set                 → revoked
--   - accepted_at set                → accepted
--   - expires_at past && unredeemed  → expired
--   - else                           → pending
--
-- One pending invitation per (tenant, email) is enforced by a partial
-- unique index so an admin can re-invite a previously-revoked or
-- accepted address without having to delete history first.
CREATE TABLE user_invitations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email        TEXT         NOT NULL,
    is_admin     BOOLEAN      NOT NULL DEFAULT false,
    -- token_hash holds the bcrypt hash of the invite token; the
    -- plaintext is shown once at creation (similar to pre-auth keys)
    -- and used by the redeem flow when wired.
    token_hash   TEXT         NOT NULL,
    invited_by   UUID                  REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ  NOT NULL,
    accepted_at  TIMESTAMPTZ,
    accepted_by  UUID                  REFERENCES users(id) ON DELETE SET NULL,
    revoked_at   TIMESTAMPTZ,
    revoked_by   UUID                  REFERENCES users(id) ON DELETE SET NULL
);

-- Only one active (pending) invitation per (tenant, email). Accepted /
-- revoked rows stay around for audit but don't block re-issuance.
CREATE UNIQUE INDEX user_invitations_tenant_email_pending
    ON user_invitations (tenant_id, lower(email))
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE INDEX user_invitations_tenant_idx ON user_invitations (tenant_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS user_invitations;
-- +goose StatementEnd
