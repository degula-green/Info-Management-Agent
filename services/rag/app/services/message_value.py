from __future__ import annotations

import json
import re
import urllib.request
from dataclasses import dataclass, field
from typing import Any

from app.config import settings


VALUE_SYSTEM_PROMPT = """你是信息管理 Agent 的消息价值判断器。
你的任务是判断一条群聊消息是否值得进入个人知识库。

判定为有价值（valuable=true）的消息包括：任务、截止时间、需求、决策、客户反馈或问题、项目进展或风险、技术/配置资料、联系人信息、文件/链接的业务说明，以及后续需要查证的事实。
判定为无价值（valuable=false）的消息包括：问候、表情、收到/好的等简单确认、无业务含义的闲聊、重复通知、纯系统噪声。
对于图片、文件、链接等无法仅凭正文判断但可能包含业务资料的消息，默认判定为有价值。

只输出 JSON，不要输出 Markdown 或其他文字：
{"valuable":true或false,"confidence":0到1之间的数字,"categories":["task"],"reason":"不超过40字的原因"}
"""


@dataclass(frozen=True)
class ValueDecision:
    valuable: bool
    evaluated: bool
    confidence: float | None = None
    categories: list[str] = field(default_factory=list)
    reason: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "valuable": self.valuable,
            "evaluated": self.evaluated,
            "confidence": self.confidence,
            "categories": self.categories,
            "reason": self.reason,
        }


class MessageValueEvaluator:
    """Call an OpenAI-compatible chat endpoint and fail open on uncertainty."""

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        model: str | None = None,
        timeout: float | None = None,
        enabled: bool | None = None,
    ):
        self.base_url = (base_url if base_url is not None else settings.message_value_api_base_url).rstrip("/")
        self.api_key = api_key if api_key is not None else settings.message_value_api_key
        self.model = model or settings.message_value_model
        self.timeout = timeout if timeout is not None else settings.message_value_timeout_seconds
        self.enabled = settings.message_value_enabled if enabled is None else enabled

    def evaluate(self, source: str, message: dict[str, Any]) -> ValueDecision:
        # Attachments without usable text are retained so a valuable file is not
        # discarded merely because its binary content is not in this request.
        if not self.enabled or not self.base_url or not self.api_key:
            return ValueDecision(True, False, reason="value evaluator is not configured")

        request_body = self._request_body(source, message)
        try:
            body = self._call_model(request_body)
            return self._parse_response(body)
        except Exception:
            # Filtering must never break collection. A later re-evaluation can be
            # added without changing the collector contract.
            return ValueDecision(True, False, reason="value evaluator unavailable")

    def _request_body(self, source: str, message: dict[str, Any]) -> dict[str, Any]:
        serialized = json.dumps(message, ensure_ascii=False, default=str)
        # Keep latency and prompt size bounded while preserving the beginning of
        # the platform payload where text and sender fields normally appear.
        serialized = serialized[:12000]
        user_prompt = f"来源平台：{source}\n消息 JSON：{serialized}"
        return {
            "model": self.model,
            "temperature": 0,
            "max_tokens": 256,
            "messages": [
                {"role": "system", "content": VALUE_SYSTEM_PROMPT},
                {"role": "user", "content": user_prompt},
            ],
        }

    def _call_model(self, payload: dict[str, Any]) -> dict[str, Any]:
        request = urllib.request.Request(
            f"{self.base_url}/chat/completions",
            data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                "Authorization": f"Bearer {self.api_key}",
            },
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=self.timeout) as response:
            if response.status < 200 or response.status >= 300:
                raise RuntimeError(f"value evaluator returned HTTP {response.status}")
            body = json.loads(response.read().decode("utf-8"))
        if not isinstance(body, dict):
            raise ValueError("value evaluator response must be an object")
        return body

    def _parse_response(self, response: dict[str, Any]) -> ValueDecision:
        choices = response.get("choices")
        if not isinstance(choices, list) or not choices:
            raise ValueError("value evaluator response has no choices")
        message = choices[0].get("message") if isinstance(choices[0], dict) else None
        content = message.get("content") if isinstance(message, dict) else None
        if isinstance(content, list):
            content = "".join(
                part.get("text", "") for part in content if isinstance(part, dict)
            )
        if not isinstance(content, str):
            raise ValueError("value evaluator content is missing")
        result = self._parse_json_object(content)
        valuable = result.get("valuable")
        if isinstance(valuable, str):
            normalized = valuable.strip().lower()
            if normalized not in {"true", "false"}:
                raise ValueError("value evaluator valuable must be true or false")
            valuable = normalized == "true"
        if not isinstance(valuable, bool):
            raise ValueError("value evaluator valuable must be boolean")

        confidence = result.get("confidence")
        if isinstance(confidence, (int, float)):
            confidence = max(0.0, min(1.0, float(confidence)))
        else:
            confidence = None
        categories = result.get("categories", [])
        if isinstance(categories, str):
            categories = [categories]
        if not isinstance(categories, list):
            categories = []
        categories = [str(item)[:32] for item in categories[:8]]
        reason = str(result.get("reason") or "")[:200]
        return ValueDecision(valuable, True, confidence, categories, reason)

    @staticmethod
    def _parse_json_object(content: str) -> dict[str, Any]:
        cleaned = content.strip()
        cleaned = re.sub(r"^```(?:json)?\s*|\s*```$", "", cleaned, flags=re.IGNORECASE)
        try:
            result = json.loads(cleaned)
        except json.JSONDecodeError:
            start, end = cleaned.find("{"), cleaned.rfind("}")
            if start < 0 or end <= start:
                raise
            result = json.loads(cleaned[start : end + 1])
        if not isinstance(result, dict):
            raise ValueError("value evaluator JSON must be an object")
        return result
