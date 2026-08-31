#!/usr/bin/env python3
"""Backfill Elasticsearch from PostgreSQL vector_store chunks."""
from __future__ import annotations

import argparse
import hashlib
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

project_root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(project_root / "services" / "rag" / ".runtime" / "python"))
sys.path.insert(0, str(project_root / "services" / "rag"))

try:
    from dotenv import load_dotenv
except ModuleNotFoundError:  # pragma: no cover - runtime bundle may provide it instead.
    load_dotenv = None

if load_dotenv is not None:
    for candidate in (project_root / "services" / "rag" / ".env", project_root / ".env"):
        if candidate.exists():
            load_dotenv(candidate, override=False)

from app.config import settings
from app.services.index_service import client as es_client
from app.services.index_service import ensure_index
from app.services.index_service import index_chunks
from app.services.index_service import reusable_vectors
from app.services.pgvector_store import PgVectorStore, _parse_vector
from app.services.vectorization import EmbeddingClient
from app.services.vectorization import canonical_platform


def _normalize(value: Any) -> str:
    if value is None:
        return ""
    if hasattr(value, "isoformat"):
        try:
            return value.isoformat()
        except Exception:
            pass
    return str(value)


def _hash_content(content: str) -> str:
    return hashlib.sha256(content.encode("utf-8")).hexdigest()


def _is_valid_vector(row: dict[str, Any], expected_hash: str) -> tuple[bool, list[float]]:
    vector = _parse_vector(row.get("embedding_text"))
    model = _normalize(row.get("embedding_model")).strip()
    version = _normalize(row.get("processor_version")).strip()
    stored_hash = _normalize(row.get("content_hash")).strip()
    if (
        len(vector) == settings.embedding_dimension
        and model == settings.embedding_model
        and version == settings.processor_version
        and stored_hash == expected_hash
    ):
        return True, vector
    return False, vector


def _set_if_not_none(target: dict[str, Any], key: str, value: Any) -> None:
    if value is None:
        return
    if isinstance(value, str) and not value.strip():
        return
    target[key] = value


def fetch_rows(store: PgVectorStore) -> list[dict[str, Any]]:
    with store.connect() as conn:
        rows = conn.execute(
            """
            SELECT
                c.id AS chunk_row_id,
                c.chunk_id,
                c.chunk_position,
                c.content,
                c.content_hash,
                c.embedding::text AS embedding_text,
                c.embedding_model,
                c.embedding_dimension,
                c.processor_version,
                c.metadata,
                d.id AS document_id,
                d.document_type,
                d.source_account_id,
                d.raw_message_id,
                d.source_message_id,
                d.attachment_id,
                d.status,
                d.index_status,
                sa.platform,
                sa.external_account_id,
                sa.internal_account_id,
                m.id AS message_id,
                m.source AS message_source,
                m.conversation_id,
                m.sender_id,
                m.occurred_at,
                m.message_type,
                m.is_deleted AS message_deleted,
                co.external_conversation_id,
                co.name AS conversation_name,
                p.external_participant_id,
                p.display_name AS sender_name,
                a.id AS file_id,
                a.file_name,
                a.user_id AS attachment_user_id,
                a.is_deleted AS attachment_deleted
            FROM vector_store.chunks c
            JOIN vector_store.documents d ON d.id = c.document_id
            JOIN ingestion.source_accounts sa ON sa.id = d.source_account_id
            LEFT JOIN ingestion.messages m
              ON m.raw_message_id = d.raw_message_id AND m.source_account_id = d.source_account_id
            LEFT JOIN ingestion.conversations co ON co.id = m.conversation_id
            LEFT JOIN ingestion.participants p ON p.id = m.sender_id
            LEFT JOIN ingestion.attachments a ON a.id = d.attachment_id
            WHERE d.status IN ('ready', 'completed')
              AND COALESCE(d.index_status, CASE WHEN d.status='completed' THEN 'indexed' END)='indexed'
            ORDER BY d.id, c.chunk_position, c.id
            """
        ).fetchall()
    return [
        {
            "chunk_row_id": row[0],
            "chunk_id": row[1],
            "chunk_position": row[2],
            "content": row[3],
            "content_hash": row[4],
            "embedding_text": row[5],
            "embedding_model": row[6],
            "embedding_dimension": row[7],
            "processor_version": row[8],
            "metadata": row[9] or {},
            "document_id": row[10],
            "document_type": row[11],
            "source_account_id": row[12],
            "raw_message_id": row[13],
            "source_message_id": row[14],
            "attachment_id": row[15],
            "status": row[16],
            "index_status": row[17],
            "platform": row[18],
            "external_account_id": row[19],
            "internal_account_id": row[20],
            "message_id": row[21],
            "message_source": row[22],
            "conversation_id": row[23],
            "sender_id": row[24],
            "occurred_at": row[25],
            "message_type": row[26],
            "message_deleted": row[27],
            "external_conversation_id": row[28],
            "conversation_name": row[29],
            "external_sender_id": row[30],
            "sender_name": row[31],
            "file_id": row[32],
            "file_name": row[33],
            "attachment_user_id": row[34],
            "attachment_deleted": row[35],
        }
        for row in rows
    ]


