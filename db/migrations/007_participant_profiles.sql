-- Store presentation data separately from immutable platform participant IDs.
ALTER TABLE ingestion.participants
    ADD COLUMN IF NOT EXISTS avatar_url TEXT;

-- Earlier WeChat ingestion used wxid values as display names. Preserve the
-- external ID while leaving the display field available for a real nickname.
UPDATE ingestion.participants
SET display_name = NULL,
    updated_at = now()
WHERE source = 'wechat'
  AND lower(COALESCE(display_name, '')) = lower(external_participant_id)
  AND lower(COALESCE(display_name, '')) LIKE 'wxid_%';
