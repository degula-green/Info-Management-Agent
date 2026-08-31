import json
import uuid

from fastapi import APIRouter, Query
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field

from app.services.search_service import AnswerGenerator, HybridSearch, QueryRewriter, SearchScope, search as run_search
from app.config import settings
from app.services.pgvector_store import PgVectorStore

router = APIRouter(prefix="/search", tags=["search"])
qa_router = APIRouter(prefix="/qa", tags=["qa"])


@router.get("")
def search(q: str = Query(default="")) -> dict[str, object]:
    result = run_search(q)
    result["message"] = "RAG search endpoint is ready"
    return result


class ScopeBody(BaseModel):
    user_id: int
    source_account_ids: list[int] = Field(default_factory=list)
    conversation_ids: list[int] = Field(default_factory=list)


class AskRequest(BaseModel):
    question: str = Field(min_length=1, max_length=4000)
    scope: ScopeBody
    platforms: list[str] = Field(default_factory=list)
    conversation_ids: list[int] | None = None
    top_k: int = Field(default=8, ge=1, le=10)
    query_rewrites: list[str] = Field(default_factory=list, max_length=3)


def _event(name: str, payload: dict) -> str:
    return f"event: {name}\ndata: {json.dumps(payload, ensure_ascii=False, default=str)}\n\n"


@qa_router.post("/ask")
def ask(request: AskRequest) -> StreamingResponse:
    request_id = uuid.uuid4().hex
    scope = SearchScope(request.scope.user_id, tuple(request.scope.source_account_ids),
                        tuple(request.conversation_ids if request.conversation_ids is not None else request.scope.conversation_ids))
    store = None
    if settings.rag_database_url:
        try: store = PgVectorStore()
        except RuntimeError: store = None

    def stream():
        yield _event("meta", {"request_id": request_id, "mode": "hybrid"})
        try:
            engine = HybridSearch()
            rewrites = request.query_rewrites or QueryRewriter().rewrite(request.question)
            results = engine.search(request.question, scope, top_k=request.top_k,
                                    platforms=request.platforms, rewrites=rewrites)
        except Exception as exc:
            yield _event("error", {"request_id": request_id, "code": "retrieval_failed", "message": str(exc)[:200]})
            yield _event("done", {"citations": [], "retrieval": {"keyword_hits": 0, "vector_hits": 0, "fused_hits": 0, "reranked_hits": 0, "latency_ms": 0}})
            return
        citations = []
        if not results:
            yield _event("delta", {"text": "知识库中未查找到与该问题直接相关的信息。"})
        else:
            answer = AnswerGenerator().generate(request.question, results)
            yield _event("delta", {"text": answer})
            # Results have already passed the retrieval relevance gate, so keep
            # their source cards even when the model qualifies an incomplete
            # answer. The user can inspect the evidence behind that qualifier.
            for index, item in enumerate(results, 1):
                citation_id = f"cite-{index}"
                citations.append(citation_id)
                citation = {"citation_id": citation_id, "type": "document" if item.attachment_id else "message",
                    "platform": item.source, "file_name": item.file_name, "snippet": item.content,
                    "chunk_id": item.chunk_id, "document_id": item.document_id, "attachment_id": item.attachment_id,
                    "message_id": item.message_id, "conversation_id": item.conversation_id,
                    "source_position": item.source_position}
                if store:
                    try:
                        context = store.citation_context(document_id=item.document_id if item.attachment_id else None, attachment_id=item.attachment_id,
                                                         message_id=item.message_id if not item.attachment_id else None,
                                                         user_id=scope.user_id)
                        if context: citation.update({k: v for k, v in context.items() if v is not None})
                    except Exception:
                        pass
                yield _event("citation", citation)
        yield _event("done", {"citations": citations, "retrieval": engine.stats})

    return StreamingResponse(stream(), media_type="text/event-stream", headers={"Cache-Control": "no-cache", "X-Request-ID": request_id})
