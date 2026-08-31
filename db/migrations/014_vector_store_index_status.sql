-- Track whether a processed document has already been written to Elasticsearch.
ALTER TABLE vector_store.documents
    ADD COLUMN IF NOT EXISTS index_status VARCHAR(32) NOT NULL DEFAULT 'pending';

UPDATE vector_store.documents
SET index_status = CASE
    WHEN status = 'completed' THEN 'indexed'
    WHEN status = 'skipped' THEN 'skipped'
    WHEN status = 'failed' THEN 'failed'
    ELSE COALESCE(index_status, 'pending')
END
WHERE index_status IS DISTINCT FROM CASE
    WHEN status = 'completed' THEN 'indexed'
    WHEN status = 'skipped' THEN 'skipped'
    WHEN status = 'failed' THEN 'failed'
    ELSE COALESCE(index_status, 'pending')
END;
