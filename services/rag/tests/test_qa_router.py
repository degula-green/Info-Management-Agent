import json
import unittest
from datetime import datetime, timezone
from unittest.mock import patch

from fastapi.testclient import TestClient

from app.main import app
from app.routers.search import _event
from app.services.search_service import SearchResult


class FakeEngine:
    def __init__(self, results=None, error=None):
        self.stats = {"keyword_hits": 2, "vector_hits": 2, "fused_hits": 1, "reranked_hits": 1}
        self.results, self.error = results or [], error

    def search(self, *args, **kwargs):
        if self.error:
            raise RuntimeError(self.error)
        return self.results


def parse_events(body: str):
    events = []
    for frame in body.strip().split("\n\n"):
        lines = frame.splitlines()
        if len(lines) >= 2:
            events.append((lines[0].removeprefix("event: "), json.loads(lines[1].removeprefix("data: "))))
    return events


class QaRouterTests(unittest.TestCase):
    def test_sse_event_serializes_datetime_from_pg_context(self):
        payload = json.loads(_event("citation", {"sent_at": datetime(2026, 8, 31, tzinfo=timezone.utc)}).splitlines()[1][6:])
        self.assertIn("2026-08-31", payload["sent_at"])

    def test_empty_result_returns_fixed_prompt_and_empty_citations(self):
        with patch("app.routers.search.HybridSearch", return_value=FakeEngine()):
            response = TestClient(app).post("/qa/ask", json={"question": "unknown", "scope": {"user_id": 7}})
        self.assertEqual(response.status_code, 200)
        events = parse_events(response.text)
        self.assertEqual([name for name, _ in events], ["meta", "delta", "done"])
        self.assertEqual(events[1][1]["text"], "知识库中未查找到与该问题直接相关的信息。")
        self.assertEqual(events[-1][1]["citations"], [])

    def test_result_emits_citation_and_real_stats(self):
        item = SearchResult("c1", 9, 4, "m1", 3, "evidence", source="feishu", file_name="a.docx")
        with patch("app.routers.search.HybridSearch", return_value=FakeEngine([item])):
            response = TestClient(app).post("/qa/ask", json={"question": "q", "scope": {"user_id": 7, "source_account_ids": [2]}})
        events = parse_events(response.text)
        self.assertEqual([name for name, _ in events], ["meta", "delta", "citation", "done"])
        self.assertEqual(events[2][1]["attachment_id"], 4)
        self.assertEqual(events[-1][1]["retrieval"]["keyword_hits"], 2)

    def test_model_qualified_answer_keeps_retrieved_citations(self):
        item = SearchResult("c1", 9, None, "m1", 3, "无关内容", source="feishu")
        with patch("app.routers.search.HybridSearch", return_value=FakeEngine([item])):
            with patch("app.routers.search.AnswerGenerator.generate", return_value="知识库中未查找到与该问题直接相关的信息。"):
                response = TestClient(app).post("/qa/ask", json={"question": "unknown", "scope": {"user_id": 7}})
        events = parse_events(response.text)
        self.assertEqual([name for name, _ in events], ["meta", "delta", "citation", "done"])
        self.assertEqual(len(events[-1][1]["citations"]), 1)

    def test_engine_failure_still_closes_stream(self):
        with patch("app.routers.search.HybridSearch", return_value=FakeEngine(error="offline")):
            response = TestClient(app).post("/qa/ask", json={"question": "q", "scope": {"user_id": 7}})
        events = parse_events(response.text)
        self.assertEqual([name for name, _ in events], ["meta", "error", "done"])
        self.assertEqual(events[-1][1]["citations"], [])


if __name__ == "__main__":
    unittest.main()
