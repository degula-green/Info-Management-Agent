#!/usr/bin/env python3
"""Rewrite legacy personal_wechat fields in Elasticsearch to canonical wechat."""
from __future__ import annotations

import json
import sys
from pathlib import Path

project_root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(project_root / "services" / "rag" / ".runtime" / "python"))
sys.path.insert(0, str(project_root / "services" / "rag"))

try:
    from dotenv import load_dotenv
except ModuleNotFoundError:  # pragma: no cover
    load_dotenv = None

if load_dotenv is not None:
    for candidate in (project_root / "services" / "rag" / ".env", project_root / ".env"):
        if candidate.exists():
            load_dotenv(candidate, override=False)

from app.config import settings
from app.services.index_service import client as es_client
from app.services.index_service import ensure_index


def main() -> int:
    es = es_client()
    ensure_index(es)
    result = es.update_by_query(
        index=settings.elasticsearch_index,
        conflicts="proceed",
        refresh=True,
        body={
            "script": {
                "lang": "painless",
                "source": """
                    if (ctx._source.containsKey('source') && ctx._source.source == params.legacy) {
                        ctx._source.source = params.canonical;
                    }
                    if (ctx._source.containsKey('platform') && ctx._source.platform == params.legacy) {
                        ctx._source.platform = params.canonical;
                    }
                """,
                "params": {"legacy": "personal_wechat", "canonical": "wechat"},
            },
            "query": {
                "bool": {
                    "should": [
                        {"term": {"source": "personal_wechat"}},
                        {"term": {"platform": "personal_wechat"}},
                    ],
                    "minimum_should_match": 1,
                }
            },
        },
    )
    print(json.dumps(result, ensure_ascii=False, default=str))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
