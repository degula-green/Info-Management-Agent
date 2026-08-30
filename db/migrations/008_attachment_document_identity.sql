-- Keep message and attachment documents independently idempotent.
-- A message can contain multiple attachments, so the former broad key cannot
-- identify attachment documents safely.
DO $$
DECLARE
    constraint_name TEXT;
BEGIN
    SELECT c.conname INTO constraint_name
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
      JOIN pg_namespace n ON n.oid = t.relnamespace
     WHERE n.nspname = 'vector_store'
       AND t.relname = 'documents'
       AND c.contype = 'u'
       AND pg_get_constraintdef(c.oid) LIKE 'UNIQUE (raw_message_id, document_type, processor_version)%'
     LIMIT 1;
    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE vector_store.documents DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;

DO $$
DECLARE
    index_name TEXT;
BEGIN
    FOR index_name IN
        SELECT i.relname
          FROM pg_index x
          JOIN pg_class i ON i.oid=x.indexrelid
          JOIN pg_class t ON t.oid=x.indrelid
          JOIN pg_namespace n ON n.oid=t.relnamespace
         WHERE n.nspname='vector_store' AND t.relname='documents' AND x.indisunique
           AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid=x.indexrelid)
           AND pg_get_indexdef(x.indexrelid) LIKE '%(raw_message_id, document_type, processor_version)%'
    LOOP
        EXECUTE format('DROP INDEX IF EXISTS vector_store.%I', index_name);
    END LOOP;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS documents_message_identity_uq
    ON vector_store.documents(raw_message_id, document_type, processor_version)
    WHERE attachment_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS documents_attachment_identity_uq
    ON vector_store.documents(attachment_id, document_type, processor_version)
    WHERE attachment_id IS NOT NULL;
