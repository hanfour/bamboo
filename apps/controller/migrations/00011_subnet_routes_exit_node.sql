-- +goose Up
-- +goose StatementBegin

-- Subnet routes (issue #136) + exit nodes (issue #137).
--
-- A peer self-advertises routes it would like to expose to the rest
-- of the tailnet (`bamboo up --advertise-routes=192.168.1.0/24`) or
-- declare itself exit-node-capable (`bamboo up --advertise-exit-node`).
-- Each advertisement requires admin approval before it actually
-- propagates into the mesh's allowed_ips compile path — same shape
-- as the device-approval queue from issue #133.
--
-- Storage layout:
--   advertised_routes / approved_routes: text[] of CIDRs.
--     advertised_*: what the peer asked for.
--     approved_*  : what the admin signed off on (subset of
--                   advertised_*). The ACL compiler merges
--                   approved_routes into the other peers' allowed_ips
--                   for traffic destined at this peer.
--   exit_node_capable: peer reported the --advertise-exit-node flag.
--   exit_node_approved: admin approved the exit-node role for this
--                       peer. Distinct from capable so the admin's
--                       intent is preserved across re-registers (the
--                       client could drop the flag in a future
--                       config; the admin's approval shouldn't
--                       silently disappear).
--   using_exit_node_peer_id: peer the *requester* is currently
--                            routing default traffic through. NULL =
--                            no exit node in use. References peers
--                            in the same tenant; FK enforced.
--
-- All columns default to empty/false/NULL so existing peers are
-- unaffected. ACL enforcement (issue #132) treats empty
-- approved_routes as "no extra allowed_ips for this peer" — the
-- normal /32 reachability still applies. Treating absence as the
-- safe default keeps the rollout non-breaking.

ALTER TABLE peers ADD COLUMN advertised_routes TEXT[] NOT NULL DEFAULT ARRAY[]::text[];
ALTER TABLE peers ADD COLUMN approved_routes   TEXT[] NOT NULL DEFAULT ARRAY[]::text[];
ALTER TABLE peers ADD COLUMN exit_node_capable BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE peers ADD COLUMN exit_node_approved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE peers ADD COLUMN using_exit_node_peer_id UUID
    REFERENCES peers(id) ON DELETE SET NULL;

-- Partial index for the "admin pending approvals" dashboard query.
-- A peer with routes in advertised_routes but not yet in
-- approved_routes (or with exit-node capable but not yet approved)
-- is what the Web UI surfaces as a pending advertisement.
CREATE INDEX peers_pending_routes_idx
    ON peers (tenant_id)
    WHERE array_length(advertised_routes, 1) > 0
       OR exit_node_capable = true;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS peers_pending_routes_idx;
ALTER TABLE peers DROP COLUMN IF EXISTS using_exit_node_peer_id;
ALTER TABLE peers DROP COLUMN IF EXISTS exit_node_approved;
ALTER TABLE peers DROP COLUMN IF EXISTS exit_node_capable;
ALTER TABLE peers DROP COLUMN IF EXISTS approved_routes;
ALTER TABLE peers DROP COLUMN IF EXISTS advertised_routes;

-- +goose StatementEnd
