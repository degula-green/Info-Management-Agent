"""Message cleaning, chunking and embedding/indexing primitives."""
from __future__ import annotations

import hashlib
import json
import urllib.request
from dataclasses import dataclass
from typing import Any, Callable, Iterable

from app.config import settings


def canonical_platform(value: Any) -> str:
    """Return the canonical platform key used by PG, ES, and API payloads."""
    text = str(value or "").strip().lower().replace("-", "_")
    if text in {"wechat", "personal_wechat", "personalwechat"}:
        return "wechat"
    if text in {"wecom", "work_wechat", "enterprise_wechat"}:
        return "wecom"
    if text == "feishu":
        return "feishu"
    return ""


def clean_text(value: Any) -> str:
    return " ".join(str(value or "").split())


def chunk_text(text: str, max_chars: int | None = None, overlap: int | None = None) -> list[str]:
    text = clean_text(text)
    limit = max_chars or settings.max_chars_per_chunk
    overlap = settings.chunk_overlap_chars if overlap is None else overlap
    if not text:
        return []
    if limit <= 0 or overlap < 0 or overlap >= limit:
        raise ValueError("chunk limit must be positive and overlap must be smaller than limit")
    step = limit - overlap
    chunks = []
    position = 0
    while position < len(text):
        end = min(position + limit, len(text))
        chunks.append(text[position:end])
        if end == len(text):
            break
        position += step
    return chunks


def message_chunks(message: dict[str, Any]) -> list[dict[str, Any]]:
    chunks = chunk_text(message.get("text", ""))
    message_id = str(message.get("id") or message.get("source_message_id") or "")
    result = []
    for position, content in enumerate(chunks):
        chunk_id = hashlib.sha256(f"{message_id}:{settings.processor_version}:{position}".encode()).hexdigest()[:32]
        result.append({
            "chunk_id": chunk_id,
            "message_id": message_id,
            "content": content,
            "chunk_position": position,
            "source": message.get("source"),
            "source_account_id": message.get("source_account_id"),
            "external_account_id": message.get("external_account_id"),
            "source_message_id": message.get("source_message_id"),
            "conversation_id": message.get("conversation_id"),
            "external_conversation_id": message.get("external_conversation_id"),
            "sender_id": message.get("sender_id"),
            "external_sender_id": message.get("external_sender_id"),
            "user_id": message.get("user_id"),
            "occurred_at": message.get("occurred_at"),
            "message_type": message.get("message_type"),
            "conversation_name": message.get("conversation_name") or (message.get("metadata") or {}).get("conversation_name"),
            "metadata": message.get("metadata") or {},
        })
    return result


def document_chunks(document: dict[str, Any]) -> list[dict[str, Any]]:
    """Keep Markdown sections intact where possible before applying size limits."""
    text = str(document.get("content") or "")
    sections: list[tuple[str, str]] = []
    title, lines = "", []
    for line in text.splitlines():
        if line.startswith("#"):
            if lines:
                sections.append((title, "\n".join(lines)))
            title, lines = line.lstrip("# ").strip(), [line]
        else:
            lines.append(line)
    if lines:
        sections.append((title, "\n".join(lines)))
    parts: list[tuple[str, str]] = []
    for heading, section in sections:
        for part in chunk_text(section):
            parts.append((heading, part))
    document_id = str(document["document_id"])
    result = []
    for position, (heading, content) in enumerate(parts):
        chunk_id = hashlib.sha256(f"{document_id}:{settings.processor_version}:{position}".encode()).hexdigest()[:32]
        result.append({
            "chunk_id": chunk_id, "document_id": int(document["document_id"]),
            "message_id": str(document["message_id"]), "content": content,
            "chunk_position": position, "source": document["source"],
            "source_account_id": document["source_account_id"],
            "external_account_id": document.get("external_account_id"),
            "source_message_id": document.get("source_message_id"),
            "conversation_id": document.get("conversation_id"),
            "external_conversation_id": document.get("external_conversation_id"),
            "sender_id": document.get("sender_id"),
            "external_sender_id": document.get("external_sender_id"),
            "user_id": document["user_id"], "occurred_at": document.get("occurred_at"),
            "message_type": "document", "file_id": str(document["attachment_id"]),
            "attachment_id": int(document["attachment_id"]),
            "file_name": document.get("file_name") or (document.get("metadata") or {}).get("file_name"),
            "document_title": document.get("document_title") or heading,
            "conversation_name": document.get("conversation_name") or (document.get("metadata") or {}).get("conversation_name"),
            "source_type": "attachment", "source_position": heading or str(position),
            "metadata": {**(document.get("metadata") or {}), "heading_path": [heading] if heading else []},
        })
    return result


class EmbeddingClient:
    def __init__(self, base_url: str | None = None, api_key: str | None = None, model: str | None = None):
        self.base_url = (base_url or settings.embedding_api_base_url).rstrip("/")
        self.api_key = api_key if api_key is not None else settings.embedding_api_key
        self.model = model or settings.embedding_model

    def embed(self, texts: list[str]) -> list[list[float]]:
        if not self.base_url or not self.api_key:
            raise RuntimeError("Embedding API is not configured (EMBEDDING_API_BASE_URL and EMBEDDING_API_KEY required)")
        # DashScope text-embedding-v4 accepts at most 10 inputs per request.
        # Keep batching here so both message and attachment pipelines obey the
        # provider limit regardless of their configured worker batch size.
        vectors: list[list[float]] = []
        for start in range(0, len(texts), 10):
            batch = texts[start : start + 10]
            payload = json.dumps({"model": self.model, "input": batch,
                                  "dimensions": settings.embedding_dimension}).encode()
            request = urllib.request.Request(
                f"{self.base_url}/embeddings", data=payload,
                headers={"Content-Type": "application/json", "Authorization": f"Bearer {self.api_key}"}, method="POST",
            )
            with urllib.request.urlopen(request, timeout=60) as response:
                body = json.loads(response.read().decode())
            vectors.extend(item["embedding"] for item in sorted(body["data"], key=lambda item: item["index"]))
        return vectors


@dataclass
class VectorizationResult:
    messages: int
    chunks: int


class VectorizationPipeline:
    def __init__(self, embedder: Any, indexer: Callable[[list[dict[str, Any]]], None], batch_size: int | None = None):
        self.embedder = embedder
        self.indexer = indexer
        self.batch_size = batch_size or settings.max_chunks_per_request

    def run(self, messages: Iterable[dict[str, Any]]) -> VectorizationResult:
        message_list = list(messages)
        all_chunks = [chunk for message in message_list for chunk in message_chunks(message)]
        for start in range(0, len(all_chunks), self.batch_size):
            batch = all_chunks[start : start + self.batch_size]
            vectors = self.embedder.embed([chunk["content"] for chunk in batch])
            if len(vectors) != len(batch):
                raise RuntimeError("Embedding API returned an unexpected number of vectors")
            self.indexer([{**chunk, "embedding": vector} for chunk, vector in zip(batch, vectors)])
        return VectorizationResult(messages=len(message_list), chunks=len(all_chunks))
