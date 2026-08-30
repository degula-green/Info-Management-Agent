from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import threading
import time
from pathlib import Path
from typing import Any
from wechatauto import MediaDownloader

from services.collectors.wechat.attachments import MAX_ATTACHMENT_BYTES, SUPPORTED_EXTENSIONS


class WeChatAttachmentDownloadWorker:
    def __init__(self, repo: Any, db: Any, binding: dict[str, Any], data_dir: str, interval: float = 5, batch_size: int = 4, client: Any = None, media_downloader: Any = None):
        self.repo, self.db, self.binding = repo, db, binding
        self.root = Path(data_dir).resolve() / "tmp" / "wechat-attachments"
        self.root.mkdir(parents=True, exist_ok=True)
        self.interval, self.batch_size = interval, batch_size
        self.stop_event = threading.Event()
        self.thread: threading.Thread | None = None
        self.worker_id = f"wechat-download-{os.getpid()}-{id(self)}"
        self.client = client or self._client()
        self.media = media_downloader or MediaDownloader(db)
        self._last_cleanup = 0.0

    def _client(self) -> Any:
        from minio import Minio
        endpoint = os.getenv("CORE_MINIO_ENDPOINT", "127.0.0.1:9000").strip()
        secure = endpoint.startswith("https://")
        endpoint = re.sub(r"^https?://", "", endpoint).rstrip("/")
        access = os.getenv("CORE_MINIO_ACCESS_KEY", "")
        secret = os.getenv("CORE_MINIO_SECRET_KEY", "")
        if not access or not secret:
            raise RuntimeError("CORE_MINIO_ACCESS_KEY and CORE_MINIO_SECRET_KEY are required")
        return Minio(endpoint, access_key=access, secret_key=secret, secure=secure)

    def start(self) -> None:
        self.thread = threading.Thread(target=self.run, name="wechat-attachment-download", daemon=True)
        self.thread.start()

    def stop(self) -> None:
        self.stop_event.set()
        if self.thread:
            self.thread.join(timeout=10)

    def run(self) -> None:
        while not self.stop_event.is_set():
            did_work = False
            try:
                if time.time() - self._last_cleanup >= 24 * 60 * 60:
                    self.cleanup_dead_temp_files()
                    self._last_cleanup = time.time()
                for task in self.repo.claim_attachment_download_tasks(self.worker_id, self.batch_size):
                    did_work = True
                    self.process(task)
            except Exception:
                pass
            if not did_work:
                self.stop_event.wait(self.interval)

    @staticmethod
    def _object_name(user_id: str, conversation_id: str, message_id: str, attachment_id: int, name: str) -> str:
        clean = lambda value: re.sub(r"[^A-Za-z0-9@._-]+", "_", str(value)).strip("._") or "unknown"
        safe = re.sub(r"[^A-Za-z0-9._-]+", "_", Path(name).name).strip("._") or "attachment"
        return f"{clean(user_id)}/wechat/{clean(conversation_id)}/{clean(message_id)}/{attachment_id}-{safe}"

    def process(self, task: dict[str, Any]) -> None:
        payload = task["payload"]
        attachment_id = int(payload["attachment_id"])
        temp_dir = self.root / str(attachment_id)
        temp_dir.mkdir(parents=True, exist_ok=True)
        payload_path = temp_dir / "payload.bin"
        state_path = temp_dir / "state.json"
        try:
            context = self.repo.get_attachment_context(attachment_id)
            if not payload_path.exists():
                local_id = int(str(context["source_message_id"]).rsplit(":", 1)[-1])
                result = self.media.download_file(context["chat_id"], local_id, str(temp_dir))
                if not result:
                    # MediaDownloader may resolve the message row from only
                    # the first encrypted shard. The parser already persisted
                    # a trusted filename, so search the local file store as a
                    # shard-independent fallback.
                    account_dir = Path(getattr(self.db, "account_dir", ""))
                    expected = Path(str(context.get("file_name") or "")).name
                    if expected and account_dir.is_dir():
                        for candidate in (account_dir / "msg" / "file").rglob(expected):
                            if candidate.is_file():
                                shutil.copyfile(candidate, payload_path)
                                result = str(payload_path)
                                break
                if not result or not Path(result).is_file():
                    raise FileNotFoundError("WeChat local file was not found")
                if Path(result) != payload_path:
                    shutil.copyfile(result, payload_path)
                    Path(result).unlink(missing_ok=True)
            size = payload_path.stat().st_size
            if size > MAX_ATTACHMENT_BYTES:
                raise ValueError(f"attachment exceeds {MAX_ATTACHMENT_BYTES} byte limit")
            ext = str(context.get("extension") or "").lower()
            if ext not in SUPPORTED_EXTENSIONS:
                raise ValueError(f"unsupported attachment extension: {ext or 'unknown'}")
            digest = hashlib.sha256()
            with payload_path.open("rb") as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    digest.update(chunk)
            storage_key = self._object_name(context["user_id"], context["conversation_id"], context["message_id"], attachment_id, context["file_name"])
            bucket = os.getenv("CORE_MINIO_BUCKET", "info-agent")
            if not self.client.bucket_exists(bucket):
                self.client.make_bucket(bucket)
            self.client.fput_object(bucket, storage_key, str(payload_path), content_type=context.get("mime_type") or "application/octet-stream")
            self.repo.complete_attachment_download(attachment_id, int(task["task_id"]), size, digest.hexdigest(), bucket, storage_key)
            shutil.rmtree(temp_dir, ignore_errors=True)
        except Exception as exc:
            state_path.write_text(json.dumps({"attachment_id": attachment_id, "path": str(payload_path), "error": str(exc)}, ensure_ascii=False, indent=2), encoding="utf-8")
            self.repo.fail_attachment_download(attachment_id, int(task["task_id"]), str(exc))

    def cleanup_dead_temp_files(self) -> None:
        cutoff = time.time() - 30 * 24 * 60 * 60
        dead_ids = self.repo.dead_attachment_download_ids()
        for item in self.root.iterdir():
            if not item.is_dir() or not item.name.isdigit() or int(item.name) not in dead_ids:
                continue
            if item.stat().st_mtime < cutoff:
                shutil.rmtree(item, ignore_errors=True)
