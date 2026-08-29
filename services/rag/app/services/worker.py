from __future__ import annotations

import threading
import time
import uuid

from app.config import settings
from app.services.pgvector_store import PgVectorStore
from app.services.vectorization import EmbeddingClient, message_chunks


class VectorizationWorker:
    def __init__(self, store: PgVectorStore, embedder: EmbeddingClient):
        self.store, self.embedder = store, embedder
        self.worker_id = f"rag-{uuid.uuid4().hex[:8]}"
        self.stop_event = threading.Event()
        self.thread: threading.Thread | None = None

    def start(self) -> None:
        if settings.worker_enabled and self.thread is None:
            self.thread = threading.Thread(target=self.run, name="vectorization-worker", daemon=True)
            self.thread.start()

    def stop(self) -> None:
        self.stop_event.set()
        if self.thread: self.thread.join(timeout=10)
        self._heartbeat("stopped")

    def run(self) -> None:
        self._heartbeat("running")
        while not self.stop_event.is_set():
            try:
                did_work = self.process_batch()
                self._heartbeat("running")
            except Exception:
                self._heartbeat("error")
                did_work = False
            if not did_work:
                self.stop_event.wait(settings.worker_poll_interval)

    def process_batch(self) -> bool:
        with self.store.connect() as conn:
            conn.execute("""UPDATE ingestion.worker_tasks SET status='pending', locked_by=NULL,
                locked_until=NULL, updated_at=now() WHERE task_type='vectorization'
                AND status='processing' AND locked_until IS NOT NULL AND locked_until < now()""")
            rows = conn.execute("""SELECT id, entity_id FROM ingestion.worker_tasks
                WHERE task_type='vectorization' AND status='pending' AND next_run_at<=now()
                ORDER BY id LIMIT %s FOR UPDATE SKIP LOCKED""", (settings.worker_batch_size,)).fetchall()
            ids = [int(row[0]) for row in rows]
            for task_id, _ in rows:
                conn.execute("UPDATE ingestion.worker_tasks SET status='processing', attempts=attempts+1, locked_by=%s, locked_until=now()+interval '10 minutes', updated_at=now() WHERE id=%s", (self.worker_id, task_id))
        for task_id, entity_id in rows:
            try:
                message = self.store.get_message(str(entity_id))
                if message is None:
                    raise RuntimeError("message not found or already processed")
                resolved = self.store.resolve_source(message)
                if not resolved: raise RuntimeError("source account or raw message not found")
                account_id, raw_id = resolved
                text = " ".join(str(message.get("text") or "").split())
                document_id = self.store.upsert_document(message, account_id, raw_id, "processing", text)
                if not text: self.store.mark_skipped(document_id)
                else:
                    chunks = message_chunks({**message, "text": text})
                    self.store.replace_chunks(document_id, str(message.get("id", "")), chunks, self.embedder.embed([c["content"] for c in chunks]))
                self._finish(int(task_id), "completed", None)
            except Exception as exc:
                self._fail(int(task_id), str(exc))
        return bool(ids)

    def _heartbeat(self, status: str) -> None:
        try:
            with self.store.connect() as conn:
                conn.execute("""INSERT INTO ingestion.worker_runs(name,status,last_heartbeat,updated_at)
                    VALUES('rag-vectorization',%s,now(),now())
                    ON CONFLICT(name) DO UPDATE SET status=EXCLUDED.status,last_heartbeat=now(),updated_at=now()""", (status,))
        except Exception:
            pass

    def _finish(self, task_id: int, status: str, error: str | None) -> None:
        with self.store.connect() as conn:
            conn.execute("UPDATE ingestion.worker_tasks SET status=%s, locked_by=NULL, locked_until=NULL, last_error=%s, completed_at=now(), updated_at=now() WHERE id=%s", (status, error, task_id))

    def _fail(self, task_id: int, error: str) -> None:
        with self.store.connect() as conn:
            conn.execute("""UPDATE ingestion.worker_tasks SET status=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'failed' END,
                next_run_at=now() + (LEAST(3600, 60 * (2 ^ LEAST(attempts-1, 6))) * interval '1 second'),
                locked_by=NULL, locked_until=NULL, last_error=%s, updated_at=now() WHERE id=%s""", (error[:4000], task_id))
