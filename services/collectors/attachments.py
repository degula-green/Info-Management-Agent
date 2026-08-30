"""Deterministic attachment metadata classification shared by collectors."""
from __future__ import annotations

import mimetypes
import json
from pathlib import PurePosixPath
from typing import Any


def classify_attachment(name: str, mime_type: str = "") -> tuple[str, str]:
    ext = PurePosixPath(name or "").suffix.lower()
    mime = (mime_type or mimetypes.guess_type(name or "")[0] or "").lower()
    if ext in {".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz"} or "zip" in mime or "compressed" in mime:
        return "archive", "download_only"
    if ext in {".exe", ".msi", ".dmg", ".apk", ".ipa", ".deb", ".rpm", ".bat", ".cmd", ".sh"}:
        return "installer", "download_only"
    if mime.startswith("image/") or ext in {".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}:
        return "image", "inline"
    if mime.startswith("audio/") or ext in {".mp3", ".wav", ".m4a", ".ogg", ".flac"}:
        return "audio", "inline"
    if mime.startswith("video/") or ext in {".mp4", ".mov", ".avi", ".mkv", ".webm"}:
        return "video", "inline"
    if ext in {".pdf", ".txt", ".md", ".csv"} or mime in {"application/pdf", "text/plain", "text/markdown", "text/csv"}:
        return "document", "inline"
    if ext in {".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx"}:
        return "document", "rendered"
    if mime.startswith("text/") or "document" in mime or "spreadsheet" in mime or "presentation" in mime:
        return "document", "rendered"
    return "unknown", "pending"


def extract_attachments(raw: dict[str, Any]) -> list[dict[str, Any]]:
    values: Any = raw.get("attachments") or raw.get("files") or raw.get("file")
    # Feishu file messages commonly embed file metadata as a JSON string in body.content.
    if not values:
        body = raw.get("body")
        content = body.get("content") if isinstance(body, dict) else None
        if isinstance(content, str):
            try:
                parsed = json.loads(content)
            except (TypeError, ValueError):
                parsed = None
            if isinstance(parsed, dict):
                values = parsed.get("attachments") or parsed.get("files") or parsed.get("file")
                if not values and any(k in parsed for k in ("file_key", "file_id", "key", "file_name", "name")):
                    values = [parsed]
    if isinstance(values, dict):
        values = [values]
    if isinstance(values, dict):
        values = [values]
    if not isinstance(values, list):
        return []
    result = []
    for item in values:
        if not isinstance(item, dict):
            continue
        name = str(item.get("file_name") or item.get("filename") or item.get("name") or "").strip()
        mime = str(item.get("mime_type") or item.get("mime") or item.get("content_type") or "").strip()
        category, preview = classify_attachment(name, mime)
        result.append({
            "external_attachment_id": str(item.get("id") or item.get("file_id") or item.get("file_key") or item.get("key") or "") or None,
            "file_name": name or None,
            "extension": PurePosixPath(name).suffix.lower().lstrip(".") or None,
            "mime_type": mime or None,
            "file_category": category,
            "file_size": item.get("size") or item.get("file_size"),
            "preview_capability": preview,
        })
    return result
