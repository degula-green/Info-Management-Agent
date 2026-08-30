from __future__ import annotations

import unittest
import os
from datetime import datetime, timedelta, timezone
from unittest.mock import patch

from fastapi import HTTPException

from services.collectors.wechat.service import Manager, WeChatWorker, internal_user


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
    def persist_message(self, account_id, conversation_id, wxid, chat_id, raw, parsed_attachments=None):
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
        rows = [{"local_id": 3, "sort_seq": 12, "create_time": int(datetime.now(timezone.utc).timestamp()), "type": "text", "content": "ok"}]
        repo = FakeRepo({1: 10}); db = FakeDB([{"id": 1, "username": "room@chatroom"}], new={"room@chatroom": rows})
        worker = WeChatWorker(repo, db, {"account_id": 1, "wxid": "wxid_a", "bound_at": bound}, evaluator=FakeEvaluator(False))
        self.assertEqual(worker.poll(), (0, 0)); self.assertEqual(repo.persisted, []); self.assertEqual(repo.saved[-1], (1, 12))


class FakeManagerRepo:
    def __init__(self):
        self.bindings = {
            10: {"account_id": 101, "internal_account_id": 10},
            20: {"account_id": 202, "internal_account_id": 20},
        }
        self.disabled = []

    def active_binding(self, user_id): return self.bindings.get(user_id)
    def disable(self, account_id): self.disabled.append(account_id)


class FakeManagedWorker:
    def __init__(self, account_id, user_id):
        self.binding = {"account_id": account_id, "internal_account_id": user_id, "wxid": f"wxid_{user_id}", "db_dir": "C:/db", "bound_at": datetime.now(timezone.utc)}
        self.status, self.last_collected_at, self.last_error, self.stopped = "running", None, None, False

    def stop(self): self.stopped = True; self.status = "stopped"


class ManagerIsolationTests(unittest.TestCase):
    def setUp(self):
        self.manager = Manager.__new__(Manager)
        self.manager.repo = FakeManagerRepo()
        self.manager.workers = {101: FakeManagedWorker(101, 10), 202: FakeManagedWorker(202, 20)}
        import threading
        self.manager.lock = threading.Lock()

    def test_worker_lookup_is_scoped_to_user(self):
        self.assertEqual(self.manager.worker_for_user(10).binding["account_id"], 101)
        self.assertEqual(self.manager.worker_for_user(20).binding["account_id"], 202)

    def test_stop_only_stops_current_users_worker(self):
        first, second = self.manager.workers[101], self.manager.workers[202]
        self.manager.stop(10)
        self.assertTrue(first.stopped)
        self.assertFalse(second.stopped)
        self.assertEqual(self.manager.repo.disabled, [101])

    def test_internal_token_is_required(self):
        with patch.dict(os.environ, {"COLLECTOR_INTERNAL_TOKEN": "secret"}):
            self.assertEqual(internal_user(10, "secret"), 10)
            with self.assertRaises(HTTPException) as raised:
                internal_user(10, "wrong")
            self.assertEqual(raised.exception.status_code, 401)


if __name__ == "__main__": unittest.main()
