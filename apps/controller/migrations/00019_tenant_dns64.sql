-- +goose Up
-- +goose StatementBegin

-- NAT64 Phase B — per-tenant DNS64 enable flag. nat64_prefix already
-- exists (migration 00018, Phase A hook). Default false so existing
-- tenants see no behaviour change; an admin opts in once Phase C makes
-- the synthesised prefix routable.
-- See docs/design/2026-05-29-nat64-phase-b-dns64.md.
ALTER TABLE tenants ADD COLUMN dns64_enabled BOOLEAN NOT NULL DEFAULT false;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE tenants DROP COLUMN IF EXISTS dns64_enabled;

-- +goose StatementEnd
