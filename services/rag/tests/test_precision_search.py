from __future__ import annotations

import unittest
from unittest.mock import patch

from app.config import settings
from app.services.pgvector_store import _parse_vector, _serialize_vector
from app.services.pgvector_store import PgVectorStore
from app.services import index_service, search_service


class PrecisionSearchTests(unittest.TestCase):
    def test_understand_query_extracts_time_people_and_topic(self) -> None:
        query = "\u5f20\u4e09\u4e0a\u5468\u5173\u4e8e\u9879\u76ee A \u7684\u62a5\u4ef7"
        understood = search_service.understand_query(query)
        self.assertEqual(understood["query"], query)
        self.assertIn("\u5f20\u4e09", understood["people"])
        self.assertIn("A", understood["topics"])
        self.assertTrue(understood["start_at"])
        self.assertTrue(understood["end_at"])

    def test_rrf_deduplicates_by_chunk_id(self) -> None:
        fused = search_service._rrf(
            [{"chunk_id": "c1", "rank": 1, "score": 12, "retrieval": "bm25"}],
            [{"chunk_id": "c1", "rank": 1, "score": 0.9, "retrieval": "knn"}, {"chunk_id": "c2", "rank": 2, "score": 0.8}],
        )
        self.assertEqual([item["chunk_id"] for item in fused], ["c1", "c2"])
        self.assertEqual(len({item["chunk_id"] for item in fused}), len(fused))
        self.assertGreater(fused[0]["rrf_score"], fused[1]["rrf_score"])

    def test_rerank_not_configured_is_explicit_without_degrading(self) -> None:
        original = (settings.rerank_enabled, settings.rerank_api_base_url, settings.rerank_api_key, settings.rerank_model)
        settings.rerank_enabled = True
        settings.rerank_api_base_url = ""
        settings.rerank_api_key = ""
        settings.rerank_model = ""
        try:
            items, degraded, reason = search_service._rerank("报价", [{"content": "项目 A 报价", "chunk_id": "c1"}])
        finally:
            settings.rerank_enabled, settings.rerank_api_base_url, settings.rerank_api_key, settings.rerank_model = original
        self.assertEqual([item["chunk_id"] for item in items], ["c1"])
        self.assertFalse(degraded)
        self.assertEqual(reason, "rerank_not_configured")

    def test_answer_without_qa_model_returns_citations_but_no_fake_answer(self) -> None:
        original = (settings.qa_api_base_url, settings.qa_api_key)
        settings.qa_api_base_url = ""
        settings.qa_api_key = ""
        try:
            with patch.object(search_service, "search", return_value={
                "query": "项目 A 报价",
                "items": [{"chunk_id": "c1", "content": "张三确认项目 A 报价", "platform": "wechat", "conversation_id": 9}],
                "total": 1,
                "degraded": False,
                "timings": {},
            }):
                result = search_service.answer({"query": "项目 A 报价"})
        finally:
            settings.qa_api_base_url, settings.qa_api_key = original
        self.assertEqual(result["answer"], "")
        self.assertTrue(result["qa_degraded"])
        self.assertEqual(result["qa_error"], "qa_model_not_configured")
        self.assertEqual(result["citations"][0]["chunk_id"], "c1")

    def test_answer_without_sources_returns_explicit_error(self) -> None:
        original = (settings.qa_api_base_url, settings.qa_api_key)
        settings.qa_api_base_url = ""
        settings.qa_api_key = ""
        try:
            with patch.object(search_service, "search", return_value={
                "query": "项目 A 报价",
                "items": [],
                "total": 0,
                "degraded": False,
                "timings": {},
            }):
                result = search_service.answer({"query": "项目 A 报价"})
        finally:
            settings.qa_api_base_url, settings.qa_api_key = original
        self.assertEqual(result["answer"], "")
        self.assertTrue(result["qa_degraded"])
        self.assertEqual(result["qa_error"], "no_sources")
        self.assertEqual(result["error_message"], "未检索到可用来源，请调整问题、平台范围或会话范围。")

    def test_explicit_constraints_match_sender_and_topic(self) -> None:
        understood = search_service.understand_query("\u5f20\u4e09\u4e0a\u5468\u5173\u4e8e\u9879\u76ee A \u7684\u62a5\u4ef7")
        item = {"content": "项目 A 报价已确认", "sender_name": "\u5f20\u4e09", "conversation_name": "后端研发群"}
        self.assertTrue(search_service._matches_explicit_constraints(item, understood))

    def test_explicit_constraints_reject_missing_sender(self) -> None:
        understood = search_service.understand_query("\u5f20\u4e09\u4e0a\u5468\u5173\u4e8e\u9879\u76ee A \u7684\u62a5\u4ef7")
        item = {"content": "项目 A 报价已确认", "sender_name": "\u674e\u56db", "conversation_name": "后端研发群"}
        self.assertFalse(search_service._matches_explicit_constraints(item, understood))

    def test_explicit_constraints_reject_missing_topic(self) -> None:
        understood = search_service.understand_query("\u5f20\u4e09\u4e0a\u5468\u5173\u4e8e\u9879\u76ee A \u7684\u62a5\u4ef7")
        item = {"content": "别的方案", "sender_name": "\u5f20\u4e09", "conversation_name": "后端研发群"}
        self.assertFalse(search_service._matches_explicit_constraints(item, understood))


