from __future__ import annotations

import json
from pathlib import Path

from elasticsearch import Elasticsearch, helpers

from app.config import settings
from app.services.vectorization import canonical_platform


def client() -> Elasticsearch:
    kwargs = {"verify_certs": settings.elasticsearch_verify_certs}
    if settings.elasticsearch_username:
        kwargs["basic_auth"] = (settings.elasticsearch_username, settings.elasticsearch_password)
    return Elasticsearch(settings.elasticsearch_url, **kwargs)


def _mapping_definition() -> dict:
    path = Path(__file__).resolve().parents[2] / "config" / "elasticsearch" / "documents-index.json"
    return json.loads(path.read_text(encoding="utf-8"))


def _canonical_platform(value: object) -> str:
    return canonical_platform(value) or str(value or "")


def ensure_index(es: Elasticsearch | None = None) -> None:
    """Create the chunk index or add the v2 fields to an existing index."""
    es = es or client()
    definition = _mapping_definition()
    index = settings.elasticsearch_index
    if not es.indices.exists(index=index):
        es.indices.create(index=index, settings=definition.get("settings", {}), mappings=definition.get("mappings", {}))
        return
    properties = definition.get("mappings", {}).get("properties", {})
    if properties:
        es.indices.put_mapping(index=index, properties=properties)


def index_chunks(chunks: list[dict]) -> None:
    for chunk in chunks:
        vector = chunk.get("embedding")
        if not isinstance(vector, list) or len(vector) != settings.embedding_dimension:
            raise ValueError(f"Embedding dimension mismatch: expected {settings.embedding_dimension}")
    es = client()
    ensure_index(es)
    actions = []
    for chunk in chunks:
        source = {**chunk}
        source.setdefault("document_status", "completed")
        if "attachment_id" not in source and source.get("file_id") is not None:
            try:
                source["attachment_id"] = int(source["file_id"])
            except (TypeError, ValueError):
                pass
        if "source" in source:
            source["source"] = _canonical_platform(source.get("source"))
        if "platform" in source:
            source["platform"] = _canonical_platform(source.get("platform"))
        elif source.get("source"):
            source["platform"] = source["source"]
        actions.append({"_index": settings.elasticsearch_index, "_id": chunk["chunk_id"], "_source": source})
    if actions:
        helpers.bulk(es, actions, raise_on_error=True, raise_on_exception=True)


def delete_message_chunks(message_id: str) -> None:
    client().delete_by_query(index=settings.elasticsearch_index,
                             query={"term": {"message_id": message_id}},
                             conflicts="proceed", refresh=True)


def delete_document_chunks(document_id: int) -> None:
    """Remove all indexed chunks for one PG document before replacing them."""
    client().delete_by_query(index=settings.elasticsearch_index,
                             query={"term": {"document_id": document_id}},
                             conflicts="proceed", refresh=True)
