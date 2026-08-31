#!/usr/bin/env python3
"""Import normalized messages, generate embeddings, and persist them in pgvector."""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

project_root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(project_root / "services" / "rag" / ".runtime" / "python"))
sys.path.insert(0, str(project_root / "services" / "rag"))

from app.services.pgvector_store import PgVectorStore
from app.services.vectorization import EmbeddingClient, message_chunks
from app.services.index_service import delete_message_chunks, index_chunks
from app.config import settings


def read_messages(path: Path, limit: int) -> list[dict]:
    messages = []
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            if line.strip():
                messages.append(json.loads(line))
                if limit and len(messages) >= limit:
                    break
    return messages


def process_messages(messages: list[dict], store: PgVectorStore, embedder: EmbeddingClient) -> int:
    run_id = store.start_run(messages[0].get("source", "local") if messages else "local", len(messages))
    completed = failed = chunks_count = skipped = 0
    errors = []
    for message in messages:
        document_id = None
        try:
            resolved = store.resolve_source(message)
            if not resolved:
                raise RuntimeError("source account or raw message not found")
            account_id, raw_id = resolved
            text = " ".join(str(message.get("text") or "").split())
            document_id = store.upsert_document(message, account_id, raw_id, "processing", text)
            if not text:
                store.mark_skipped(document_id)
                skipped += 1
                continue
            chunks = message_chunks({**message, "text": text})
            vectors = embedder.embed([chunk["content"] for chunk in chunks])
            chunks_count += store.replace_chunks(document_id, str(message.get("id", "")), chunks, vectors)
            message_id = str(message.get("id", ""))
            delete_message_chunks(message_id)
            index_chunks([{**chunk,
                           "document_id": document_id,
                           "raw_message_id": raw_id,
                           "embedding_model": settings.embedding_model,
                           "embedding_version": settings.processor_version,
                           "document_status": "completed",
                           "is_deleted": bool(message.get("is_deleted")),
                           "indexed_at": datetime.now(timezone.utc),
                           "embedding": vector}
                          for chunk, vector in zip(chunks, vectors)])
            store.mark_completed(document_id)
            completed += 1
        except Exception as exc:
            if document_id is not None:
                store.mark_failed(document_id, str(exc))
            failed += 1
            errors.append(f"{message.get('source_message_id', '')}: {exc}")
    status = "failed" if failed and not completed and not skipped else ("partial" if failed else "completed")
    store.finish_run(run_id, status, completed + skipped, failed, chunks_count, "; ".join(errors[:10]) or None)
    print(json.dumps({"run_id": run_id, "messages": len(messages), "completed": completed,
                      "skipped": skipped, "failed": failed, "chunks": chunks_count}, ensure_ascii=False))
    return 1 if failed else 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", default="data/collector-feishu/normalized/messages.jsonl")
    parser.add_argument("--limit", type=int, default=10, help="messages to process; 0 means all")
    parser.add_argument("--from-db", action="store_true", help="read pending messages from PostgreSQL")
    args = parser.parse_args()
    store = PgVectorStore()
    embedder = EmbeddingClient()
    messages = store.pending_messages(args.limit or 100000) if args.from_db else read_messages(Path(args.input), args.limit)
    return process_messages(messages, store, embedder)


if __name__ == "__main__":
    raise SystemExit(main())
