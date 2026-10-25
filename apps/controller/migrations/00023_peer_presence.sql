-- +goose Up
-- +goose StatementBegin

-- Daily online seconds per peer, for the overview activity chart.
-- Each heartbeat adds the time since the previous accounted tick (capped
-- in the repo layer) to the UTC day bucket it lands in. Additive only:
-- one nullable column and one new table.
ALTER TABLE peers ADD COLUMN IF NOT EXISTS presence_tick_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS peer_presence_daily (
    peer_id   UUID        NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
    tenant_id UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    day       DATE        NOT NULL,
    seconds   INTEGER     NOT NULL DEFAULT 0 CHECK (seconds >= 0),
    PRIMARY KEY (peer_id, day)
);
CREATE INDEX IF NOT EXISTS peer_presence_daily_tenant_day_idx
    ON peer_presence_daily (tenant_id, day);

GRANT SELECT, INSERT, UPDATE, DELETE ON peer_presence_daily TO bamboo_app, bamboo_maintenance;

ALTER TABLE peer_presence_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE peer_presence_daily FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON peer_presence_daily;
CREATE POLICY tenant_isolation ON peer_presence_daily
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS peer_presence_daily;
ALTER TABLE peers DROP COLUMN IF EXISTS presence_tick_at;
-- +goose StatementEnd
