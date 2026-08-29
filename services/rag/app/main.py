from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import Any

from app.config import settings
from app.routers import health, search
from app.services.index_service import index_chunks
from app.services.vectorization import EmbeddingClient, VectorizationPipeline
from app.services.pgvector_store import PgVectorStore
from app.services.worker import VectorizationWorker

app = FastAPI(title="info-agent-rag", version="0.1.0")
app.include_router(health.router)
app.include_router(search.router)
worker: VectorizationWorker | None = None


@app.on_event("startup")
def start_worker() -> None:
    global worker
    if settings.rag_database_url:
        worker = VectorizationWorker(PgVectorStore(), EmbeddingClient())
        worker.start()


@app.on_event("shutdown")
def stop_worker() -> None:
    if worker:
        worker.stop()


class IngestRequest(BaseModel):
    messages: list[dict[str, Any]]


@app.post("/ingest")
def ingest(request: IngestRequest) -> dict[str, int]:
    try:
        result = VectorizationPipeline(EmbeddingClient(), index_chunks).run(request.messages)
    except (RuntimeError, ValueError, KeyError) as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    return {"messages": result.messages, "chunks": result.chunks}
