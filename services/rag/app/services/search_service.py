from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Iterable
import json
import time
import urllib.request

from app.config import settings
from app.services.index_service import client as elasticsearch_client
from app.services.vectorization import EmbeddingClient, canonical_platform


@dataclass(frozen=True)
class SearchScope:
    user_id: int
    source_account_ids: tuple[int, ...] = ()
    conversation_ids: tuple[int, ...] = ()

@dataclass(frozen=True)
class SearchFilters:
    resource_types: tuple[str, ...] = ()
    sender_name: str = ""
    occurred_after: str | None = None
    occurred_before: str | None = None


@dataclass
class SearchResult:
    chunk_id: str
    document_id: int | None
    attachment_id: int | None
    message_id: str | None
    conversation_id: int | None
    content: str
    score: float = 0.0
    rank: int = 0
    rerank_score: float | None = None
    file_name: str | None = None
    source_position: str | None = None
    source: str | None = None
    conversation_name: str | None = None
    sender_name: str | None = None
    resource_type: str | None = None
    document_title: str | None = None
    occurred_at: str | None = None
    highlight: str | None = None
    metadata: dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_hit(cls, hit: dict[str, Any], rank: int = 0) -> "SearchResult":
        source = hit.get("_source", hit)
        metadata = source.get("metadata") or {}
        def integer(value: Any) -> int | None:
            try:
                return int(value) if value is not None else None
            except (TypeError, ValueError):
                return None
        def text(value: Any) -> str | None:
            return str(value) if value is not None and str(value) else None
        highlights = hit.get("highlight") or {}
        highlighted = next(iter(highlights.get("content", []) or []), None)
        return cls(str(source.get("chunk_id") or hit.get("_id") or ""), integer(source.get("document_id")),
                   integer(source.get("attachment_id") or source.get("file_id")),
                   text(source.get("message_id") or source.get("source_message_id")), integer(source.get("conversation_id")),
                   str(source.get("content") or ""), float(hit.get("_score") or 0.0), rank,
                   file_name=text(source.get("file_name") or metadata.get("file_name")),
                   source_position=text(source.get("source_position")), source=_canonical_platform(source.get("source") or source.get("platform")), metadata=metadata,
                   conversation_name=text(source.get("conversation_name")), sender_name=text(source.get("sender_name")),
                   resource_type=text(source.get("resource_type")), document_title=text(source.get("document_title")),
                   occurred_at=text(source.get("occurred_at")), highlight=text(highlighted))


def _canonical_platform(value: Any) -> str:
    return canonical_platform(value)


def _platform_storage_values(value: Any) -> tuple[str, ...]:
    canonical = _canonical_platform(value)
    return ("wechat", "personal_wechat") if canonical == "wechat" else ((canonical,) if canonical else ())


def scope_filter(scope: SearchScope, *, platforms: Iterable[str] = ()) -> list[dict[str, Any]]:
    filters: list[dict[str, Any]] = [{"term": {"user_id": scope.user_id}}, {"term": {"is_deleted": False}},
                                     {"term": {"document_status": "completed"}}]
    filters.append({"terms": {"source_account_id": list(scope.source_account_ids)}} if scope.source_account_ids
                   else {"term": {"source_account_id": -1}})
    if scope.conversation_ids:
        filters.append({"terms": {"conversation_id": list(scope.conversation_ids)}})
    platform_values: list[str] = []
    for platform in platforms:
        for value in _platform_storage_values(platform):
            if value not in platform_values:
                platform_values.append(value)
    if platform_values:
        # personal_wechat is queried only for legacy documents; all newly
        # indexed documents are normalized to the canonical wechat value.
        filters.append({"bool": {"should": [
            {"terms": {"source": platform_values}},
            {"terms": {"platform": platform_values}},
        ], "minimum_should_match": 1}})
    return filters


