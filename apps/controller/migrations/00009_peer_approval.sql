-- +goose Up
-- +goose StatementBegin

-- Device approval queue (issue #133).
--
-- Today every successfully-redeemed pre-auth-key registration creates
-- an immediately-visible peer with online status. There is no admin
-- gate between "credential redeemed" and "peer participates in the
-- mesh". This migration adds the gate.
--
-- Model:
--   - peers.approval_status: pending / approved / rejected.
--       Distinct from peers.status (online / offline / disabled) which
--       represents data-plane liveness, not lifecycle state.
--   - peers.approved_at + approved_by_user_id record who flipped a
--       pending row into approved (or rejected). NULL until an admin
--       acts; backfilled NULL for legacy rows that this migration
--       marks 'approved' below.
--   - pre_auth_keys.auto_approve: when true, peers minted with this
--       key skip the queue and land approved. Defaults FALSE so the
--       safer manual-approval flow is the new norm; CI / kiosk use
--       cases opt in explicitly. Mint flow can also be patched to
--       force the safe default for tenants with the future
--       tenant.manual_approval_required toggle.
--
-- Backfill: every existing peer is marked 'approved' so the rollout
-- is non-breaking. New peers, when pre_auth_keys.auto_approve=false,
-- land 'pending' and stay invisible to other peers + the relay-token
-- mint endpoint until an admin approves.
--
-- Visibility enforcement happens in handlers/coordinator.go (Register
-- response peer-list filter + Approve handler publishes PeerAdded),
-- not at the DB layer — the row remains queryable for the admin
-- pending-list endpoint while being filtered out of mesh data-plane
-- responses.

ALTER TABLE peers ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (approval_status IN ('pending', 'approved', 'rejected'));
ALTER TABLE peers ADD COLUMN approved_at TIMESTAMPTZ;
ALTER TABLE peers ADD COLUMN approved_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

-- Existing rows: treat as already-approved so the upgrade is
-- non-breaking. approved_at gets created_at so audit history is
-- coherent ("approved at the same moment it was registered, by the
-- pre-#133 implicit grant"); approved_by_user_id stays NULL because
-- there is no per-row record of who would have approved it.
UPDATE peers SET approval_status = 'approved', approved_at = created_at;

-- Partial index for the admin "pending queue" query. Skews tiny —
-- the table is dominated by approved rows — so a filtered index keeps
-- the scan O(pending) regardless of how large the approved cohort is.
CREATE INDEX peers_pending_idx ON peers (tenant_id, created_at)
    WHERE approval_status = 'pending';

-- pre_auth_keys.auto_approve — opt-in skip-the-queue flag. CI / kiosk
-- workflows that mint a reusable key + register a peer should set
-- this true (the alternative is "admin watches a queue and approves
-- each CI runner", which makes the queue useless for batch use).
-- Default false so the security-first behavior is the path-of-least-
-- resistance for new keys.
ALTER TABLE pre_auth_keys ADD COLUMN auto_approve BOOLEAN NOT NULL DEFAULT false;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE pre_auth_keys DROP COLUMN IF EXISTS auto_approve;
DROP INDEX IF EXISTS peers_pending_idx;
ALTER TABLE peers DROP COLUMN IF EXISTS approved_by_user_id;
ALTER TABLE peers DROP COLUMN IF EXISTS approved_at;
ALTER TABLE peers DROP COLUMN IF EXISTS approval_status;

-- +goose StatementEnd
