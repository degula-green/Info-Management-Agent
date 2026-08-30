from __future__ import annotations

import threading
import uuid

from app.config import settings
from app.services.document_parser import AttachmentDocumentParser
from app.services.index_service import delete_document_chunks
from app.services.pgvector_store import PgVectorStore


class AttachmentParseWorker:
    """Consumes uploaded attachments in a process separate from text embeddings."""

    def __init__(self, store: PgVectorStore, parser: AttachmentDocumentParser):
        self.store, self.parser = store, parser
        self.worker_id = f"rag-attachment-parse-{uuid.uuid4().hex[:8]}"
        self.stop_event = threading.Event()
        self.thread: threading.Thread | None = None

    def start(self) -> None:
        if settings.attachment_parse_worker_enabled and self.thread is None:
            self.thread = threading.Thread(target=self.run, name="attachment-parse-worker", daemon=True)
            self.thread.start()

    def stop(self) -> None:
        self.stop_event.set()
        if self.thread:
            self.thread.join(timeout=10)
        self._heartbeat("stopped")

    def run(self) -> None:
        self._heartbeat("running")
        while not self.stop_event.is_set():
            try:
                worked = self.process_batch()
                self._heartbeat("running")
            except Exception:
                self._heartbeat("error")
                worked = False
            if not worked:
                self.stop_event.wait(settings.attachment_parse_poll_interval)

    def process_batch(self) -> bool:
        with self.store.connect() as conn:
            conn.execute("""UPDATE ingestion.worker_tasks SET status='pending',locked_by=NULL,locked_until=NULL,updated_at=now()
                WHERE task_type='attachment_parse' AND status='processing' AND locked_until < now()""")
            rows = conn.execute("""WITH picked AS (
                    SELECT t.id,t.payload->>'attachment_id' attachment_id FROM ingestion.worker_tasks t
                    JOIN ingestion.attachments a ON a.id=(t.payload->>'attachment_id')::bigint
                    WHERE t.task_type='attachment_parse' AND t.status IN ('pending','failed') AND t.next_run_at<=now()
                      AND a.download_status='completed' AND a.parse_status='pending' AND a.storage_key IS NOT NULL
                    ORDER BY t.id LIMIT %s FOR UPDATE SKIP LOCKED)
                UPDATE ingestion.worker_tasks t SET status='processing',attempts=attempts+1,locked_by=%s,
                    locked_until=now()+interval '30 minutes',updated_at=now() FROM picked
                WHERE t.id=picked.id RETURNING t.id,picked.attachment_id""",
                (settings.attachment_parse_batch_size, self.worker_id)).fetchall()
        for task_id, raw_attachment_id in rows:
            attachment_id, document_id = int(raw_attachment_id), None
            try:
                context = self.store.get_attachment_context(attachment_id)
                if context is None:
                    raise RuntimeError("attachment not found")
                self.store.begin_attachment_parse(attachment_id)
                if self.store.current_attachment_document(context) is not None:
                    self.store.skip_attachment_parse(attachment_id, int(task_id))
                    continue
                stale_ids = self.store.stale_attachment_document_ids(attachment_id)
                for stale_id in stale_ids:
                    delete_document_chunks(stale_id)
                self.store.delete_documents(stale_ids)
                result = self.parser.parse(context)
                metadata = {"attachment_id": attachment_id, "file_name": context.get("file_name"),
                            "extension": context.get("extension"), "mime_type": context.get("mime_type"),
                            "source_content_hash": context.get("content_hash"), "parser": result.parser,
                            "parser_version": result.parser_version, "derived_markdown_key": result.derived_markdown_key,
                            "canonical_key": result.canonical_key, "asset_keys": result.asset_keys, "headings": result.headings}
                document_id = self.store.upsert_attachment_document(context, result.content, metadata)
                self.store.finish_attachment_parse(attachment_id, int(task_id), document_id)
            except Exception as exc:
                if document_id is not None:
                    self.store.mark_failed(document_id, str(exc))
                self.store.fail_attachment_parse(attachment_id, int(task_id), str(exc))
        return bool(rows)

    def _heartbeat(self, status: str) -> None:
        try:
            with self.store.connect() as conn:
                conn.execute("""INSERT INTO ingestion.worker_runs(name,status,last_heartbeat,updated_at)
                    VALUES('rag-attachment-parse',%s,now(),now())
                    ON CONFLICT(name) DO UPDATE SET status=EXCLUDED.status,last_heartbeat=now(),updated_at=now()""", (status,))
        except Exception:
            pass
