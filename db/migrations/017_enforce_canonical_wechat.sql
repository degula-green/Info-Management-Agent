-- Canonical platform write contract:
--   wechat = personal WeChat (current value)
--   personal_wechat = legacy migration/read compatibility only
--   wecom = Enterprise WeChat

UPDATE ingestion.source_accounts sa
SET platform = 'wechat', updated_at = now()
WHERE sa.platform = 'personal_wechat'
  AND NOT EXISTS (
    SELECT 1 FROM ingestion.source_accounts existing
    WHERE existing.id <> sa.id
      AND existing.platform = 'wechat'
      AND existing.external_account_id = sa.external_account_id
  );

UPDATE ingestion.collector_bindings b
SET collector_type = 'wechat', updated_at = now()
WHERE b.collector_type = 'personal_wechat'
  AND NOT EXISTS (
    SELECT 1 FROM ingestion.collector_bindings existing
    WHERE existing.id <> b.id
      AND existing.source_account_id = b.source_account_id
      AND existing.collector_type = 'wechat'
  );

UPDATE ingestion.conversations SET platform = 'wechat' WHERE platform = 'personal_wechat';
UPDATE ingestion.messages SET source = 'wechat' WHERE source = 'personal_wechat';
UPDATE ingestion.raw_messages SET source = 'wechat' WHERE source = 'personal_wechat';
UPDATE ingestion.participants SET source = 'wechat' WHERE source = 'personal_wechat';
UPDATE ingestion.attachments SET platform = 'wechat' WHERE platform = 'personal_wechat';
UPDATE ingestion.ai_qa_sessions SET platform = 'wechat' WHERE platform = 'personal_wechat';

UPDATE ingestion.worker_tasks
SET payload = jsonb_set(payload, '{platform}', '"wechat"', false)
WHERE payload->>'platform' = 'personal_wechat';

UPDATE ingestion.worker_tasks
SET payload = jsonb_set(payload, '{source}', '"wechat"', false)
WHERE payload->>'source' = 'personal_wechat';

DO $$
BEGIN
  IF to_regclass('vector_store.processing_runs') IS NOT NULL THEN
    UPDATE vector_store.processing_runs SET source = 'wechat' WHERE source = 'personal_wechat';
  END IF;
END $$;

CREATE OR REPLACE FUNCTION ingestion.canonicalize_platform_value()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.platform = 'personal_wechat' THEN NEW.platform = 'wechat'; END IF;
  RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION ingestion.canonicalize_source_value()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.source = 'personal_wechat' THEN NEW.source = 'wechat'; END IF;
  RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION ingestion.canonicalize_collector_type()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.collector_type = 'personal_wechat' THEN NEW.collector_type = 'wechat'; END IF;
  RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS canonical_source_accounts_platform ON ingestion.source_accounts;
CREATE TRIGGER canonical_source_accounts_platform
BEFORE INSERT OR UPDATE OF platform ON ingestion.source_accounts
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();

DROP TRIGGER IF EXISTS canonical_collector_bindings_type ON ingestion.collector_bindings;
CREATE TRIGGER canonical_collector_bindings_type
BEFORE INSERT OR UPDATE OF collector_type ON ingestion.collector_bindings
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_collector_type();

DROP TRIGGER IF EXISTS canonical_conversations_platform ON ingestion.conversations;
CREATE TRIGGER canonical_conversations_platform
BEFORE INSERT OR UPDATE OF platform ON ingestion.conversations
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();

DROP TRIGGER IF EXISTS canonical_attachments_platform ON ingestion.attachments;
CREATE TRIGGER canonical_attachments_platform
BEFORE INSERT OR UPDATE OF platform ON ingestion.attachments
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();

DROP TRIGGER IF EXISTS canonical_qa_platform ON ingestion.ai_qa_sessions;
CREATE TRIGGER canonical_qa_platform
BEFORE INSERT OR UPDATE OF platform ON ingestion.ai_qa_sessions
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_platform_value();

DROP TRIGGER IF EXISTS canonical_messages_source ON ingestion.messages;
CREATE TRIGGER canonical_messages_source
BEFORE INSERT OR UPDATE OF source ON ingestion.messages
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_source_value();

DROP TRIGGER IF EXISTS canonical_raw_messages_source ON ingestion.raw_messages;
CREATE TRIGGER canonical_raw_messages_source
BEFORE INSERT OR UPDATE OF source ON ingestion.raw_messages
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_source_value();

DROP TRIGGER IF EXISTS canonical_participants_source ON ingestion.participants;
CREATE TRIGGER canonical_participants_source
BEFORE INSERT OR UPDATE OF source ON ingestion.participants
FOR EACH ROW EXECUTE FUNCTION ingestion.canonicalize_source_value();
