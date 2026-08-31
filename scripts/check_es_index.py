#!/usr/bin/env python3
"""Inspect the hybrid retrieval index without exposing document contents."""
from __future__ import annotations

import json
import os
import sys

from elasticsearch import Elasticsearch


def main() -> int:
    url = os.getenv("ELASTICSEARCH_URL", "http://127.0.0.1:9200")
    index = os.getenv("ELASTICSEARCH_INDEX", "info-agent-chunks-v1")
    kwargs = {"verify_certs": os.getenv("ELASTICSEARCH_VERIFY_CERTS", "false").lower() == "true"}
    if os.getenv("ELASTICSEARCH_USERNAME"):
        kwargs["basic_auth"] = (os.environ["ELASTICSEARCH_USERNAME"], os.getenv("ELASTICSEARCH_PASSWORD", ""))
    es = Elasticsearch(url, **kwargs)
    if not es.indices.exists(index=index):
        print(json.dumps({"index": index, "exists": False}, ensure_ascii=False))
        return 1
    mapping = es.indices.get_mapping(index=index)[index]["mappings"].get("properties", {})
    fields = ["content", "embedding", "document_status", "attachment_id", "file_name", "user_id", "source_account_id", "conversation_id"]
    status = es.search(index=index, size=0, aggs={"document_status": {"terms": {"field": "document_status"}}})
    buckets = status.get("aggregations", {}).get("document_status", {}).get("buckets", [])
    result = {"index": index, "exists": True, "count": status["hits"]["total"]["value"],
              "required_fields": {field: field in mapping for field in fields},
              "document_status": {bucket["key"]: bucket["doc_count"] for bucket in buckets}}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if all(result["required_fields"].values()) else 2


if __name__ == "__main__":
    raise SystemExit(main())
