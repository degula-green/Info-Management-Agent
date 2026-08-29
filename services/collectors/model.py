from __future__ import annotations

import hashlib
import json
import re
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from typing import Any


def _text(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    return str(value)


def _iso_timestamp(value: Any, source: str) -> str | None:
    if value in (None, ""):
        return None
    try:
        number = int(value)
        seconds = number / (1000 if source == "feishu" and number > 10_000_000_000 else 1)
        return datetime.fromtimestamp(seconds, tz=timezone.utc).isoformat()
    except (TypeError, ValueError, OverflowError, OSError):
        return None


def _feishu_text(raw: dict[str, Any]) -> str:
    content = raw.get("body", {}).get("content", "")
    try:
        parsed = json.loads(content) if isinstance(content, str) else content
        return _text(parsed.get("text", "")) if isinstance(parsed, dict) else _text(parsed)
    except (TypeError, ValueError, json.JSONDecodeError):
        return _text(content)


def _wechat_text(raw: dict[str, Any]) -> tuple[str, dict[str, Any]]:
    content = _text(raw.get("content", ""))
    match = re.match(r"^([^:\n]+):\s*\n?(.*)$", content, re.S)
    if match and match.group(1).lower().startswith(("wxid_", "gh_")):
        return match.group(2).strip(), {"sender_name_raw": match.group(1)}
    return content, {}


@dataclass
class CanonicalMessage:
    id: str
    source: str
    source_account_id: str
    source_message_id: str
    conversation_id: str
    conversation_type: str
    sender_id: str | None
    sender_name: str | None
    occurred_at: str | None
    occurred_at_raw: Any
    message_type: str
    text: str
    attachments: list[dict[str, Any]] = field(default_factory=list)
    links: list[dict[str, Any]] = field(default_factory=list)
    is_deleted: bool = False
    is_updated: bool = False
    metadata: dict[str, Any] = field(default_factory=dict)
    raw_payload_ref: str | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass
class CollectionEvent:
    source: str
    source_account_id: str
    source_message_id: str
    collected_at: str
    raw_payload: dict[str, Any]

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def normalize_event(event: CollectionEvent) -> CanonicalMessage:
    raw = event.raw_payload
    if event.source == "feishu":
        sender = raw.get("sender") or {}
        text = _feishu_text(raw) if raw.get("msg_type") == "text" else ""
        sender_id = sender.get("id")
        conversation_id = _text(raw.get("chat_id"))
        message_type = _text(raw.get("msg_type") or "unknown")
        metadata = {"message_position": raw.get("message_position"), "tenant_key": sender.get("tenant_key")}
        occurred_raw = raw.get("create_time")
        deleted, updated = bool(raw.get("deleted")), bool(raw.get("updated"))
    else:
        text, metadata = _wechat_text(raw)
        sender_id = raw.get("sender_id")
        conversation_id = _text(raw.get("chat_id"))
        message_type = _text(raw.get("message_type") or "unknown")
        occurred_raw = raw.get("create_time")
        deleted, updated = bool(raw.get("deleted")), bool(raw.get("updated"))
    source_id = _text(event.source_message_id)
    stable = hashlib.sha256(f"{event.source_account_id}:{source_id}".encode()).hexdigest()[:24]
    return CanonicalMessage(
        id=f"msg_{stable}", source=event.source, source_account_id=event.source_account_id,
        source_message_id=source_id, conversation_id=conversation_id,
        conversation_type="group" if conversation_id.lower().endswith(("@chatroom", "@chatroom")) or conversation_id.startswith(("oc_", "@@")) else "direct",
        sender_id=_text(sender_id) or None, sender_name=raw.get("sender_name"),
        occurred_at=_iso_timestamp(occurred_raw, event.source), occurred_at_raw=occurred_raw,
        message_type=message_type, text=text, is_deleted=deleted, is_updated=updated,
        metadata={k: v for k, v in metadata.items() if v is not None},
    )
