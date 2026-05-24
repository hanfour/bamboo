-- +goose Up
-- +goose StatementBegin

-- audit_log immutability (§4 P2 compliance).
--
-- Webhooks v1 (#180 / #181) made the audit log a load-bearing
-- source of truth: every webhook delivery is fan-out from an
-- audit row, and the at-most-once delivery model relies on the
-- audit row staying readable for reconstruction. The audit_log
-- table has been append-only by convention since 00001, but
-- "by convention" doesn't survive a stolen DB credential or a
-- well-meaning operator running ad-hoc SQL.
--
-- This migration adds BEFORE triggers that enforce the
-- append-only contract at the storage layer:
--
--   * UPDATE — always blocked. An audit row that says "alice
--     approved peer X at 2026-05-18T10:49:10Z" must never silently
--     become "bob approved peer X at 2026-05-18T10:49:10Z". No
--     legitimate use case in the controller; tampering is the only
--     remaining caller.
--
--   * DELETE — blocked by default, but allowed when the calling
--     transaction has explicitly set the session variable
--     `bamboo.allow_audit_delete = 'true'`. Future use cases
--     (GDPR right-to-erasure flow, time-based retention purge)
--     will opt in via `SET LOCAL` so the bypass scopes to the
--     transaction and disappears on commit/rollback. Casual
--     `DELETE FROM audit_log` from a normal psql session still
--     fails loud.
--
-- Why session-var rather than a separate Postgres role:
--   * Portability: works with the existing `bamboo` role + the
--     stock controller connection pool. Adding a second role
--     would force every operator (managed PG, self-hosted,
--     CI) to provision + secret-manage it.
--   * Auditable: the few places we add `SET LOCAL` are easy to
--     grep for and review. A role-based bypass would be opaque
--     once a connection is open.
--   * Mockable in tests: a unit test exercising the bypass
--     just runs SET LOCAL in the same transaction.
--
-- Tenant deletion is unaffected: audit_log.tenant_id is
-- ON DELETE SET NULL (since 00001), so dropping a tenant flips
-- its audit rows to tenant_id=NULL rather than CASCADE-deleting
-- them. The trigger never sees those UPDATEs because SET NULL
-- runs as an internal referential action, not as a SQL UPDATE
-- statement against audit_log.

CREATE OR REPLACE FUNCTION audit_log_block_update() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (UPDATE blocked by trigger)'
        USING ERRCODE = 'insufficient_privilege',
              HINT    = 'audit rows are immutable once written; create a new event row instead';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION audit_log_block_delete() RETURNS trigger AS $$
BEGIN
    -- current_setting(name, missing_ok=true) returns NULL when
    -- the GUC was never set on this session. NULL != 'true', so
    -- the default-deny path catches every connection that didn't
    -- explicitly opt in.
    IF current_setting('bamboo.allow_audit_delete', true) = 'true' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only (DELETE blocked by trigger)'
        USING ERRCODE = 'insufficient_privilege',
              HINT    = 'set bamboo.allow_audit_delete = ''true'' in the transaction to bypass (e.g. retention purge); ad-hoc DELETE is rejected';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_block_update_trg
    BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_block_update();

CREATE TRIGGER audit_log_block_delete_trg
    BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_block_delete();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS audit_log_block_delete_trg ON audit_log;
DROP TRIGGER IF EXISTS audit_log_block_update_trg ON audit_log;
DROP FUNCTION IF EXISTS audit_log_block_delete();
DROP FUNCTION IF EXISTS audit_log_block_update();
-- +goose StatementEnd
