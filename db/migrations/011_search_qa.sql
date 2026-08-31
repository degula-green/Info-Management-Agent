-- Unified permission-scoped retrieval and durable AI answer history.
CREATE TABLE IF NOT EXISTS ingestion.ai_qa_sessions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    question TEXT NOT NULL,
    answer TEXT NOT NULL,
    platform VARCHAR(32) NOT NULL DEFAULT 'all',
    citations JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ai_qa_sessions_platform_check CHECK (platform IN ('all', 'feishu', 'wecom', 'wechat', 'personal_wechat'))
);

CREATE INDEX IF NOT EXISTS ai_qa_sessions_user_created_idx
    ON ingestion.ai_qa_sessions(user_id, created_at DESC);