def weighted_rrf(keyword: Iterable[SearchResult], vector: Iterable[SearchResult], *, rrf_k: int = 60,
                 keyword_weight: float = 0.5, vector_weight: float = 0.5) -> list[SearchResult]:
    merged: dict[str, SearchResult] = {}; scores: dict[str, float] = {}
    for rank, item in enumerate(keyword, 1):
        merged.setdefault(item.chunk_id, item); scores[item.chunk_id] = scores.get(item.chunk_id, 0.0) + keyword_weight / (rrf_k + rank)
    for rank, item in enumerate(vector, 1):
        merged.setdefault(item.chunk_id, item); scores[item.chunk_id] = scores.get(item.chunk_id, 0.0) + vector_weight / (rrf_k + rank)
    result = sorted(merged.values(), key=lambda item: scores[item.chunk_id], reverse=True)
    for rank, item in enumerate(result, 1): item.score, item.rank = scores[item.chunk_id], rank
    return result


def deduplicate(results: Iterable[SearchResult], *, max_per_document: int = 3) -> list[SearchResult]:
    seen: set[str] = set(); counts: dict[int, int] = {}; output: list[SearchResult] = []
    for item in results:
        if not item.chunk_id or item.chunk_id in seen or (item.document_id is not None and counts.get(item.document_id, 0) >= max_per_document): continue
        seen.add(item.chunk_id); output.append(item)
        if item.document_id is not None: counts[item.document_id] = counts.get(item.document_id, 0) + 1
    return output


def _query_terms(query: str) -> set[str]:
    """Extract conservative matching terms for the no-reranker relevance gate."""
    import re
    stopwords = {"关于", "什么", "怎么", "如何", "有没有", "是否", "是不是", "可以", "能够", "我有", "有没", "说什", "有说", "的", "吗"}
    terms = {value.lower() for value in re.findall(r"[A-Za-z0-9][A-Za-z0-9_-]{1,}|[\u4e00-\u9fff]{2}", query)}
    terms.difference_update(stopwords)
    return terms


def filter_relevant_results(query: str, results: Iterable[SearchResult], *, reranker_used: bool = False) -> list[SearchResult]:
    values = list(results)
    if not values:
        return []
    if reranker_used:
        # Cross-encoder scores are provider-specific but scores below this level
        # are consistently weak for the configured reranker.
        return [item for item in values if item.rerank_score is not None and item.rerank_score >= 0.05]
    terms = _query_terms(query)
    if not terms:
        return values
    relevant: list[SearchResult] = []
    for item in values:
        content = item.content.lower()
        matches = sum(1 for term in terms if term in content)
        if matches >= 1:
            relevant.append(item)
    return relevant


