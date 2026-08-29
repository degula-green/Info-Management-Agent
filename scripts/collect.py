#!/usr/bin/env python3
"""Reusable local collector runner. Use --source wechat with WeChatDB installed."""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from services.collectors.model import CollectionEvent, normalize_event
from services.collectors.storage import LocalStore


def account_id(source: str, explicit: str | None, info: dict[str, Any]) -> str:
    if explicit:
        return explicit
    external = info.get("wxid") or info.get("username") or info.get("id") or "unknown"
    return f"{source}_{external}"


def collect_fixture(path: Path, source: str, account: str, store: LocalStore) -> int:
    payload = json.loads(path.read_text(encoding="utf-8"))
    rows = payload if isinstance(payload, list) else payload.get("items", [payload])
    saved = 0
    for raw in rows:
        source_id = str(raw.get("message_id") or raw.get("local_id") or raw.get("id") or "")
        if not source_id:
            continue
        event = CollectionEvent(source, account, source_id, datetime.now(timezone.utc).isoformat(), raw)
        if store.save(event, normalize_event(event)):
            saved += 1
    return saved


def run_wechat(args: argparse.Namespace, store: LocalStore) -> int:
    from wechatauto import WeChatDB
    db = WeChatDB(account=args.account or None, db_dir=args.db_dir or None)
    info = db.get_self_info() if hasattr(db, "get_self_info") else {}
    info = info if isinstance(info, dict) else getattr(info, "__dict__", {})
    account = account_id("wechat", args.account, info)
    since = datetime.now(timezone.utc) - timedelta(days=1)
    total = 0
    while True:
        sessions = db.get_sessions(limit=args.chat_limit)
        for row in sessions:
            raw_chat = row if isinstance(row, dict) else vars(row)
            chat_id = str(raw_chat.get("username") or raw_chat.get("chat_id") or raw_chat.get("user") or "")
            if not chat_id:
                continue
            rows = db.get_messages(chat_id, limit=args.message_limit)
            for item in rows:
                raw = item if isinstance(item, dict) else vars(item)
                source_id = str(raw.get("message_id") or raw.get("local_id") or raw.get("id") or "")
                if not source_id:
                    continue
                occurred = raw.get("create_time")
                if not store.get_checkpoint(account, chat_id) and args.since is None and isinstance(occurred, (int, float)) and occurred < since.timestamp():
                    continue
                event = CollectionEvent("wechat", account, source_id, datetime.now(timezone.utc).isoformat(), {**raw, "chat_id": chat_id})
                if store.save(event, normalize_event(event)):
                    total += 1
            store.checkpoint(account, chat_id, {"last_run": datetime.now(timezone.utc).isoformat()})
        if not args.watch:
            return total
        time.sleep(args.interval)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", choices=("wechat", "fixture"), required=True)
    parser.add_argument("--fixture", type=Path)
    parser.add_argument("--data-dir", default=os.getenv("COLLECTOR_DATA_DIR", "data/collector"))
    parser.add_argument("--account")
    parser.add_argument("--db-dir", default=os.getenv("WECHAT_DB_DIR"))
    parser.add_argument("--since")
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--watch", action="store_true")
    parser.add_argument("--interval", type=int, default=60)
    parser.add_argument("--chat-limit", type=int, default=1000)
    parser.add_argument("--message-limit", type=int, default=500)
    args = parser.parse_args()
    args.watch = args.watch and not args.once
    store = LocalStore(args.data_dir)
    if args.source == "fixture":
        if not args.fixture or not args.account:
            parser.error("fixture mode requires --fixture and --account")
        print(json.dumps({"saved": collect_fixture(args.fixture, "fixture", args.account, store)}))
        return 0
    print(json.dumps({"saved": run_wechat(args, store)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
