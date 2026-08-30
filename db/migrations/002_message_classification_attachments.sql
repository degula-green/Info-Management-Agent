-- Phase 0: preserve platform message types and provide attachment lifecycle storage.
ALTER TABLE ingestion.messages
    ADD COLUMN IF NOT EXISTS source_message_type TEXT;

CREATE TABLE IF NOT EXISTS ingestion.attachments (
    id BIGSERIAL PRIMARY KEY,
    message_id VARCHAR NOT NULL REFERENCES ingestion.messages(id) ON DELETE CASCADE,
    raw_message_id BIGINT REFERENCES ingestion.raw_messages(id) ON DELETE SET NULL,
    source_account_id BIGINT NOT NULL REFERENCES ingestion.source_accounts(id) ON DELETE CASCADE,
    user_id BIGINT,
    platform VARCHAR(32) NOT NULL,
    external_attachment_id VARCHAR(512),
    file_name TEXT,
    extension VARCHAR(32),
    mime_type VARCHAR(255),
    file_category VARCHAR(32) NOT NULL DEFAULT 'unknown',
    file_size BIGINT,
    content_hash CHAR(64),
    storage_provider VARCHAR(32),
    storage_bucket VARCHAR(255),
    storage_key TEXT,
    download_status VARCHAR(32) NOT NULL DEFAULT 'not_downloaded',
    parse_status VARCHAR(32) NOT NULL DEFAULT 'not_required',
    preview_capability VARCHAR(32) NOT NULL DEFAULT 'pending',
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT attachments_category_check CHECK (file_category IN ('document','archive','installer','image','audio','video','unknown')),
    CONSTRAINT attachments_download_status_check CHECK (download_status IN ('not_downloaded','pending','completed','failed')),
    CONSTRAINT attachments_parse_status_check CHECK (parse_status IN ('not_required','pending','processing','completed','failed')),
    CONSTRAINT attachments_preview_check CHECK (preview_capability IN ('inline','rendered','download_only','pending')),
    CONSTRAINT attachments_external_unique UNIQUE (source_account_id, external_attachment_id)
);

CREATE INDEX IF NOT EXISTS attachments_message_idx ON ingestion.attachments(message_id);
CREATE INDEX IF NOT EXISTS attachments_user_idx ON ingestion.attachments(user_id);
CREATE INDEX IF NOT EXISTS attachments_status_idx ON ingestion.attachments(download_status, parse_status);
