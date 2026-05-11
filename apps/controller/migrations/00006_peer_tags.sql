-- +goose Up
-- +goose StatementBegin

-- peer_tags is the many-to-many join holding user-defined tags per
-- peer. PR-3 of the node-detail-drawer series finally wires this
-- up; the apiPeerJSON.Tags slice has been hardcoded to []string{}
-- since the Phase-1 web work (see comment "populated once
-- peer_tags wiring lands").
--
-- Why a separate table rather than a TEXT[] column on peers:
--   - tags double as policy-source selectors (ACL HCL DSL: `tag:db`)
--     and the policy evaluator does set-intersection lookups by
--     tag. A normalized table lets us index on tag for the
--     evaluator's "list peers matching tag X" path without
--     scanning every peer row.
--   - tag values are short and reused across peers; a TEXT[]
--     stores N copies of each tag string per N peers, the join
--     table stores each tag value once per peer (and lets us
--     normalize further later if we ever want a dim_tags table).
--
-- ON DELETE CASCADE so deleting a peer doesn't leave orphan rows.
-- The peer mutation handlers landing in this PR rely on it.
CREATE TABLE peer_tags (
    peer_id UUID NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    tag     TEXT NOT NULL,
    PRIMARY KEY (peer_id, tag)
);

-- Index for the "list peers matching tag X" path used by the
-- policy evaluator when resolving `tag:foo` source selectors.
-- The PK already covers (peer_id, tag) lookups in the other
-- direction.
CREATE INDEX peer_tags_tag_idx ON peer_tags (tag);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE peer_tags;

-- +goose StatementEnd
