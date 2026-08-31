CREATE TABLE IF NOT EXISTS qa_conversations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '新的对话',
    message_count INTEGER NOT NULL DEFAULT 0,
    last_message_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS qa_conversations_user_updated_idx ON qa_conversations(user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS qa_messages (
    id BIGSERIAL PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES qa_conversations(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    question TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    answer_status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (answer_status IN ('pending','streaming','completed','failed')),
    error_message TEXT,
    scope_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    citations JSONB NOT NULL DEFAULT '[]'::jsonb,
    retrieval_meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS qa_messages_conversation_created_idx ON qa_messages(conversation_id, created_at);
CREATE INDEX IF NOT EXISTS qa_messages_user_created_idx ON qa_messages(user_id, created_at DESC);
