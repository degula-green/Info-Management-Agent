from __future__ import annotations

import json
import hashlib
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .model import CanonicalMessage, CollectionEvent


class LocalStore:
    def __init__(self, root: str | Path):
        self.root = Path(root).resolve()
        for name in ("raw", "normalized", "checkpoints", "outbox", "attachments", "logs"):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        self.seen_path = self.root / "seen.json"
        self.seen: set[str] = set(json.loads(self.seen_path.read_text(encoding="utf-8"))) if self.seen_path.exists() else set()

    @staticmethod
    def _append(path: Path, value: dict[str, Any]) -> int:
        path.parent.mkdir(parents=True, exist_ok=True)
        line_no = 0
        if path.exists():
            with path.open("r", encoding="utf-8") as existing:
                line_no = sum(1 for _ in existing)
        with path.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(value, ensure_ascii=False, default=str) + "\n")
            stream.flush()
        return line_no + 1

    def save(self, event: CollectionEvent, message: CanonicalMessage) -> bool:
        key = f"{event.source_account_id}:{event.source_message_id}"
        if key in self.seen:
            return False
        day = datetime.now(timezone.utc).date().isoformat()
        raw_path = self.root / "raw" / event.source / event.source_account_id / f"{day}.jsonl"
        raw_ref = self._append(raw_path, event.to_dict())
        message.raw_payload_ref = f"raw/{event.source}/{event.source_account_id}/{day}.jsonl#{raw_ref}"
        self._append(self.root / "normalized" / "messages.jsonl", message.to_dict())
        self._append(self.root / "outbox" / "pending-events.jsonl", event.to_dict())
        self.seen.add(key)
        self.seen_path.write_text(json.dumps(sorted(self.seen), ensure_ascii=False, indent=2), encoding="utf-8")
        return True

    def checkpoint(self, account: str, conversation: str, value: dict[str, Any]) -> None:
        path = self.root / "checkpoints" / f"{account}.json"
        data = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
        data[conversation] = value
        path.write_text(json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8")

    def get_checkpoint(self, account: str, conversation: str) -> dict[str, Any]:
        path = self.root / "checkpoints" / f"{account}.json"
        if not path.exists():
            return {}
        return json.loads(path.read_text(encoding="utf-8")).get(conversation, {})
