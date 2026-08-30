-- Permit parsed uploaded files to use the document type introduced by the
-- attachment processing pipeline. No table or column is added.
ALTER TABLE vector_store.documents
    DROP CONSTRAINT IF EXISTS documents_type_ck;

ALTER TABLE vector_store.documents
    ADD CONSTRAINT documents_type_ck
    CHECK (document_type IN ('message', 'attachment', 'attachment_text', 'manual'));
