from __future__ import annotations

from elasticsearch import Elasticsearch, helpers

from app.config import settings


def client() -> Elasticsearch:
    kwargs = {"verify_certs": settings.elasticsearch_verify_certs}
    if settings.elasticsearch_username:
        kwargs["basic_auth"] = (settings.elasticsearch_username, settings.elasticsearch_password)
    return Elasticsearch(settings.elasticsearch_url, **kwargs)


def index_chunks(chunks: list[dict]) -> None:
    for chunk in chunks:
        vector = chunk.get("embedding")
        if not isinstance(vector, list) or len(vector) != settings.embedding_dimension:
            raise ValueError(f"Embedding dimension mismatch: expected {settings.embedding_dimension}")
    actions = [{"_index": settings.elasticsearch_index, "_id": chunk["chunk_id"], "_source": chunk} for chunk in chunks]
    if actions:
        helpers.bulk(client(), actions, raise_on_error=True, raise_on_exception=True)


def delete_message_chunks(message_id: str) -> None:
    client().delete_by_query(index=settings.elasticsearch_index,
                             query={"term": {"message_id": message_id}},
                             conflicts="proceed", refresh=True)


def delete_document_chunks(document_id: int) -> None:
    """Remove all indexed chunks for one PG document before replacing them."""
    client().delete_by_query(index=settings.elasticsearch_index,
                             query={"term": {"document_id": document_id}},
                             conflicts="proceed", refresh=True)
