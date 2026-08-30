from __future__ import annotations

import json
import logging
import os
import urllib.error
import urllib.request
from typing import Any


logger = logging.getLogger(__name__)


class MessageValueClient:
    """Call the internal RAG evaluator without making collection depend on it."""

    def __init__(self, endpoint: str | None = None, timeout: float | None = None):
        self.endpoint = (endpoint if endpoint is not None else os.getenv(
            "MESSAGE_VALUE_EVALUATOR_URL", "http://127.0.0.1:8000/evaluate/message"
        )).strip()
        if timeout is not None:
            self.timeout = timeout
        else:
            try:
                self.timeout = float(os.getenv("MESSAGE_VALUE_TIMEOUT_SECONDS", "10"))
            except (TypeError, ValueError):
                self.timeout = 10.0
        if self.timeout <= 0:
            self.timeout = 10.0

    def is_valuable(self, source: str, message: dict[str, Any]) -> bool:
        if not self.endpoint or not self._is_group_message(source, message):
            return True
        payload = json.dumps({"source": source, "message": message}, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(
            self.endpoint,
            data=payload,
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                if response.status < 200 or response.status >= 300:
                    raise RuntimeError(f"HTTP {response.status}")
                result = json.loads(response.read().decode("utf-8"))
            valuable = result.get("valuable") if isinstance(result, dict) else None
            if not isinstance(valuable, bool):
                raise ValueError("response does not contain a boolean valuable field")
            return valuable
        except Exception as exc:
            # Fail open: the existing collector/storage path remains available if
            # the optional semantic service is down or returns malformed data.
            logger.warning("message value evaluator unavailable; message will be stored (%s)", type(exc).__name__)
            return True

    @staticmethod
    def _is_group_message(source: str, message: dict[str, Any]) -> bool:
        chat_id = str(message.get("chat_id") or "").lower()
        source = str(source).lower()
        if source == "wechat":
            return chat_id.endswith("@chatroom")
        if source == "feishu":
            for key in ("chat_type", "chat_mode", "conversation_type"):
                chat_type = str(message.get(key) or "").strip().lower()
                if chat_type in {"p2p", "direct", "private", "single"}:
                    return False
                if chat_type in {"group", "group_chat"}:
                    return True
            return chat_id.startswith("oc_")
        return False
