from __future__ import annotations

import os
import re
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from wechatauto import WeChatDB

from services.collectors.value_judgment import MessageValueClient
from services.collectors.wechat.repository import WeChatRepository, occurred_at


class BindRequest(BaseModel):
    db_dir: str = Field(min_length=3)
    wxid: str = Field(min_length=3)


class WeChatWorker:
    def __init__(self, repo: WeChatRepository, db: Any, binding: dict[str, Any], interval: float = 5, evaluator: Any = None):
        self.repo, self.db, self.binding, self.interval, self.evaluator = repo, db, binding, interval, evaluator
        self.stop_event = threading.Event()
        self.thread: threading.Thread | None = None
        self.status = "stopped"
        self.last_error: str | None = None
        self.last_collected_at: datetime | None = None

    def start(self) -> None:
        self.status = "running"
        self.thread = threading.Thread(target=self.run, name="wechat-collector", daemon=True)
        self.thread.start()

    def stop(self) -> None:
        self.stop_event.set()
        if self.thread: self.thread.join(timeout=10)
        self.status = "stopped"
        self.repo.heartbeat("stopped")

    def run(self) -> None:
        self.repo.heartbeat("running")
        while not self.stop_event.is_set():
            processed = failed = 0
            error = None
            try:
                processed, failed = self.poll()
                self.status, self.last_error = "running", None
            except Exception as exc:
                failed, error = 1, str(exc)
                self.status, self.last_error = "error", error
            try: self.repo.heartbeat(self.status, processed, failed, error)
            except Exception: pass
            self.stop_event.wait(self.interval)

    def poll(self) -> tuple[int, int]:
        processed = failed = 0
        account_id, wxid = self.binding["account_id"], self.binding["wxid"]
        bound_at = self.binding["bound_at"]
        for session in self.db.get_sessions(limit=1000):
            chat_id = str(session.get("username") or "")
            if not chat_id: continue
            try:
                conversation_id = self.repo.upsert_conversation(account_id, session)
                seq = self.repo.checkpoint(account_id, conversation_id)
                if seq is None:
                    recent = self.db.get_messages(chat_id, limit=1000)
                    candidates = [m for m in recent if (occurred_at(m.get("create_time")) or datetime.min.replace(tzinfo=timezone.utc)) >= bound_at]
                    candidates.sort(key=lambda item: int(item.get("sort_seq") or 0))
                else:
                    candidates = []
                    cursor = seq
                    while True:
                        batch = self.db.get_new_messages(chat_id, since_seq=cursor, limit=200)
                        if not batch: break
                        candidates.extend(batch)
                        cursor = max(int(item.get("sort_seq") or cursor) for item in batch)
                        if len(batch) < 200: break
                max_seq = seq or 0
                last_id = None
                last_time = None
                for raw in candidates:
                    raw_seq = int(raw.get("sort_seq") or 0)
                    max_seq = max(max_seq, raw_seq)
                    when = occurred_at(raw.get("create_time"))
                    if when and when < bound_at: continue
                    evaluation_message = {**raw, "chat_id": chat_id}
                    if self.evaluator is not None and not self.evaluator.is_valuable("wechat", evaluation_message):
                        last_id = f"{chat_id}:{raw.get('local_id')}"
                        last_time = when
                        continue
                    if self.repo.persist_message(account_id, conversation_id, wxid, chat_id, raw):
                        processed += 1
                        self.last_collected_at = datetime.now(timezone.utc)
                    last_id = f"{chat_id}:{raw.get('local_id')}"
                    last_time = when
                if seq is None and not candidates:
                    latest = self.db.get_messages(chat_id, limit=1)
                    if latest: max_seq = int(latest[0].get("sort_seq") or 0)
                self.repo.save_checkpoint(account_id, conversation_id, max_seq, last_id, last_time)
            except Exception as exc:
                failed += 1
                self.last_error = f"{chat_id}: {exc}"
        return processed, failed


