-- Normalize internal attachment download lifecycle names.
UPDATE ingestion.attachments SET download_status='not_started' WHERE download_status='not_downloaded';
UPDATE ingestion.attachments SET download_status='downloading' WHERE download_status='pending';
ALTER TABLE ingestion.attachments DROP CONSTRAINT IF EXISTS attachments_download_status_check;
ALTER TABLE ingestion.attachments ADD CONSTRAINT attachments_download_status_check
    CHECK (download_status IN ('not_started','downloading','completed','failed'));
ALTER TABLE ingestion.attachments ALTER COLUMN download_status SET DEFAULT 'not_started';
