-- Align platform identifiers with the retrieval contract:
-- feishu = Feishu, wecom = Enterprise WeChat, wechat = Personal WeChat.
-- personal_wechat is kept only as a legacy compatibility value.
ALTER TABLE ingestion.collector_bindings
    DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_check;

ALTER TABLE ingestion.collector_bindings
    ADD CONSTRAINT collector_bindings_collector_type_check
    CHECK (collector_type IN ('wechat', 'personal_wechat', 'wecom', 'feishu'));

UPDATE ingestion.participants
SET display_name = NULL,
    updated_at = now()
WHERE source IN ('wechat', 'personal_wechat')
  AND lower(COALESCE(display_name, '')) = lower(external_participant_id)
  AND lower(COALESCE(display_name, '')) LIKE 'wxid_%';
