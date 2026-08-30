from __future__ import annotations

import mimetypes
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from wechatauto import MediaDownloader


SUPPORTED_EXTENSIONS = {"pdf", "docx", "xlsx", "pptx", "txt", "md", "csv"}
MAX_ATTACHMENT_BYTES = 200 * 1024 * 1024


def _safe_name(value: str | None) -> str | None:
    if not value:
        return None
    name = Path(str(value).replace("\\", "/")).name.strip()
    name = re.sub(r"[\x00-\x1f\x7f]+", "", name)
    return name or None


@dataclass(frozen=True)
class WeChatAttachment:
    external_attachment_id: str
    file_name: str
    extension: str
    mime_type: str
    file_size: int | None
    file_category: str = "document"
    preview_capability: str = "rendered"
    chat_id: str = ""
    local_id: int = 0

    def as_dict(self) -> dict[str, Any]:
        return {
            "external_attachment_id": self.external_attachment_id,
            "file_name": self.file_name,
            "extension": self.extension,
            "mime_type": self.mime_type or None,
            "file_size": self.file_size,
            "file_category": self.file_category,
            "preview_capability": self.preview_capability,
            "chat_id": self.chat_id,
            "local_id": self.local_id,
        }


class WeChatAttachmentParser:
    """Extract file-message metadata using WeChatDB/message_resource.db."""

    def __init__(self, db: Any):
        self.db = db
        self.media = MediaDownloader(db)

    @staticmethod
    def is_file_message(raw: dict[str, Any]) -> bool:
        for key in ("local_type", "type", "message_type"):
            value = raw.get(key)
            if str(value).strip().lower() in {"49", "file", "文件"}:
                return True
        # WeChat app messages may have a localized/garbled display type, while
        # the XML payload still carries the stable appmsg type (6 = file).
        content = str(raw.get("content") or "")
        if re.search(r"<appmsg\b[^>]*>.*?<type>\s*6\s*</type>", content, re.I | re.S):
            return True
        return False

    def _name(self, chat_id: str, local_id: int, raw: dict[str, Any]) -> str | None:
        content = str(raw.get("content") or "")
        title = re.search(r"<title>\s*([^<]+?)\s*</title>", content, re.I | re.S)
        if title:
            name = _safe_name(title.group(1))
            if name:
                return name
        try:
            row = self.db.get_message_row(chat_id, local_id)
            if row:
                name = self.media._file_name(row)
                if name:
                    return _safe_name(name)
        except Exception:
            pass
        for key in ("file_name", "filename", "name"):
            name = _safe_name(raw.get(key))
            if name:
                return name
        match = re.search(r"[^\s<>:\"/\\|?*]+\.(?:pdf|docx|xlsx|pptx|txt|md|csv)\b", content, re.I)
        return _safe_name(match.group(0)) if match else None

    def parse(self, wxid: str, chat_id: str, raw: dict[str, Any]) -> list[dict[str, Any]]:
        if not self.is_file_message(raw):
            return []
        try:
            local_id = int(raw.get("local_id"))
        except (TypeError, ValueError):
            return []
        name = self._name(chat_id, local_id, raw)
        if not name:
            return [{
                "external_attachment_id": f"{wxid}:{chat_id}:{local_id}:0",
                "file_name": None,
                "extension": None,
                "mime_type": None,
                "file_size": None,
                "file_category": "unknown",
                "preview_capability": "pending",
                "parse_error": "WeChat file name could not be resolved",
            }]
        extension = Path(name).suffix.lower().lstrip(".")
        if extension not in SUPPORTED_EXTENSIONS:
            return [{
                "external_attachment_id": f"{wxid}:{chat_id}:{local_id}:0",
                "file_name": name,
                "extension": extension or None,
                "mime_type": mimetypes.guess_type(name)[0],
                "file_size": None,
                "file_category": "unknown",
                "preview_capability": "pending",
                "unsupported": True,
            }]
        return [WeChatAttachment(
            external_attachment_id=f"{wxid}:{chat_id}:{local_id}:0",
            file_name=name,
            extension=extension,
            mime_type=mimetypes.guess_type(name)[0] or "application/octet-stream",
            file_size=None,
            chat_id=chat_id,
            local_id=local_id,
        ).as_dict()]
