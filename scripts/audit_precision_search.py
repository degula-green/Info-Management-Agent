#!/usr/bin/env python3
"""Audit the precision-retrieval chain across PostgreSQL and Elasticsearch."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

project_root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(project_root / "services" / "rag" / ".runtime" / "python"))
sys.path.insert(0, str(project_root / "services" / "rag"))
sys.path.insert(0, str(project_root / "scripts"))

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
from app.services.pgvector_store import PgVectorStore
from app.services.pgvector_store import _parse_vector
from elasticsearch import __versionstr__ as elasticsearch_client_version
from backfill_precision_search import build_es_doc, fetch_rows


def _normalize(value: Any) -> str:
    if value is None:
        return ""
    if hasattr(value, "isoformat"):
        try:
            return value.isoformat()
        except Exception:
            pass
    if isinstance(value, (dict, list)):
        try:
            return json.dumps(value, ensure_ascii=False, sort_keys=True, default=str)
        except Exception:
            pass
    return str(value)


def fetch_sample_chunks(store: PgVectorStore, limit: int) -> list[dict[str, Any]]:
    sample = fetch_rows(store)[:limit]
    for row in sample:
        row["embedding"] = _parse_vector(row.get("embedding_text"))
    return sample


def compare_sample_chunks(sample: list[dict[str, Any]]) -> dict[str, Any]:
    es = es_client()
    if not sample:
        return {"sample_size": 0, "matched": 0, "missing": 0, "mismatched": 0, "items": []}
    response = es.mget(index=settings.elasticsearch_index, body={"ids": [item["chunk_id"] for item in sample]})
    by_id = {str(doc.get("_id")): doc for doc in response.get("docs", [])}
    items: list[dict[str, Any]] = []
    matched = missing = mismatched = 0
    for row in sample:
        expected = build_es_doc(row)
        doc = by_id.get(str(row["chunk_id"]))
        if not doc or not doc.get("found"):
            missing += 1
            items.append({"chunk_id": row["chunk_id"], "status": "missing"})
            continue
        source = doc.get("_source") or {}
        diffs = []
        for field in sorted((set(expected.keys()) | set(source.keys())) - {"embedding", "indexed_at"}):
            left = _normalize(expected.get(field))
            right = _normalize(source.get(field))
            if left != right:
                diffs.append({"field": field, "pg": left, "es": right})
        if diffs:
            mismatched += 1
            items.append({"chunk_id": row["chunk_id"], "status": "mismatch", "diffs": diffs[:8]})
        else:
            matched += 1
            items.append({"chunk_id": row["chunk_id"], "status": "match"})
    return {"sample_size": len(sample), "matched": matched, "missing": missing, "mismatched": mismatched, "items": items}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--sample", type=int, default=20, help="number of completed chunks to inspect")
    args = parser.parse_args()
    store = PgVectorStore()
    sample = fetch_sample_chunks(store, max(1, args.sample))
    comparison = compare_sample_chunks(sample)
    with store.connect() as conn:
        documents_total = conn.execute("SELECT count(*) FROM vector_store.documents").fetchone()[0]
        ready_documents = conn.execute("SELECT count(*) FROM vector_store.documents WHERE status IN ('ready','completed')").fetchone()[0]
        indexed_documents = conn.execute("SELECT count(*) FROM vector_store.documents WHERE COALESCE(index_status, CASE WHEN status='completed' THEN 'indexed' END)='indexed'").fetchone()[0]
        chunk_total = conn.execute("SELECT count(*) FROM vector_store.chunks").fetchone()[0]
    es = es_client()
    es_info = es.info()
    es_total = es.count(index=settings.elasticsearch_index)["count"]
    summary = {
        "elasticsearch_index": settings.elasticsearch_index,
        "elasticsearch_server_version": es_info.get("version", {}).get("number"),
        "elasticsearch_client_version": elasticsearch_client_version,
        "documents_total": int(documents_total),
        "documents_ready": int(ready_documents),
        "documents_indexed": int(indexed_documents),
        "chunks_total": int(chunk_total),
        "es_total": int(es_total),
        "sample": comparison,
    }
    print(json.dumps(summary, ensure_ascii=False, default=str, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
