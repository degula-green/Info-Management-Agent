import unittest
from unittest.mock import patch

from app.services.search_service import AnswerGenerator, HybridSearch, QueryRewriter, RerankClient, SearchResult, SearchScope, deduplicate, filter_relevant_results, scope_filter, weighted_rrf
from app.services.index_service import ensure_index


def result(chunk, document=1):
    return SearchResult(chunk, document, None, None, None, chunk)


class SearchCoreTests(unittest.TestCase):
    def test_scope_filter_contains_all_security_constraints(self):
        filters = scope_filter(SearchScope(7, (11, 12), (31,)), platforms=("feishu",))
        self.assertIn({"term": {"user_id": 7}}, filters)
        self.assertIn({"term": {"is_deleted": False}}, filters)
        self.assertIn({"term": {"document_status": "completed"}}, filters)
        self.assertIn({"terms": {"source_account_id": [11, 12]}}, filters)
        self.assertIn({"terms": {"conversation_id": [31]}}, filters)

    def test_empty_account_scope_fails_closed(self):
        filters = scope_filter(SearchScope(7))
        self.assertIn({"term": {"source_account_id": -1}}, filters)

    def test_rrf_merges_duplicate_chunks_and_prefers_cross_lane_hit(self):
        merged = weighted_rrf([result("a"), result("b")], [result("b"), result("c")])
        self.assertEqual([item.chunk_id for item in merged], ["b", "a", "c"])
        self.assertEqual([item.rank for item in merged], [1, 2, 3])

    def test_document_deduplication_limits_chunk_count(self):
        values = deduplicate([result("a", 1), result("b", 1), result("c", 1), result("d", 1), result("e", 2)])
        self.assertEqual([item.chunk_id for item in values], ["a", "b", "c", "e"])

    def test_hybrid_search_applies_same_scope_to_bm25_and_knn_and_falls_back(self):
        class FakeES:
            def __init__(self): self.bodies = []
            def search(self, **kwargs):
                self.bodies.append(kwargs["body"])
                return {"hits": {"hits": [{"_id": "a", "_score": 1, "_source": {"chunk_id": "a", "document_id": 1, "content": "x"}}]}}
        class BrokenEmbedder:
            def embed(self, texts): raise RuntimeError("offline")
        es = FakeES()
        output = HybridSearch(es=es, embedder=BrokenEmbedder(), reranker=lambda *_: (_ for _ in ()).throw(RuntimeError("down"))).search("q", SearchScope(7, (11,), (31,)))
        self.assertEqual(len(output), 1)
        self.assertEqual(len(es.bodies), 1)  # kNN is skipped when embedding is unavailable
        self.assertEqual(es.bodies[0]["query"]["bool"]["filter"], scope_filter(SearchScope(7, (11,), (31,))))

    def test_es_index_bootstrap_is_idempotent(self):
        class Indices:
            def __init__(self): self.created = False; self.calls = []
            def exists(self, **kwargs): return self.created
            def create(self, **kwargs): self.created = True; self.calls.append("create")
            def put_mapping(self, **kwargs): self.calls.append("mapping")
        class FakeES:
            def __init__(self): self.indices = Indices()
        es = FakeES(); ensure_index(es); ensure_index(es)
        self.assertEqual(es.indices.calls, ["create", "mapping"])

    def test_answer_generator_falls_back_without_model_configuration(self):
        item = result("fallback")
        self.assertEqual(AnswerGenerator(base_url="", api_key="").generate("q", [item]), "知识库中未查找到与该问题直接相关的信息。")

    def test_relevance_gate_rejects_unrelated_long_question(self):
        self.assertEqual(filter_relevant_results("公司的食堂今天有什么菜", [result("系统记录消息来源")]), [])

    def test_relevance_gate_keeps_matching_question(self):
        self.assertEqual(len(filter_relevant_results("如何记录消息来源", [result("系统记录消息来源") ])), 1)

    def test_relevance_gate_keeps_single_topic_entity_match(self):
        self.assertEqual(len(filter_relevant_results("李老板有说什么", [result("李老板说明天下午三点开会") ])), 1)
        self.assertEqual(len(filter_relevant_results("关于会议有什么我遗漏的吗", [result("会议安排和参会人员") ])), 1)

    def test_answer_generator_identifies_explicit_no_answer(self):
        self.assertTrue(AnswerGenerator.is_no_answer("知识库中未查找到与该问题直接相关的信息。"))

    def test_answer_generator_uses_openai_compatible_response(self):
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self): return b'{"choices":[{"message":{"content":"generated"}}]}'
        def opener(request, timeout):
            self.assertEqual(request.full_url, "https://model.test/chat/completions")
            self.assertEqual(timeout, 45)
            return Response()
        item = result("fallback")
        with patch("app.services.search_service.urllib.request.urlopen", opener):
            answer = AnswerGenerator(base_url="https://model.test", api_key="secret").generate("q", [item])
        self.assertEqual(answer, "generated")

    def test_rerank_client_maps_provider_scores_by_index(self):
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self): return b'{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.2}]}'
        def opener(request, timeout):
            self.assertEqual(request.full_url, "https://model.test/rerank")
            self.assertEqual(timeout, 30)
            return Response()
        with patch("app.services.search_service.urllib.request.urlopen", opener):
            scores = RerankClient(base_url="https://model.test", api_key="secret").rerank("q", [result("a"), result("b")])
        self.assertEqual(scores, [0.2, 0.9])

    def test_query_rewriter_limits_and_deduplicates_rewrites(self):
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self): return b'{"choices":[{"message":{"content":"[\\"a\\",\\"a\\",\\"b\\",\\"c\\",\\"d\\"]"}}]}'
        with patch("app.services.search_service.urllib.request.urlopen", lambda *args, **kwargs: Response()):
            values = QueryRewriter(base_url="https://model.test", api_key="secret").rewrite("q")
        self.assertEqual(values, ["a", "b", "c"])


if __name__ == "__main__":
    unittest.main()