class FakeIndices:
    def __init__(self, exists: bool = False, dims: int = 1536):
        self._exists = exists
        self._dims = dims
        self.created_body = None

    def exists(self, index: str) -> bool:
        return self._exists

    def create(self, index: str, body: dict) -> None:
        self.created_body = body
        self._exists = True

    def get_mapping(self, index: str) -> dict:
        return {index: {"mappings": {"properties": {"embedding": {"dims": self._dims}}}}}


class FakeES:
    def __init__(self, exists: bool = False, dims: int = 1536):
        self.indices = FakeIndices(exists=exists, dims=dims)


class IndexServiceTests(unittest.TestCase):
    def test_ensure_index_creates_mapping_with_runtime_dimension(self) -> None:
        original = settings.embedding_dimension
        settings.embedding_dimension = 7
        try:
            fake = FakeES(exists=False)
            index_service.ensure_index(fake)  # type: ignore[arg-type]
        finally:
            settings.embedding_dimension = original
        self.assertEqual(fake.indices.created_body["mappings"]["properties"]["embedding"]["dims"], 7)

    def test_pgvector_literal_roundtrip(self) -> None:
        vector = [0.125, 2.5, -3.75]
        literal = _serialize_vector(vector)
        parsed = _parse_vector(literal)
        self.assertEqual(len(parsed), len(vector))
        for got, want in zip(parsed, vector, strict=True):
            self.assertAlmostEqual(got, want)

    def test_ensure_index_rejects_dimension_mismatch(self) -> None:
        original = settings.embedding_dimension
        settings.embedding_dimension = 1536
        try:
            with self.assertRaises(RuntimeError):
                index_service.ensure_index(FakeES(exists=True, dims=768))  # type: ignore[arg-type]
        finally:
            settings.embedding_dimension = original


class _FakeQuery:
    def __init__(self, row):
        self._row = row

    def fetchone(self):
        return self._row


class _FakeConn:
    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def execute(self, sql, params=()):
        self.calls.append((sql, params))
        for needle, row in self.responses:
            if needle in sql:
                return _FakeQuery(row(params) if callable(row) else row)
        raise AssertionError(f"unexpected SQL: {sql}")


class _FakeStore(PgVectorStore):
    def __init__(self, responses):
        self._conn = _FakeConn(responses)

    def connect(self):  # type: ignore[override]
        return self._conn


class ResolveSourceTests(unittest.TestCase):
    def test_resolve_source_prefers_raw_message_id(self) -> None:
        store = _FakeStore([
            ("FROM ingestion.raw_messages WHERE id=%s", (17,)),
        ])
        account_id, raw_id = store.resolve_source({"raw_message_id": "55", "source": "wechat"})
        self.assertEqual((account_id, raw_id), (17, 55))

    def test_resolve_source_falls_back_to_platform_and_source_message_id(self) -> None:
        store = _FakeStore([
            ("FROM ingestion.source_accounts", (23,)),
            ("FROM ingestion.raw_messages", (99,)),
        ])
        account_id, raw_id = store.resolve_source(
            {"source": "wechat", "source_account_id": "wechat-account", "source_message_id": "chatroom:42"}
        )
        self.assertEqual((account_id, raw_id), (23, 99))


if __name__ == "__main__":
    unittest.main()
