from __future__ import annotations

import io
import json
import re
from pathlib import Path
from typing import Any

from minio import Minio

from app.config import settings


class ObjectStore:
    """Minimal MinIO adapter shared by attachment parsing operations."""

    def __init__(self, client: Any | None = None, bucket: str | None = None):
        endpoint = settings.minio_endpoint.strip()
        if not endpoint and client is None:
            raise RuntimeError("CORE_MINIO_ENDPOINT is required for attachment parsing")
        secure = endpoint.startswith("https://")
        endpoint = re.sub(r"^https?://", "", endpoint).rstrip("/")
        self.bucket = bucket or settings.minio_bucket
        self.client = client or Minio(endpoint, access_key=settings.minio_access_key,
                                      secret_key=settings.minio_secret_key, secure=secure)

    def _ensure_bucket(self) -> None:
        if not self.client.bucket_exists(self.bucket):
            self.client.make_bucket(self.bucket)

    def download(self, key: str, path: Path, max_bytes: int, bucket: str | None = None) -> int:
        response = self.client.get_object(bucket or self.bucket, key)
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            size = 0
            with path.open("wb") as output:
                for block in response.stream(1024 * 1024):
                    size += len(block)
                    if size > max_bytes:
                        raise ValueError(f"attachment exceeds {max_bytes} byte limit")
                    output.write(block)
            return size
        finally:
            response.close()
            response.release_conn()

    def upload_file(self, key: str, path: Path, content_type: str) -> None:
        self._ensure_bucket()
        self.client.fput_object(self.bucket, key, str(path), content_type=content_type)

    def upload_json(self, key: str, value: dict[str, Any]) -> None:
        self._ensure_bucket()
        payload = json.dumps(value, ensure_ascii=False, sort_keys=True, default=str).encode("utf-8")
        self.client.put_object(self.bucket, key, io.BytesIO(payload), len(payload), content_type="application/json")


def safe_segment(value: object) -> str:
    result = re.sub(r"[^A-Za-z0-9._-]+", "_", str(value or "unknown")).strip("._")
    return result or "unknown"


def derived_prefix(context: dict[str, Any]) -> str:
    return "/".join((safe_segment(context["user_id"]), safe_segment(context["platform"]),
                     safe_segment(context["conversation_id"]), safe_segment(context["message_id"]),
                     "derived", safe_segment(context["attachment_id"]),
                     safe_segment(settings.attachment_parser_version)))
