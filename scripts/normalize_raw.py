#!/usr/bin/env python3
"""Normalize raw collector JSONL files into CanonicalMessage JSONL."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from services.collectors.model import CollectionEvent, normalize_event


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--data-dir", default="data/collector-feishu")
    parser.add_argument("--source", default="feishu")
    args = parser.parse_args()
    root = Path(args.data_dir)
    output = root / "normalized" / "messages.jsonl"
    output.parent.mkdir(parents=True, exist_ok=True)
    known: set[str] = set()
    if output.exists():
        for line in output.read_text(encoding="utf-8").splitlines():
            try:
                item = json.loads(line)
                known.add(f"{item.get('source_account_id')}:{item.get('source_message_id')}")
            except json.JSONDecodeError:
                continue
    saved = 0
    for path in sorted((root / "raw" / args.source).glob("*/*.jsonl")):
        for line_no, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
            if not line.strip():
                continue
            try:
                item = json.loads(line)
                event = CollectionEvent(
                    source=str(item["source"]),
                    source_account_id=str(item["source_account_id"]),
                    source_message_id=str(item["source_message_id"]),
                    collected_at=str(item.get("collected_at") or ""),
                    raw_payload=item["raw_payload"],
                )
            except (KeyError, TypeError, json.JSONDecodeError) as exc:
                print(f"skip {path}: {exc}", file=sys.stderr)
                continue
            key = f"{event.source_account_id}:{event.source_message_id}"
            if key in known:
                continue
            message = normalize_event(event)
            relative_ref = str(path.relative_to(root)).replace("\\", "/")
            message.raw_payload_ref = f"{relative_ref}#{line_no}"
            with output.open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(message.to_dict(), ensure_ascii=False) + "\n")
            known.add(key)
            saved += 1
    print(json.dumps({"saved": saved, "output": str(output)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