class Manager:
    def __init__(self):
        database_url = os.getenv("COLLECTOR_DATABASE_URL") or os.getenv("CORE_DATABASE_URL") or ""
        if not database_url: raise RuntimeError("COLLECTOR_DATABASE_URL is required")
        self.repo = WeChatRepository(database_url)
        self.repo.ensure_schema()
        self.worker: WeChatWorker | None = None
        self.lock = threading.Lock()

    @staticmethod
    def open_db(db_dir: str, wxid: str):
        path = Path(db_dir)
        if not path.is_absolute() or not path.exists():
            raise ValueError("db_dir must be an existing local absolute path")
        # WeChatDB expects the parent directory plus an account directory. Accept
        # either that parent, the account directory, or one database file.
        if path.is_file():
            storage = next((parent for parent in path.parents if parent.name == "db_storage"), None)
            if storage is None:
                raise ValueError("database file must be located below an account db_storage directory")
            path = storage.parent
        if not path.is_dir():
            raise ValueError("db_dir must point to a directory or a database file")
        if (path / "db_storage").is_dir():
            account = path.name
            root = path.parent
        else:
            root = path
            account = ""
        if not account:
            # Prefer a directory whose normalized name matches the supplied wxid.
            # If it cannot be inferred from the directory name, a single account
            # directory is still unambiguous and may be bound to the supplied wxid.
            candidates = [item for item in root.iterdir()
                          if item.is_dir() and (item / "db_storage").is_dir()]
            matches = [item for item in candidates
                       if re.sub(r"_\w{4}$", "", item.name) == wxid]
            selected = matches[0] if len(matches) == 1 else (candidates[0] if len(candidates) == 1 else None)
            if selected is None:
                raise ValueError("db_dir must contain exactly one WeChat account directory, or one matching the supplied wxid")
            account = selected.name
        if not (root / account / "db_storage").is_dir():
            raise ValueError("db_dir does not contain a readable WeChat db_storage directory")
        db = WeChatDB(account=account, db_dir=str(root))
        # The supplied wxid is the binding identity. Do not infer or reject it
        # from contact.db: exported/partial databases may not contain self info.
        info = {}
        db.get_sessions(limit=1)
        return db, info

    def bind(self, request: BindRequest) -> dict[str, Any]:
        with self.lock:
            db, info = self.open_db(request.db_dir, request.wxid)
            if self.worker: self.worker.stop()
            baselines = []
            for session in db.get_sessions(limit=1000):
                chat_id = str(session.get("username") or "")
                if not chat_id: continue
                try:
                    latest = db.get_messages(chat_id, limit=1)
                    seq = int(latest[0].get("sort_seq") or 0) if latest else 0
                except Exception:
                    # A single malformed/locked message DB must not prevent the
                    # account from binding; that conversation is isolated.
                    seq = 0
                baselines.append((session, seq))
            bound_at = datetime.now(timezone.utc)
            account_id = self.repo.bind(request.wxid, str(Path(request.db_dir).resolve()), str(info.get("nick_name") or request.wxid), bound_at)
            self.repo.reset_checkpoints(account_id)
            for session, seq in baselines:
                conversation_id = self.repo.upsert_conversation(account_id, session)
                self.repo.save_checkpoint(account_id, conversation_id, seq)
            binding = {"account_id": account_id, "wxid": request.wxid, "db_dir": str(Path(request.db_dir).resolve()), "bound_at": bound_at}
            self.worker = WeChatWorker(self.repo, db, binding, float(os.getenv("WECHAT_POLL_INTERVAL", "5")), MessageValueClient())
            self.worker.start()
            return self.status()

    def restore(self) -> None:
        binding = self.repo.active_binding()
        if not binding: return
        try:
            db, _ = self.open_db(binding["db_dir"], binding["wxid"])
            self.worker = WeChatWorker(self.repo, db, binding, float(os.getenv("WECHAT_POLL_INTERVAL", "5")), MessageValueClient())
            self.worker.start()
        except Exception as exc:
            self.repo.heartbeat("error", failed=1, error=str(exc))

    def stop(self) -> dict[str, Any]:
        with self.lock:
            if self.worker: self.worker.stop()
            self.repo.disable()
            return self.status()

    def status(self) -> dict[str, Any]:
        worker = self.worker
        return {"status": worker.status if worker else "stopped", "wxid": worker.binding["wxid"] if worker else None,
                "db_dir": worker.binding["db_dir"] if worker else None, "bound_at": worker.binding["bound_at"] if worker else None,
                "last_collected_at": worker.last_collected_at if worker else None, "last_error": worker.last_error if worker else None}


app = FastAPI(title="info-agent-wechat-collector")
manager: Manager | None = None


@app.on_event("startup")
def startup() -> None:
    global manager
    manager = Manager()
    manager.restore()


@app.on_event("shutdown")
def shutdown() -> None:
    if manager and manager.worker: manager.worker.stop()


def current() -> Manager:
    if manager is None: raise HTTPException(503, "collector is starting")
    return manager


@app.get("/health")
def health(): return {"service": "wechat-collector", "status": "ok"}


@app.post("/bind")
def bind(request: BindRequest):
    try: return current().bind(request)
    except (ValueError, RuntimeError) as exc: raise HTTPException(422, str(exc)) from exc


@app.post("/rebind")
def rebind(request: BindRequest):
    try: return current().bind(request)
    except (ValueError, RuntimeError) as exc: raise HTTPException(422, str(exc)) from exc


@app.get("/status")
def status(): return current().status()


@app.post("/stop")
def stop(): return current().stop()
