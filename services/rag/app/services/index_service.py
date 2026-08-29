from __future__ import annotations

from elasticsearch import Elasticsearch, helpers

from app.config import settings


def client() -> Elasticsearch:
    kwargs = {"verify_certs": settings.elasticsearch_verify_certs}
    if settings.elasticsearch_username:
        kwargs["basic_auth"] = (settings.elasticsearch_username, settings.elasticsearch_password)
    return Elasticsearch(settings.elasticsearch_url, **kwargs)


def index_chunks(chunks: list[dict]) -> None:
    actions = [{"_index": settings.elasticsearch_index, "_id": chunk["chunk_id"], "_source": chunk} for chunk in chunks]
    if actions:
        helpers.bulk(client(), actions)