def compute_missing_embeddings(
    store: PgVectorStore,
    embedder: EmbeddingClient,
    rows: list[dict[str, Any]],
    batch_size: int,
) -> tuple[int, int]:
    missing = []
    for row in rows:
        content = str(row["content"] or "")
        expected_hash = _hash_content(content)
        valid, vector = _is_valid_vector(row, expected_hash)
        if valid:
            row["embedding"] = vector
            row["content_hash"] = expected_hash
            continue
        row["content_hash"] = expected_hash
        row["embedding"] = None
        missing.append(row)

    recomputed = 0
    failed = 0
    if not missing:
        return recomputed, failed

    with store.connect() as conn:
        for start in range(0, len(missing), batch_size):
            batch = missing[start : start + batch_size]
            try:
                vectors = embedder.embed([str(row["content"] or "") for row in batch])
                if len(vectors) != len(batch):
                    raise RuntimeError("Embedding API returned an unexpected number of vectors")
            except Exception as exc:
                failed += len(batch)
                for row in batch:
                    row["embedding_error"] = f"embedding_failed: {type(exc).__name__}"
                continue
            for row, vector in zip(batch, vectors, strict=True):
                conn.execute(
                    """UPDATE vector_store.chunks
                       SET embedding=%s::vector,
                           embedding_model=%s,
                           embedding_dimension=%s,
                           processor_version=%s,
                           content_hash=%s,
                           updated_at=now()
                       WHERE id=%s""",
                    (
                        "[" + ",".join(f"{float(value):.12g}" for value in vector) + "]",
                        settings.embedding_model,
                        len(vector),
                        settings.processor_version,
                        row["content_hash"],
                        row["chunk_row_id"],
                    ),
                )
                row["embedding"] = vector
                row["embedding_model"] = settings.embedding_model
                row["embedding_dimension"] = len(vector)
                row["processor_version"] = settings.processor_version
                recomputed += 1
    return recomputed, failed


def build_es_doc(row: dict[str, Any]) -> dict[str, Any]:
    platform = canonical_platform(row["platform"]) or _normalize(row["platform"]).strip()
    document: dict[str, Any] = {
        "chunk_id": row["chunk_id"],
        "document_id": int(row["document_id"]),
        "content": str(row["content"] or ""),
        "chunk_position": int(row["chunk_position"]),
        "content_hash": _normalize(row["content_hash"]).strip(),
        "embedding_model": settings.embedding_model,
        "embedding_version": settings.processor_version,
        "metadata": row["metadata"] or {},
        "platform": platform,
        "source": platform,
        "resource_type": row["document_type"],
        "source_account_id": int(row["source_account_id"]),
        "external_account_id": row["external_account_id"],
        "source_message_id": row["source_message_id"],
        "message_id": row["message_id"],
        "conversation_id": row["conversation_id"],
        "occurred_at": row["occurred_at"],
        "message_type": row["message_type"] if row["document_type"] == "message" else "document",
        "sender_id": row["sender_id"],
        "indexed_at": datetime.now(timezone.utc).isoformat(),
        "embedding": row["embedding"],
    }
    _set_if_not_none(document, "conversation_name", row["conversation_name"])
    _set_if_not_none(document, "external_conversation_id", row["external_conversation_id"])
    _set_if_not_none(document, "external_sender_id", row["external_sender_id"])
    _set_if_not_none(document, "sender_name", row["sender_name"])
    _set_if_not_none(document, "user_id", row["internal_account_id"])

    if row["document_type"] == "attachment":
        document["source_type"] = "attachment"
        document["file_id"] = str(row["file_id"])
        _set_if_not_none(document, "file_name", row["file_name"])
        if isinstance(row["metadata"], dict):
            heading_path = row["metadata"].get("heading_path")
            if isinstance(heading_path, list) and heading_path:
                _set_if_not_none(document, "source_position", heading_path[0])
        _set_if_not_none(document, "user_id", row["attachment_user_id"] or row["internal_account_id"])
        document["is_deleted"] = bool(row["attachment_deleted"]) or bool(row["message_deleted"])
    else:
        document["is_deleted"] = bool(row["message_deleted"])

    return document


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--batch-size", type=int, default=8, help="chunk batch size for embedding and ES writes")
    args = parser.parse_args()
    batch_size = max(1, min(int(args.batch_size), 8))

    store = PgVectorStore()
    embedder = EmbeddingClient()
    es = es_client()
    ensure_index(es)

    rows = fetch_rows(store)
    if not rows:
        print(json.dumps({"total": 0, "indexed": 0, "skipped": 0, "reused_pg": 0, "recomputed": 0, "failed": 0}, ensure_ascii=False))
        return 0

    reused_pg = 0
    recomputed = 0
    failed = 0
    skipped = 0
    indexed = 0
    docs: list[dict[str, Any]] = []

    recompute_count, failed_count = compute_missing_embeddings(store, embedder, rows, batch_size)
    recomputed += recompute_count
    failed += failed_count

    for row in rows:
        if row.get("embedding") is not None and _is_valid_vector(row, _hash_content(str(row["content"] or "")))[0]:
            reused_pg += 1
        if row.get("embedding") is None:
            continue
        docs.append(build_es_doc(row))

    for start in range(0, len(docs), batch_size):
        batch = docs[start : start + batch_size]
        existing = reusable_vectors(batch, es)
        to_write = [doc for doc in batch if doc["chunk_id"] not in existing]
        skipped += len(batch) - len(to_write)
        if not to_write:
            continue
        index_chunks(to_write)
        indexed += len(to_write)

    summary = {
        "total": len(rows),
        "indexed": indexed,
        "skipped": skipped,
        "reused_pg": reused_pg,
        "recomputed": recomputed,
        "failed": failed,
        "es_index": settings.elasticsearch_index,
    }
    print(json.dumps(summary, ensure_ascii=False, default=str))
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
