from __future__ import annotations

import threading
import time
import uuid
from datetime import datetime, timezone

from app.config import settings
from app.services.pgvector_store import PgVectorStore
from app.services.vectorization import EmbeddingClient, document_chunks, message_chunks
from app.services.index_service import delete_document_chunks, delete_message_chunks, index_chunks


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
            rows = conn.execute("""SELECT id, entity_id, payload FROM ingestion.worker_tasks
                WHERE task_type='vectorization' AND status='pending' AND next_run_at<=now()
                  AND COALESCE(payload->>'attachment_task','false') <> 'true'
                  AND created_at >= COALESCE(NULLIF(%s, '')::timestamptz, now())
                ORDER BY id LIMIT %s FOR UPDATE SKIP LOCKED""", (settings.rag_es_cutover_at, settings.worker_batch_size)).fetchall()
            ids = [int(row[0]) for row in rows]
            for task_id, _, _ in rows:
                conn.execute("UPDATE ingestion.worker_tasks SET status='processing', attempts=attempts+1, locked_by=%s, locked_until=now()+interval '10 minutes', updated_at=now() WHERE id=%s", (self.worker_id, task_id))
        for task_id, entity_id, payload in rows:
            document_id = None
            try:
                if (payload or {}).get("kind") == "attachment":
                    self._process_attachment(int(task_id), int((payload or {})["document_id"]))
                    continue
                message = self.store.get_message(str(entity_id))
                if message is None:
                    raise RuntimeError("message not found or already processed")
                text = " ".join(str(message.get("text") or "").split())
                account_id = message.get("source_account_id")
                raw_id = message.get("raw_message_id")
                if account_id is None or raw_id is None:
                    raise RuntimeError("message is missing internal source account or raw message id")
                document_id = self.store.upsert_document(message, int(account_id), int(raw_id), "processing", text)
                if not text: self.store.mark_skipped(document_id)
                else:
                    chunks = message_chunks({**message, "text": text})
                    vectors = self.embedder.embed([c["content"] for c in chunks])
                    self.store.replace_chunks(document_id, str(message.get("id", "")), chunks, vectors)
                    delete_message_chunks(str(message.get("id", "")))
                    index_chunks([{**chunk, "document_id": document_id,
                                   "raw_message_id": message.get("raw_message_id"),
                                   "embedding_model": settings.embedding_model,
                                   "embedding_version": settings.processor_version,
                                   "is_deleted": bool(message.get("is_deleted")),
                                   "indexed_at": datetime.now(timezone.utc),
                                   "embedding": vector}
                                  for chunk, vector in zip(chunks, vectors)])
                    self.store.mark_completed(document_id)
                self._finish(int(task_id), "completed", None)
            except Exception as exc:
                if document_id is not None:
                    self.store.mark_failed(document_id, str(exc))
                self._fail(int(task_id), str(exc))
        return bool(ids)

    def _process_attachment(self, task_id: int, document_id: int) -> None:
        document = self.store.get_attachment_document(document_id)
        if document is None:
            raise RuntimeError("attachment document not found")
        text = "\n".join(str(document.get("content") or "").splitlines()).strip()
        try:
            chunks = document_chunks({**document, "content": text})
            if not chunks:
                raise RuntimeError("attachment document produced no chunks")
            vectors = self.embedder.embed([c["content"] for c in chunks])
            self.store.replace_chunks(document_id, str(document["message_id"]), chunks, vectors)
            delete_document_chunks(document_id)
            index_chunks([{**chunk, "raw_message_id": document.get("raw_message_id"),
                           "embedding_model": settings.embedding_model, "embedding_version": settings.processor_version,
                           "is_deleted": bool(document.get("is_deleted")), "indexed_at": datetime.now(timezone.utc),
                           "embedding": vector} for chunk, vector in zip(chunks, vectors)])
            self.store.mark_completed(document_id)
            self._finish(task_id, "completed", None)
        except Exception as exc:
            self.store.mark_failed(document_id, str(exc))
            self._fail(task_id, str(exc))

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
