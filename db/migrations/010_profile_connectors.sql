-- Personal profile avatars and account-scoped connector workers.
ALTER TABLE identity.users
    ADD COLUMN IF NOT EXISTS avatar_storage_key TEXT,
    ADD COLUMN IF NOT EXISTS avatar_content_type VARCHAR(64),
    ADD COLUMN IF NOT EXISTS avatar_updated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE ingestion.collector_bindings
    DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_key;

CREATE UNIQUE INDEX IF NOT EXISTS collector_bindings_account_type_uidx
    ON ingestion.collector_bindings(source_account_id, collector_type);

CREATE INDEX IF NOT EXISTS source_accounts_owner_platform_status_idx
    ON ingestion.source_accounts(internal_account_id, platform, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS collector_bindings_account_enabled_idx
    ON ingestion.collector_bindings(source_account_id, enabled);

ALTER TABLE ingestion.worker_runs
    ADD COLUMN IF NOT EXISTS source_account_id BIGINT REFERENCES ingestion.source_accounts(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS worker_runs_source_account_idx
    ON ingestion.worker_runs(source_account_id, last_heartbeat DESC);
