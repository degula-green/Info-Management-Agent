-- Canonical platform contract:
-- feishu = Feishu
-- wecom = Enterprise WeChat
-- wechat = Personal WeChat
-- personal_wechat = legacy compatibility value only

ALTER TABLE ingestion.collector_bindings
    DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_check;

ALTER TABLE ingestion.collector_bindings
    ADD CONSTRAINT collector_bindings_collector_type_check
    CHECK (collector_type IN ('feishu', 'wecom', 'wechat', 'personal_wechat'));

ALTER TABLE ingestion.ai_qa_sessions
    DROP CONSTRAINT IF EXISTS ai_qa_sessions_platform_check;

ALTER TABLE ingestion.ai_qa_sessions
    ADD CONSTRAINT ai_qa_sessions_platform_check
    CHECK (platform IN ('all', 'feishu', 'wecom', 'wechat', 'personal_wechat'));

-- New code writes personal WeChat as wechat. These best-effort updates convert
-- legacy values to wechat when doing so is safe. Rows blocked by unique
-- constraints stay readable through compatibility logic.
UPDATE ingestion.source_accounts sa
SET platform='wechat',
    updated_at=now()
WHERE sa.platform='personal_wechat'
  AND NOT EXISTS (
      SELECT 1
      FROM ingestion.source_accounts existing
      WHERE existing.id<>sa.id
        AND existing.platform='wechat'
        AND existing.external_account_id=sa.external_account_id
  );

UPDATE ingestion.collector_bindings b
SET collector_type='wechat',
    updated_at=now()
WHERE b.collector_type='personal_wechat'
  AND NOT EXISTS (
      SELECT 1
      FROM ingestion.collector_bindings existing
      WHERE existing.id<>b.id
        AND existing.source_account_id=b.source_account_id
        AND existing.collector_type='wechat'
  );

UPDATE ingestion.conversations SET platform='wechat' WHERE platform='personal_wechat';
UPDATE ingestion.messages SET source='wechat' WHERE source='personal_wechat';
UPDATE ingestion.raw_messages SET source='wechat' WHERE source='personal_wechat';
UPDATE ingestion.participants SET source='wechat' WHERE source='personal_wechat';
UPDATE ingestion.attachments SET platform='wechat' WHERE platform='personal_wechat';
UPDATE ingestion.ai_qa_sessions SET platform='wechat' WHERE platform='personal_wechat';

UPDATE ingestion.worker_tasks
SET payload = jsonb_set(payload, '{platform}', '"wechat"', false)
WHERE payload->>'platform' = 'personal_wechat';

UPDATE ingestion.worker_tasks
SET payload = jsonb_set(payload, '{source}', '"wechat"', false)
WHERE payload->>'source' = 'personal_wechat';

DO $$
BEGIN
    IF to_regclass('vector_store.processing_runs') IS NOT NULL THEN
        UPDATE vector_store.processing_runs SET source='wechat' WHERE source='personal_wechat';
    END IF;
END $$;
