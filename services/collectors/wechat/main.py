#!/usr/bin/env python3
"""Run the local WeChat collector with manual db directory and wxid."""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from scripts.collect import run_wechat
from services.collectors.storage import LocalStore


def main() -> int:
    parser = argparse.ArgumentParser(description="Collect messages from a locally logged-in WeChat")
    parser.add_argument("--db-dir", required=True, help="local WeChat database directory")
    parser.add_argument("--wxid", required=True, help="pre-registered WeChat wxid")
    parser.add_argument("--data-dir", default=os.getenv("COLLECTOR_DATA_DIR", "data/collector-wechat"))
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--watch", action="store_true")
    parser.add_argument("--interval", type=int, default=60)
    parser.add_argument("--chat-limit", type=int, default=1000)
    parser.add_argument("--message-limit", type=int, default=500)
    parser.add_argument("--since")
    args = parser.parse_args()
    args.account = args.wxid
    args.watch = args.watch and not args.once
    store = LocalStore(args.data_dir)
    print(json.dumps({"saved": run_wechat(args, store)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
