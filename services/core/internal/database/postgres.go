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
	// Durable multi-turn QA history used by the remote-compatible chat flow.
	// Keep it alongside the local one-shot history table for older clients.
	_, _ = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS qa_conversations (
        id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
        title TEXT NOT NULL DEFAULT '新的对话', message_count INTEGER NOT NULL DEFAULT 0,
        last_message_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
        CREATE INDEX IF NOT EXISTS qa_conversations_user_updated_idx ON qa_conversations(user_id, updated_at DESC);
        CREATE TABLE IF NOT EXISTS qa_messages (
        id BIGSERIAL PRIMARY KEY, conversation_id BIGINT NOT NULL REFERENCES qa_conversations(id) ON DELETE CASCADE,
        user_id BIGINT NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE, question TEXT NOT NULL,
        answer TEXT NOT NULL DEFAULT '', answer_status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (answer_status IN ('pending','streaming','completed','failed')),
        error_message TEXT, scope_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb, citations JSONB NOT NULL DEFAULT '[]'::jsonb,
        retrieval_meta JSONB NOT NULL DEFAULT '{}'::jsonb, request_id TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), completed_at TIMESTAMPTZ);
        CREATE INDEX IF NOT EXISTS qa_messages_conversation_created_idx ON qa_messages(conversation_id, created_at);
        CREATE INDEX IF NOT EXISTS qa_messages_user_created_idx ON qa_messages(user_id, created_at DESC)`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.messages ADD COLUMN IF NOT EXISTS source_message_type TEXT`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.participants ADD COLUMN IF NOT EXISTS avatar_url TEXT`)
	// Normalize the legacy personal_wechat spelling at startup as well as in
	// migrations. All new collector and API writes use the canonical wechat key.
	_, _ = pool.Exec(ctx, `UPDATE ingestion.source_accounts sa SET platform='wechat',updated_at=now()
        WHERE sa.platform='personal_wechat' AND NOT EXISTS (
          SELECT 1 FROM ingestion.source_accounts existing WHERE existing.id<>sa.id
            AND existing.platform='wechat' AND existing.external_account_id=sa.external_account_id)`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.conversations SET platform='wechat' WHERE platform='personal_wechat'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.messages SET source='wechat' WHERE source='personal_wechat'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.raw_messages SET source='wechat' WHERE source='personal_wechat'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.participants SET source='wechat' WHERE source='personal_wechat'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.attachments SET platform='wechat' WHERE platform='personal_wechat'`)
	// Bindings and queued work are also platform-bearing records. Normalize
	// them here so an existing installation can be upgraded without requiring
	// the collector process to be restarted first.
	_, _ = pool.Exec(ctx, `UPDATE ingestion.collector_bindings b SET collector_type='wechat',updated_at=now()
        WHERE b.collector_type='personal_wechat' AND NOT EXISTS (
          SELECT 1 FROM ingestion.collector_bindings existing WHERE existing.id<>b.id
            AND existing.source_account_id=b.source_account_id AND existing.collector_type='wechat')`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.worker_tasks
        SET payload=jsonb_set(payload,'{platform}','"wechat"',false)
        WHERE payload->>'platform'='personal_wechat'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.worker_tasks
        SET payload=jsonb_set(payload,'{source}','"wechat"',false)
        WHERE payload->>'source'='personal_wechat'`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.participants SET display_name=NULL,updated_at=now()
        WHERE source IN ('wechat','personal_wechat') AND lower(COALESCE(display_name,''))=lower(external_participant_id)
        AND lower(COALESCE(display_name,'')) LIKE 'wxid_%'`)
	_, _ = pool.Exec(ctx, `ALTER TABLE vector_store.documents ADD COLUMN IF NOT EXISTS attachment_id BIGINT`)
	_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS documents_attachment_idx ON vector_store.documents(attachment_id)`)
	_, _ = pool.Exec(ctx, `ALTER TABLE vector_store.documents ADD COLUMN IF NOT EXISTS index_status VARCHAR(32) NOT NULL DEFAULT 'pending'`)
	_, _ = pool.Exec(ctx, `UPDATE vector_store.documents
        SET index_status = CASE
            WHEN status = 'completed' THEN 'indexed'
            WHEN status = 'skipped' THEN 'skipped'
            WHEN status = 'failed' THEN 'failed'
            ELSE COALESCE(index_status, 'pending')
        END
        WHERE index_status IS DISTINCT FROM CASE
            WHEN status = 'completed' THEN 'indexed'
            WHEN status = 'skipped' THEN 'skipped'
            WHEN status = 'failed' THEN 'failed'
            ELSE COALESCE(index_status, 'pending')
        END`)
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
        ADD CONSTRAINT collector_bindings_collector_type_check CHECK (collector_type IN ('wechat','personal_wechat','wecom','feishu'))`)
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
	_, _ = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS ingestion.ai_qa_sessions (
        id BIGSERIAL PRIMARY KEY,
        user_id BIGINT NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
        question TEXT NOT NULL,
        answer TEXT NOT NULL,
        platform VARCHAR(32) NOT NULL DEFAULT 'all',
        citations JSONB NOT NULL DEFAULT '[]'::jsonb,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
        CONSTRAINT ai_qa_sessions_platform_check CHECK (platform IN ('all','feishu','wecom','wechat','personal_wechat'))
    )`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.ai_qa_sessions DROP CONSTRAINT IF EXISTS ai_qa_sessions_platform_check`)
	_, _ = pool.Exec(ctx, `ALTER TABLE ingestion.ai_qa_sessions ADD CONSTRAINT ai_qa_sessions_platform_check CHECK (platform IN ('all','feishu','wecom','wechat','personal_wechat'))`)
	_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS ai_qa_sessions_user_created_idx ON ingestion.ai_qa_sessions(user_id, created_at DESC)`)
	_, _ = pool.Exec(ctx, `UPDATE ingestion.ai_qa_sessions SET platform='wechat',updated_at=now() WHERE platform='personal_wechat'`)
	// The local launcher does not run migration files automatically. Install the
	// same write guard as migration 017 so legacy input is accepted but stored
	// only under the canonical value.
	_, _ = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION ingestion.canonicalize_platform_value()
        RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
          IF NEW.platform='personal_wechat' THEN NEW.platform='wechat'; END IF;
          RETURN NEW;
        END $$;
        CREATE OR REPLACE FUNCTION ingestion.canonicalize_source_value()
        RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
          IF NEW.source='personal_wechat' THEN NEW.source='wechat'; END IF;
          RETURN NEW;
        END $$;
        CREATE OR REPLACE FUNCTION ingestion.canonicalize_collector_type()
        RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
          IF NEW.collector_type='personal_wechat' THEN NEW.collector_type='wechat'; END IF;
          RETURN NEW;
        END $$;
        DROP TRIGGER IF EXISTS canonical_source_accounts_platform ON ingestion.source_accounts;
        CREATE TRIGGER canonical_source_accounts_platform BEFORE INSERT OR UPDATE OF platform ON ingestion.source_accounts
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();
        DROP TRIGGER IF EXISTS canonical_collector_bindings_type ON ingestion.collector_bindings;
        CREATE TRIGGER canonical_collector_bindings_type BEFORE INSERT OR UPDATE OF collector_type ON ingestion.collector_bindings
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_collector_type();
        DROP TRIGGER IF EXISTS canonical_conversations_platform ON ingestion.conversations;
        CREATE TRIGGER canonical_conversations_platform BEFORE INSERT OR UPDATE OF platform ON ingestion.conversations
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();
        DROP TRIGGER IF EXISTS canonical_attachments_platform ON ingestion.attachments;
        CREATE TRIGGER canonical_attachments_platform BEFORE INSERT OR UPDATE OF platform ON ingestion.attachments
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();
        DROP TRIGGER IF EXISTS canonical_qa_platform ON ingestion.ai_qa_sessions;
        CREATE TRIGGER canonical_qa_platform BEFORE INSERT OR UPDATE OF platform ON ingestion.ai_qa_sessions
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();
        DROP TRIGGER IF EXISTS canonical_messages_source ON ingestion.messages;
        CREATE TRIGGER canonical_messages_source BEFORE INSERT OR UPDATE OF source ON ingestion.messages
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_source_value();
        DROP TRIGGER IF EXISTS canonical_raw_messages_source ON ingestion.raw_messages;
        CREATE TRIGGER canonical_raw_messages_source BEFORE INSERT OR UPDATE OF source ON ingestion.raw_messages
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_source_value();
        DROP TRIGGER IF EXISTS canonical_participants_source ON ingestion.participants;
        CREATE TRIGGER canonical_participants_source BEFORE INSERT OR UPDATE OF source ON ingestion.participants
          FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_source_value()`)
	return pool, nil
}
