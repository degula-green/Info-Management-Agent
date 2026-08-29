from __future__ import annotations

import hashlib
from collections.abc import Iterable
from typing import Any

try:
    import psycopg
except ModuleNotFoundError:  # Keep CLI help and pure tests usable without optional runtime deps.
    psycopg = None

from app.config import settings


class PgVectorStore:
    def __init__(self, database_url: str | None = None):
        self.database_url = database_url or settings.rag_database_url
        if not self.database_url:
            raise RuntimeError("RAG_DATABASE_URL is required")

    def connect(self):
        if psycopg is None:
            raise RuntimeError("psycopg is not installed; install services/rag/requirements.txt first")
        return psycopg.connect(self.database_url)

    def start_run(self, source: str, total_documents: int) -> int:
        with self.connect() as conn:
            row = conn.execute(
                """INSERT INTO vector_store.processing_runs
                   (run_type, source, processor_version, embedding_model, status, total_documents)
                   VALUES ('embedding', %s, %s, %s, 'running', %s) RETURNING id""",
                (source, settings.processor_version, settings.embedding_model, total_documents),
            ).fetchone()
            return int(row[0])

    def finish_run(self, run_id: int, status: str, completed: int, failed: int, chunks: int, error: str | None = None) -> None:
        with self.connect() as conn:
            conn.execute(
                """UPDATE vector_store.processing_runs
                   SET status=%s, completed_documents=%s, failed_documents=%s,
                       total_chunks=%s, error_message=%s, finished_at=now()
                   WHERE id=%s""",
                (status, completed, failed, chunks, error, run_id),
            )

    def resolve_source(self, message: dict[str, Any]) -> tuple[int, int] | None:
        with self.connect() as conn:
            row = conn.execute(
                """SELECT sa.id, rm.id
                   FROM ingestion.source_accounts sa
                   JOIN ingestion.raw_messages rm
                     ON rm.source_account_id = sa.id
                    AND rm.source_message_id = %s
                   WHERE sa.platform = %s AND sa.external_account_id = %s""",
                (str(message.get("source_message_id", "")), message.get("source"), str(message.get("source_account_id", ""))),
            ).fetchone()
            return (int(row[0]), int(row[1])) if row else None

    def pending_messages(self, limit: int = 10) -> list[dict[str, Any]]:
        with self.connect() as conn:
            rows = conn.execute(
                """SELECT m.id, m.source, sa.external_account_id, m.source_message_id,
                          m.text, m.message_type, m.metadata
                   FROM ingestion.messages m
                   JOIN ingestion.source_accounts sa ON sa.id = m.source_account_id
                   WHERE COALESCE(m.text, '') <> ''
                     AND NOT EXISTS (
                         SELECT 1 FROM vector_store.documents d
                         WHERE d.raw_message_id = m.raw_message_id
                           AND d.processor_version = %s
                           AND d.status = 'completed'
                     )
                   ORDER BY m.occurred_at NULLS LAST, m.id
                   LIMIT %s""",
                (settings.processor_version, limit),
            ).fetchall()
        return [{"id": row[0], "source": row[1], "source_account_id": row[2],
                 "source_message_id": row[3], "text": row[4],
                 "message_type": row[5], "metadata": row[6] or {}} for row in rows]

    def get_message(self, message_id: str) -> dict[str, Any] | None:
        with self.connect() as conn:
            row = conn.execute("""SELECT m.id,m.source,sa.external_account_id,m.source_message_id,m.text,m.message_type,m.metadata
                FROM ingestion.messages m JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id WHERE m.id=%s""", (message_id,)).fetchone()
        if not row: return None
        return {"id": row[0], "source": row[1], "source_account_id": row[2], "source_message_id": row[3], "text": row[4], "message_type": row[5], "metadata": row[6] or {}}

    def upsert_document(self, message: dict[str, Any], source_account_id: int, raw_message_id: int, status: str, content: str) -> int:
        content_hash = hashlib.sha256(content.encode("utf-8")).hexdigest()
        with self.connect() as conn:
            row = conn.execute(
                """INSERT INTO vector_store.documents
                   (source_account_id, raw_message_id, source_message_id, document_type,
                    content, content_hash, metadata, processor_version, status)
                   VALUES (%s, %s, %s, 'message', %s, %s, %s, %s, %s)
                   ON CONFLICT (raw_message_id, document_type, processor_version)
                   DO UPDATE SET content=EXCLUDED.content, content_hash=EXCLUDED.content_hash,
                       metadata=EXCLUDED.metadata, status=EXCLUDED.status,
                       error_message=NULL, updated_at=now()
                   RETURNING id""",
                (source_account_id, raw_message_id, str(message.get("source_message_id", "")), content,
                 content_hash, psycopg.types.json.Jsonb(message.get("metadata") or {}), settings.processor_version, status),
            ).fetchone()
            return int(row[0])

    def replace_chunks(self, document_id: int, message_id: str, chunks: Iterable[dict[str, Any]], vectors: list[list[float]]) -> int:
        chunks = list(chunks)
        if len(chunks) != len(vectors):
            raise RuntimeError("Embedding API returned an unexpected number of vectors")
        for vector in vectors:
            if len(vector) != settings.rag_vector_dimension:
                raise RuntimeError(f"Embedding dimension mismatch: expected {settings.rag_vector_dimension}, got {len(vector)}")
        with self.connect() as conn:
            conn.execute("DELETE FROM vector_store.chunks WHERE document_id=%s", (document_id,))
            for chunk, vector in zip(chunks, vectors):
                content_hash = hashlib.sha256(chunk["content"].encode("utf-8")).hexdigest()
                conn.execute(
                    """INSERT INTO vector_store.chunks
                       (document_id, chunk_id, chunk_position, content, content_hash,
                        embedding, embedding_model, embedding_dimension, processor_version, metadata)
                       VALUES (%s, %s, %s, %s, %s, %s::vector, %s, %s, %s, %s)""",
                    (document_id, chunk["chunk_id"], chunk["chunk_position"], chunk["content"], content_hash,
                     "[" + ",".join(str(float(value)) for value in vector) + "]", settings.embedding_model,
                     len(vector), settings.processor_version, psycopg.types.json.Jsonb(chunk.get("metadata") or {})),
                )
            conn.execute("UPDATE vector_store.documents SET status='completed', updated_at=now() WHERE id=%s", (document_id,))
        return len(chunks)

    def mark_skipped(self, document_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE vector_store.documents SET status='skipped', updated_at=now() WHERE id=%s", (document_id,))

    def mark_failed(self, document_id: int, error: str) -> None:
        with self.connect() as conn:
            conn.execute(
                "UPDATE vector_store.documents SET status='failed', error_message=%s, updated_at=now() WHERE id=%s",
                (error[:4000], document_id),
            )
