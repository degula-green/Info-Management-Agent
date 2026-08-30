from __future__ import annotations

import unittest
from datetime import datetime, timedelta, timezone

from services.collectors.wechat.service import WeChatWorker


class FakeDB:
    def __init__(self, sessions, recent=None, new=None):
        self.sessions, self.recent, self.new = sessions, recent or {}, new or {}

    def get_sessions(self, limit=1000): return self.sessions
    def get_messages(self, chat, limit=1000): return self.recent.get(chat, [])[:limit]
    def get_new_messages(self, chat, since_seq=0, limit=200):
        return [item for item in self.new.get(chat, []) if int(item["sort_seq"]) > since_seq][:limit]


class FakeRepo:
    def __init__(self, checkpoints):
        self.checkpoints, self.persisted, self.saved = checkpoints, [], []

    def upsert_conversation(self, account_id, session): return session["id"]
    def checkpoint(self, account_id, conversation_id): return self.checkpoints.get(conversation_id)
    def persist_message(self, account_id, conversation_id, wxid, chat_id, raw):
        self.persisted.append(raw["local_id"]); return True
    def save_checkpoint(self, account_id, conversation_id, seq, message_id=None, message_time=None):
        self.saved.append((conversation_id, seq))
    def heartbeat(self, *args, **kwargs): pass


class FakeEvaluator:
    def __init__(self, valuable): self.valuable = valuable
    def is_valuable(self, source, message): return self.valuable


class WorkerTests(unittest.TestCase):
    def test_existing_conversation_only_reads_after_checkpoint(self):
        bound = datetime.now(timezone.utc) - timedelta(minutes=1)
        rows = [{"local_id": 2, "sort_seq": 11, "create_time": int(datetime.now(timezone.utc).timestamp()), "type": "text", "content": "new"}]
        repo = FakeRepo({1: 10}); db = FakeDB([{"id": 1, "username": "chat"}], new={"chat": rows})
        worker = WeChatWorker(repo, db, {"account_id": 1, "wxid": "wxid_a", "bound_at": bound})
        self.assertEqual(worker.poll(), (1, 0)); self.assertEqual(repo.persisted, [2]); self.assertEqual(repo.saved[-1], (1, 11))

    def test_new_conversation_filters_messages_before_binding(self):
        bound = datetime.now(timezone.utc)
        rows = [
            {"local_id": 1, "sort_seq": 1, "create_time": int((bound-timedelta(seconds=5)).timestamp()), "type": "text", "content": "old"},
            {"local_id": 2, "sort_seq": 2, "create_time": int((bound+timedelta(seconds=5)).timestamp()), "type": "text", "content": "new"},
        ]
        repo = FakeRepo({}); db = FakeDB([{"id": 2, "username": "new-chat"}], recent={"new-chat": list(reversed(rows))})
        worker = WeChatWorker(repo, db, {"account_id": 1, "wxid": "wxid_a", "bound_at": bound})
        self.assertEqual(worker.poll(), (1, 0)); self.assertEqual(repo.persisted, [2]); self.assertEqual(repo.saved[-1], (2, 2))

    def test_low_value_group_message_is_not_persisted(self):
        bound = datetime.now(timezone.utc) - timedelta(minutes=1)
        rows = [{"local_id": 3, "sort_seq": 12, "create_time": int(datetime.now(timezone.utc).timestamp()), "type": "text", "content": "好的"}]
        repo = FakeRepo({1: 10}); db = FakeDB([{"id": 1, "username": "room@chatroom"}], new={"room@chatroom": rows})
        worker = WeChatWorker(repo, db, {"account_id": 1, "wxid": "wxid_a", "bound_at": bound}, evaluator=FakeEvaluator(False))
        self.assertEqual(worker.poll(), (0, 0))
        self.assertEqual(repo.persisted, [])
        self.assertEqual(repo.saved[-1], (1, 12))


if __name__ == "__main__": unittest.main()
