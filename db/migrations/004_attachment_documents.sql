-- Phase 4A: associate parsed documents with their source attachment.
ALTER TABLE vector_store.documents
    ADD COLUMN IF NOT EXISTS attachment_id BIGINT;

CREATE INDEX IF NOT EXISTS documents_attachment_idx
    ON vector_store.documents(attachment_id);
