package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("CORE_DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 15 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	// Keep the existing users table compatible with authentication without adding tables.
	_, _ = pool.Exec(ctx, `ALTER TABLE identity.users ADD COLUMN IF NOT EXISTS username TEXT UNIQUE,
        ADD COLUMN IF NOT EXISTS nickname TEXT,
        ADD COLUMN IF NOT EXISTS password_hash TEXT,
        ADD COLUMN IF NOT EXISTS feishu_open_id TEXT,
        ADD COLUMN IF NOT EXISTS feishu_name TEXT,
        ADD COLUMN IF NOT EXISTS feishu_avatar TEXT,
        ADD COLUMN IF NOT EXISTS avatar_storage_key TEXT,
        ADD COLUMN IF NOT EXISTS avatar_content_type VARCHAR(64),
        ADD COLUMN IF NOT EXISTS avatar_updated_at TIMESTAMPTZ,
        ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`)
	_, _ = pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS users_email_unique_idx
        ON identity.users (lower(email)) WHERE email IS NOT NULL AND btrim(email) <> ''`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.messages ADD COLUMN IF NOT EXISTS source_message_type TEXT`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.participants ADD COLUMN IF NOT EXISTS avatar_url TEXT`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.participants SET display_name=NULL,updated_at=now()
        WHERE source='wechat' AND lower(COALESCE(display_name,''))=lower(external_participant_id)
        AND lower(COALESCE(display_name,'')) LIKE 'wxid_%'`)
	_, _ = pool.Exec(ctx, `ALTER TABLE vector_store.documents ADD COLUMN IF NOT EXISTS attachment_id BIGINT`)
	_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS documents_attachment_idx ON vector_store.documents(attachment_id)`)
	_, _ = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS ingestion.attachments (
        id BIGSERIAL PRIMARY KEY, message_id VARCHAR NOT NULL REFERENCES ingestion.messages(id) ON DELETE CASCADE,
        raw_message_id BIGINT REFERENCES ingestion.raw_messages(id) ON DELETE SET NULL,
        source_account_id BIGINT NOT NULL REFERENCES ingestion.source_accounts(id) ON DELETE CASCADE,
        user_id BIGINT, platform VARCHAR(32) NOT NULL, external_attachment_id VARCHAR(512), file_name TEXT,
        extension VARCHAR(32), mime_type VARCHAR(255), file_category VARCHAR(32) NOT NULL DEFAULT 'unknown',
        file_size BIGINT, content_hash CHAR(64), storage_provider VARCHAR(32), storage_bucket VARCHAR(255), storage_key TEXT,
        download_status VARCHAR(32) NOT NULL DEFAULT 'not_started', parse_status VARCHAR(32) NOT NULL DEFAULT 'not_required',
        preview_capability VARCHAR(32) NOT NULL DEFAULT 'pending', is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
        last_error TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
        CONSTRAINT attachments_category_check CHECK (file_category IN ('document','archive','installer','image','audio','video','unknown')),
        CONSTRAINT attachments_download_status_check CHECK (download_status IN ('not_started','downloading','completed','failed')),
        CONSTRAINT attachments_parse_status_check CHECK (parse_status IN ('not_required','pending','processing','completed','failed')),
        CONSTRAINT attachments_preview_check CHECK (preview_capability IN ('inline','rendered','download_only','pending')),
        CONSTRAINT attachments_external_unique UNIQUE (source_account_id, external_attachment_id))`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.attachments SET download_status='not_started' WHERE download_status='not_downloaded'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.attachments SET download_status='downloading' WHERE download_status='pending'`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.attachments DROP CONSTRAINT IF EXISTS attachments_download_status_check`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.attachments ADD CONSTRAINT attachments_download_status_check CHECK (download_status IN ('not_started','downloading','completed','failed'))`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.worker_tasks DROP CONSTRAINT IF EXISTS worker_tasks_task_type_check`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.worker_tasks ADD CONSTRAINT worker_tasks_task_type_check
        CHECK (task_type IN ('vectorization','collector','attachment_download','attachment_parse'))`)
	// Store connector listen policy on the existing binding table. No new table
	// is required; an empty JSON array means that collection is paused until the
	// user selects conversations.
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.collector_bindings
        DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_check`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.collector_bindings
        DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_key`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.collector_bindings
        ADD CONSTRAINT collector_bindings_collector_type_check CHECK (collector_type IN ('wechat','feishu'))`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.collector_bindings
        ADD COLUMN IF NOT EXISTS listen_mode VARCHAR(16) NOT NULL DEFAULT 'whitelist',
        ADD COLUMN IF NOT EXISTS selected_conversations JSONB NOT NULL DEFAULT '[]'::jsonb,
        ADD COLUMN IF NOT EXISTS history_start_at TIMESTAMPTZ,
        ADD COLUMN IF NOT EXISTS config_updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`)
	_, _ = pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS collector_bindings_account_type_uidx
        ON ingestion.collector_bindings(source_account_id,collector_type)`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.worker_runs ADD COLUMN IF NOT EXISTS source_account_id BIGINT REFERENCES ingestion.source_accounts(id) ON DELETE CASCADE`)
	_, _ = pool.Exec(ctx, `INSERT INTO ingestion.collector_bindings(source_account_id,collector_type,db_directory,bound_at,enabled,listen_mode,selected_conversations,config_updated_at)
        SELECT sa.id,'feishu','',now(),true,'whitelist','[]'::jsonb,now() FROM ingestion.source_accounts sa
        WHERE sa.platform='feishu' AND sa.status='active'
        ON CONFLICT(source_account_id,collector_type) DO NOTHING`)
	return pool, nil
}