class HybridSearch:
    def __init__(self, es: Any | None = None, embedder: Any | None = None,
                 reranker: Callable[[str, list[SearchResult]], list[float]] | None = None):
        self.es, self.embedder = es or elasticsearch_client(), embedder or EmbeddingClient()
        self.reranker = reranker
        if self.reranker is None and settings.rerank_api_base_url and settings.rerank_api_key:
            self.reranker = RerankClient().rerank
        self.stats: dict[str, int] = {"keyword_hits": 0, "vector_hits": 0, "fused_hits": 0, "reranked_hits": 0}

    def _bm25(self, query: str, scope: SearchScope, top_k: int, platforms: Iterable[str], filters: SearchFilters | None = None) -> list[SearchResult]:
        clauses = scope_filter(scope, platforms=platforms)
        filters = filters or SearchFilters()
        if filters.resource_types: clauses.append({"terms": {"resource_type": list(filters.resource_types)}})
        if filters.sender_name: clauses.append({"match": {"sender_name": filters.sender_name}})
        if filters.occurred_after or filters.occurred_before:
            rng = {}
            if filters.occurred_after: rng["gte"] = filters.occurred_after
            if filters.occurred_before: rng["lte"] = filters.occurred_before
            clauses.append({"range": {"occurred_at": rng}})
        body = {"size": top_k, "query": {"bool": {"filter": clauses, "must": {
            "multi_match": {"query": query, "fields": ["content^3", "document_title^2", "file_name^2", "conversation_name^2", "sender_name"]}},
            "should": [{"match_phrase": {"content": {"query": query, "boost": 2}}}] }},
            "highlight": {"fields": {"content": {}, "document_title": {}, "file_name": {}, "conversation_name": {}, "sender_name": {}}}}
        return [SearchResult.from_hit(h, i) for i, h in enumerate(self.es.search(index=settings.elasticsearch_index, body=body)["hits"]["hits"], 1)]

    def _knn(self, vector: list[float], scope: SearchScope, top_k: int, platforms: Iterable[str], filters: SearchFilters | None = None) -> list[SearchResult]:
        clauses = scope_filter(scope, platforms=platforms); filters = filters or SearchFilters()
        if filters.resource_types: clauses.append({"terms": {"resource_type": list(filters.resource_types)}})
        if filters.occurred_after or filters.occurred_before:
            rng = {}
            if filters.occurred_after: rng["gte"] = filters.occurred_after
            if filters.occurred_before: rng["lte"] = filters.occurred_before
            clauses.append({"range": {"occurred_at": rng}})
        body = {"knn": {"field": "embedding", "query_vector": vector, "k": top_k, "num_candidates": max(top_k * 2, 100),
                         "filter": {"bool": {"filter": clauses}}}, "size": top_k}
        return [SearchResult.from_hit(h, i) for i, h in enumerate(self.es.search(index=settings.elasticsearch_index, body=body)["hits"]["hits"], 1)]

    def search(self, query: str, scope: SearchScope, *, top_k: int = 8, platforms: Iterable[str] = (), rewrites: Iterable[str] = (), filters: SearchFilters | None = None) -> list[SearchResult]:
        started = time.perf_counter()
        keyword: list[SearchResult] = []; vector: list[SearchResult] = []
        for text in list(dict.fromkeys([query, *rewrites]))[:4]:
            keyword_hits = self._bm25(text, scope, 100, platforms, filters)
            keyword.extend(keyword_hits)
            self.stats["keyword_hits"] += len(keyword_hits)
            try:
                vector_hits = self._knn(self.embedder.embed([text])[0], scope, 100, platforms, filters)
                vector.extend(vector_hits)
                self.stats["vector_hits"] += len(vector_hits)
            except Exception: pass
        candidates = deduplicate(weighted_rrf(keyword, vector)[:50])
        self.stats["fused_hits"] = len(candidates)
        if self.reranker and candidates:
            try:
                scores = self.reranker(query, candidates)
                if len(scores) != len(candidates): raise ValueError("invalid reranker result")
                for item, score in zip(candidates, scores): item.rerank_score = float(score)
                candidates.sort(key=lambda item: item.rerank_score or 0.0, reverse=True)
            except Exception: pass
        reranker_used = bool(self.reranker and any(item.rerank_score is not None for item in candidates))
        final = filter_relevant_results(query, candidates, reranker_used=reranker_used)[:max(1, min(top_k, 10))]
        self.stats["reranked_hits"] = len(final)
        self.stats["latency_ms"] = int((time.perf_counter() - started) * 1000)
        return final


class RerankClient:
    def __init__(self, base_url: str | None = None, api_key: str | None = None, model: str | None = None):
        self.base_url = (base_url or settings.rerank_api_base_url).rstrip("/")
        self.api_key = api_key if api_key is not None else settings.rerank_api_key
        self.model = model or settings.rerank_model

    def rerank(self, query: str, candidates: list[SearchResult]) -> list[float]:
        payload = json.dumps({"model": self.model, "query": query,
                              "documents": [item.content for item in candidates], "top_n": len(candidates)}).encode()
        request = urllib.request.Request(f"{self.base_url}/rerank", data=payload,
            headers={"Content-Type": "application/json", "Authorization": f"Bearer {self.api_key}"}, method="POST")
        with urllib.request.urlopen(request, timeout=30) as response:
            body = json.loads(response.read().decode())
        scores = [0.0] * len(candidates)
        for item in body.get("results", []):
            index = int(item.get("index", -1))
            if 0 <= index < len(scores): scores[index] = float(item.get("relevance_score", item.get("score", 0.0)))
        return scores


