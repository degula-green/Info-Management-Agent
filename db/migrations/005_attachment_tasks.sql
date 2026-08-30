-- Attachment download and parse tasks are shared by platform collectors.
ALTER TABLE ingestion.worker_tasks
    DROP CONSTRAINT IF EXISTS worker_tasks_task_type_check;

ALTER TABLE ingestion.worker_tasks
    ADD CONSTRAINT worker_tasks_task_type_check
    CHECK (task_type IN ('vectorization', 'collector', 'attachment_download', 'attachment_parse'));
