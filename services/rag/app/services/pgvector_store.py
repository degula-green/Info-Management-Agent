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

    def pending_messages(self, limit: int = 10) -> list[dict[str, Any]]:
        with self.connect() as conn:
            rows = conn.execute(
                """SELECT m.id, m.source, m.source_account_id, sa.external_account_id,
                          sa.internal_account_id, m.source_message_id, m.conversation_id,
                          c.external_conversation_id, m.sender_id, p.external_participant_id,
                          m.text, m.message_type, m.metadata, m.occurred_at
                   FROM ingestion.messages m
                   JOIN ingestion.source_accounts sa ON sa.id = m.source_account_id
                   LEFT JOIN ingestion.conversations c ON c.id = m.conversation_id
                   LEFT JOIN ingestion.participants p ON p.id = m.sender_id
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
                 "external_account_id": row[3], "user_id": row[4],
                 "source_message_id": row[5], "conversation_id": row[6],
                 "external_conversation_id": row[7], "sender_id": row[8],
                 "external_sender_id": row[9], "text": row[10],
                 "message_type": row[11], "metadata": row[12] or {},
                 "occurred_at": row[13]} for row in rows]

    def get_message(self, message_id: str) -> dict[str, Any] | None:
        with self.connect() as conn:
            row = conn.execute("""SELECT m.id, m.raw_message_id, m.source, m.source_account_id,
                    sa.internal_account_id, sa.external_account_id, m.source_message_id,
                    m.conversation_id, c.external_conversation_id,
                    m.sender_id, p.external_participant_id,
                    m.text, m.message_type, m.metadata, m.occurred_at,
                    m.is_deleted, m.is_updated
                FROM ingestion.messages m
                JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id
                LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
                LEFT JOIN ingestion.participants p ON p.id=m.sender_id
                WHERE m.id=%s""", (message_id,)).fetchone()
        if not row: return None
        return {"id": row[0], "raw_message_id": row[1], "source": row[2], "source_account_id": row[3],
                "user_id": row[4], "external_account_id": row[5], "source_message_id": row[6],
                "conversation_id": row[7], "external_conversation_id": row[8], "sender_id": row[9],
                "external_sender_id": row[10], "text": row[11], "message_type": row[12],
                "metadata": row[13] or {}, "occurred_at": row[14], "is_deleted": row[15], "is_updated": row[16]}

    def upsert_document(self, message: dict[str, Any], source_account_id: int, raw_message_id: int, status: str, content: str) -> int:
        content_hash = hashlib.sha256(content.encode("utf-8")).hexdigest()
        with self.connect() as conn:
            row = conn.execute(
                """INSERT INTO vector_store.documents
                   (source_account_id, raw_message_id, source_message_id, document_type,
                    content, content_hash, metadata, processor_version, status)
                   VALUES (%s, %s, %s, 'message', %s, %s, %s, %s, %s)
                   ON CONFLICT (raw_message_id, document_type, processor_version) WHERE attachment_id IS NULL
                   DO UPDATE SET content=EXCLUDED.content, content_hash=EXCLUDED.content_hash,
                       metadata=EXCLUDED.metadata, status=EXCLUDED.status,
                       error_message=NULL, updated_at=now()
                   RETURNING id""",
                (source_account_id, raw_message_id, str(message.get("source_message_id", "")), content,
                 content_hash, psycopg.types.json.Jsonb(message.get("metadata") or {}), settings.processor_version, status),
            ).fetchone()
            return int(row[0])

    def get_attachment_context(self, attachment_id: int) -> dict[str, Any] | None:
        with self.connect() as conn:
            row = conn.execute("""SELECT a.id,a.message_id,a.raw_message_id,a.source_account_id,
                    a.user_id,a.platform,a.file_name,a.extension,a.mime_type,a.file_size,a.content_hash,
                    a.storage_bucket,a.storage_key,a.download_status,a.parse_status,a.is_deleted,
                    m.source_message_id,m.conversation_id,m.sender_id,m.occurred_at,m.is_deleted,
                    sa.external_account_id,sa.internal_account_id,c.external_conversation_id,
                    p.external_participant_id
                FROM ingestion.attachments a
                JOIN ingestion.messages m ON m.id=a.message_id
                JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id
                LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
                LEFT JOIN ingestion.participants p ON p.id=m.sender_id
                WHERE a.id=%s""", (attachment_id,)).fetchone()
        if not row:
            return None
        keys = ("attachment_id","message_id","raw_message_id","source_account_id","user_id","platform",
                "file_name","extension","mime_type","file_size","content_hash","storage_bucket","storage_key",
                "download_status","parse_status","attachment_deleted","source_message_id","conversation_id",
                "sender_id","occurred_at","message_deleted","external_account_id","account_user_id",
                "external_conversation_id","external_sender_id")
        context = dict(zip(keys, row))
        context["user_id"] = context["user_id"] or context["account_user_id"]
        return context

    def begin_attachment_parse(self, attachment_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.attachments SET parse_status='processing',last_error=NULL,updated_at=now() WHERE id=%s", (attachment_id,))

    def upsert_attachment_document(self, context: dict[str, Any], content: str, metadata: dict[str, Any], status: str = "processing") -> int:
        if context.get("raw_message_id") is None:
            raise RuntimeError("attachment is missing raw_message_id")
        digest = hashlib.sha256(content.encode("utf-8")).hexdigest()
        with self.connect() as conn:
            row = conn.execute("""INSERT INTO vector_store.documents
                    (source_account_id,raw_message_id,source_message_id,attachment_id,document_type,content,content_hash,metadata,processor_version,status)
                    VALUES(%s,%s,%s,%s,'attachment',%s,%s,%s,%s,%s)
                    ON CONFLICT (attachment_id,document_type,processor_version) WHERE attachment_id IS NOT NULL
                    DO UPDATE SET raw_message_id=EXCLUDED.raw_message_id,source_message_id=EXCLUDED.source_message_id,
                      content=EXCLUDED.content,content_hash=EXCLUDED.content_hash,metadata=EXCLUDED.metadata,
                      status=EXCLUDED.status,error_message=NULL,updated_at=now() RETURNING id""",
                (context["source_account_id"], context["raw_message_id"], str(context.get("source_message_id") or ""),
                 context["attachment_id"], content, digest, psycopg.types.json.Jsonb(metadata), settings.attachment_parser_version, status)).fetchone()
            return int(row[0])

    def current_attachment_document(self, context: dict[str, Any]) -> int | None:
        """Return a completed document only when it represents the current stored file."""
        with self.connect() as conn:
            row = conn.execute("""SELECT id FROM vector_store.documents
                WHERE attachment_id=%s AND document_type='attachment' AND processor_version=%s
                  AND status='completed' AND metadata->>'source_content_hash'=%s""",
                (context["attachment_id"], settings.attachment_parser_version, context.get("content_hash") or "")).fetchone()
        return int(row[0]) if row else None

    def stale_attachment_document_ids(self, attachment_id: int) -> list[int]:
        with self.connect() as conn:
            rows = conn.execute("""SELECT id FROM vector_store.documents
                WHERE attachment_id=%s AND document_type='attachment' AND processor_version<>%s""",
                (attachment_id, settings.attachment_parser_version)).fetchall()
        return [int(row[0]) for row in rows]

    def delete_documents(self, document_ids: list[int]) -> None:
        if not document_ids:
            return
        with self.connect() as conn:
            conn.execute("DELETE FROM vector_store.chunks WHERE document_id=ANY(%s)", (document_ids,))
            conn.execute("DELETE FROM vector_store.documents WHERE id=ANY(%s)", (document_ids,))

    def get_attachment_document(self, document_id: int) -> dict[str, Any] | None:
        with self.connect() as conn:
            row = conn.execute("""SELECT d.id,d.attachment_id,d.content,d.metadata,d.content_hash,d.status,
                    a.message_id,a.source_account_id,a.user_id,a.platform,a.is_deleted,
                    m.raw_message_id,m.source_message_id,m.conversation_id,m.sender_id,m.occurred_at,m.is_deleted,
                    sa.external_account_id,sa.internal_account_id,c.external_conversation_id,p.external_participant_id
                FROM vector_store.documents d
                JOIN ingestion.attachments a ON a.id=d.attachment_id
                JOIN ingestion.messages m ON m.id=a.message_id
                JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id
                LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
                LEFT JOIN ingestion.participants p ON p.id=m.sender_id
                WHERE d.id=%s AND d.document_type='attachment'""", (document_id,)).fetchone()
        if not row:
            return None
        keys = ("document_id","attachment_id","content","metadata","content_hash","status","message_id",
                "source_account_id","user_id","source","attachment_deleted","raw_message_id","source_message_id",
                "conversation_id","sender_id","occurred_at","message_deleted","external_account_id","account_user_id",
                "external_conversation_id","external_sender_id")
        result = dict(zip(keys, row))
        result["user_id"] = result["user_id"] or result["account_user_id"]
        result["is_deleted"] = bool(result.pop("attachment_deleted")) or bool(result.pop("message_deleted"))
        return result

    def finish_attachment_parse(self, attachment_id: int, task_id: int, document_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.attachments SET parse_status='completed',last_error=NULL,updated_at=now() WHERE id=%s", (attachment_id,))
            conn.execute("UPDATE ingestion.worker_tasks SET status='completed',locked_by=NULL,locked_until=NULL,last_error=NULL,completed_at=now(),updated_at=now() WHERE id=%s", (task_id,))
            conn.execute("""INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload)
                VALUES('vectorization',%s,%s) ON CONFLICT(task_type,entity_id) DO UPDATE SET
                  payload=EXCLUDED.payload,status=CASE WHEN ingestion.worker_tasks.status IN ('failed','dead') THEN 'pending' ELSE ingestion.worker_tasks.status END,
                  next_run_at=CASE WHEN ingestion.worker_tasks.status IN ('failed','dead') THEN now() ELSE ingestion.worker_tasks.next_run_at END,updated_at=now()""",
                (f"attachment-document:{document_id}", psycopg.types.json.Jsonb({"kind":"attachment","document_id":document_id,"attachment_id":attachment_id})))

    def skip_attachment_parse(self, attachment_id: int, task_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.attachments SET parse_status='completed',last_error=NULL,updated_at=now() WHERE id=%s", (attachment_id,))
            conn.execute("UPDATE ingestion.worker_tasks SET status='completed',locked_by=NULL,locked_until=NULL,last_error=NULL,completed_at=now(),updated_at=now() WHERE id=%s", (task_id,))

    def fail_attachment_parse(self, attachment_id: int, task_id: int, error: str) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.attachments SET parse_status='failed',last_error=%s,updated_at=now() WHERE id=%s", (error[:4000], attachment_id))
            conn.execute("""UPDATE ingestion.worker_tasks SET status=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'failed' END,
                    next_run_at=now() + (LEAST(3600,60*(2^LEAST(attempts-1,6)))*interval '1 second'),locked_by=NULL,locked_until=NULL,last_error=%s,updated_at=now() WHERE id=%s""", (error[:4000],task_id))

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
                       VALUES (%s, %s, %s, %s, %s, NULL, %s, %s, %s, %s)""",
                    (document_id, chunk["chunk_id"], chunk["chunk_position"], chunk["content"], content_hash,
                     settings.embedding_model,
                     len(vector), settings.processor_version, psycopg.types.json.Jsonb(chunk.get("metadata") or {})),
                )
            conn.execute("UPDATE vector_store.documents SET status='processing', updated_at=now() WHERE id=%s", (document_id,))
        return len(chunks)

    def mark_completed(self, document_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE vector_store.documents SET status='completed', updated_at=now() WHERE id=%s", (document_id,))

    def mark_skipped(self, document_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE vector_store.documents SET status='skipped', updated_at=now() WHERE id=%s", (document_id,))

    def mark_failed(self, document_id: int, error: str) -> None:
        with self.connect() as conn:
            conn.execute(
                "UPDATE vector_store.documents SET status='failed', error_message=%s, updated_at=now() WHERE id=%s",
                (error[:4000], document_id),
            )