class AnswerGenerator:
    """Optional OpenAI-compatible answer generation with extractive fallback."""
    NO_ANSWER = "知识库中未查找到与该问题直接相关的信息。"
    def __init__(self, base_url: str | None = None, api_key: str | None = None, model: str | None = None):
        self.base_url = (base_url or settings.qa_api_base_url).rstrip("/")
        self.api_key = api_key if api_key is not None else settings.qa_api_key
        self.model = model or settings.qa_model

    def generate(self, question: str, results: list[SearchResult]) -> str:
        fallback = self._extractive_fallback(question, results)
        if not results or not self.base_url or not self.api_key:
            return fallback
        context = "\n\n".join(f"[{i + 1}] {item.content}" for i, item in enumerate(results))
        payload = json.dumps({"model": self.model, "temperature": 0,
            "messages": [{"role": "system", "content": "你是知识库问答助手。只能依据给定资料回答问题，不得编造。请优先从资料中提取与问题相关的事实并做简洁概括；如果资料只有部分线索，要明确说明‘根据现有资料只能确认……’，不要因为缺少完整结论就直接说未找到。只有资料与问题完全无关时，才只输出：知识库中未查找到与该问题直接相关的信息。不要复制整段资料，不要输出与问题无关的内容；请用简洁中文回答，必要时使用Markdown标题或列表。"},
                         {"role": "user", "content": f"问题：{question}\n资料：\n{context}"}]}).encode()
        request = urllib.request.Request(f"{self.base_url}/chat/completions", data=payload,
            headers={"Content-Type": "application/json", "Authorization": f"Bearer {self.api_key}"}, method="POST")
        try:
            with urllib.request.urlopen(request, timeout=45) as response:
                body = json.loads(response.read().decode())
            text = body["choices"][0]["message"]["content"]
            return str(text).strip() or fallback
        except Exception:
            return fallback

    @staticmethod
    def _extractive_fallback(question: str, results: list[SearchResult]) -> str:
        terms = _query_terms(question)
        best: tuple[int, str] | None = None
        for item in results:
            for sentence in __import__("re").split(r"(?<=[。！？.!?])\s*|\n+", item.content):
                sentence = sentence.strip()
                if not sentence:
                    continue
                score = sum(1 for term in terms if term in sentence.lower())
                if score and (best is None or score > best[0]):
                    best = (score, sentence)
        if best:
            return best[1][:600]
        return AnswerGenerator.NO_ANSWER

    @classmethod
    def is_no_answer(cls, text: str) -> bool:
        normalized = "".join(str(text or "").split())
        return normalized == "".join(cls.NO_ANSWER.split())


class QueryRewriter:
    def __init__(self, base_url: str | None = None, api_key: str | None = None, model: str | None = None):
        self.base_url = (base_url or settings.qa_api_base_url).rstrip("/")
        self.api_key = api_key if api_key is not None else settings.qa_api_key
        self.model = model or settings.qa_model

    def rewrite(self, question: str) -> list[str]:
        if not self.base_url or not self.api_key:
            return []
        payload = json.dumps({"model": self.model, "temperature": 0,
            "messages": [{"role": "system", "content": "将用户问题改写为最多3个等价检索问题，只输出JSON数组字符串。"},
                         {"role": "user", "content": question}]}).encode()
        request = urllib.request.Request(f"{self.base_url}/chat/completions", data=payload,
            headers={"Content-Type": "application/json", "Authorization": f"Bearer {self.api_key}"}, method="POST")
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                body = json.loads(response.read().decode())
            raw = body["choices"][0]["message"]["content"]
            values = json.loads(raw)
            if not isinstance(values, list): return []
            cleaned = [str(value).strip() for value in values if str(value).strip() and str(value).strip() != question]
            return list(dict.fromkeys(cleaned))[:3]
        except Exception:
            return []


def search(query: str, scope: SearchScope | None = None) -> dict[str, object]:
    items = HybridSearch().search(query, scope or SearchScope(user_id=0))
    return {"query": query, "index": settings.elasticsearch_url, "items": [item.__dict__ for item in items]}
