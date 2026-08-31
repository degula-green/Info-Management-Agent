from __future__ import annotations

import os
import re
import hashlib
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from fastapi import Depends, FastAPI, HTTPException, Header
from pydantic import BaseModel, Field
from wechatauto import WeChatDB

from services.collectors.wechat.downloader import WeChatAttachmentDownloadWorker
from services.collectors.wechat.attachments import WeChatAttachmentParser
from services.collectors.wechat.repository import WeChatRepository, conversation_display_name, display_name, occurred_at
from services.collectors.value_judgment import MessageValueClient
from services.collectors.message_filter import is_obvious_noise


class BindRequest(BaseModel):
    db_dir: str = Field(min_length=3)
    wxid: str = Field(min_length=3)


class ConfigRequest(BaseModel):
    selected_conversations: list[str] = Field(default_factory=list)
    history_start_at: datetime | None = None


def resolve_session_name(db: Any, session: dict[str, Any], chat_id: str) -> str:
    """Resolve a current human name from the same source as wechat-python.

    ``get_nickname`` reflects contact remark/nickname changes immediately,
    while the session row may contain a stale snapshot.  Prefer it for direct
    chats; retain explicit room titles for group conversations.
    """
    get_nickname = getattr(db, "get_nickname", None)
    if get_nickname and not chat_id.lower().endswith("@chatroom"):
        try:
            value = display_name(get_nickname(str(chat_id)) or "", chat_id)
            if value:
                return value
        except Exception:
            pass
    explicit = conversation_display_name(session, chat_id)
    if explicit != chat_id:
        return explicit
    if get_nickname:
        try:
            value = display_name(get_nickname(str(chat_id)) or "", chat_id)
            if value:
                return value
        except Exception:
            pass
    return chat_id


class WeChatWorker:
    def __init__(self, repo: WeChatRepository, db: Any, binding: dict[str, Any], interval: float = 5, evaluator: Any = None):
        self.repo, self.db, self.binding, self.interval, self.evaluator = repo, db, binding, interval, evaluator
        self.attachment_parser = WeChatAttachmentParser(db)
        self.stop_event = threading.Event()
        self.thread: threading.Thread | None = None
        self.status = "stopped"
        self.last_error: str | None = None
        self.last_collected_at: datetime | None = None
        self._corrupt_chats: set[str] = set()
        self._participant_names: dict[str, str | None] = {}
        self._participant_backfill_complete = False
        self.attachment_worker: WeChatAttachmentDownloadWorker | None = None

    def start(self) -> None:
        self.status = "running"
        self.thread = threading.Thread(target=self.run, name=f"wechat-collector-{self.binding['account_id']}", daemon=True)
        self.thread.start()
        try:
            self.attachment_worker = WeChatAttachmentDownloadWorker(
                self.repo, self.db, self.binding,
                os.getenv("COLLECTOR_DATA_DIR", "data/collector-wechat"),
                float(os.getenv("WECHAT_ATTACHMENT_POLL_INTERVAL", "5")),
            )
            self.attachment_worker.start()
        except Exception as exc:
            self.last_error = f"attachment worker unavailable: {exc}"

    def stop(self) -> None:
        self.stop_event.set()
        if self.thread: self.thread.join(timeout=10)
        if self.attachment_worker: self.attachment_worker.stop()
        self.status = "stopped"
        self.repo.heartbeat(self.binding["account_id"], "stopped")

    def run(self) -> None:
        self.repo.heartbeat(self.binding["account_id"], "running")
        while not self.stop_event.is_set():
            processed = failed = 0
            error = None
            try:
                processed, failed = self.poll()
                self.status, self.last_error = "running", None
            except Exception as exc:
                failed, error = 1, str(exc)
                self.status, self.last_error = "error", error
            try: self.repo.heartbeat(self.binding["account_id"], self.status, processed, failed, error)
            except Exception: pass
            self.stop_event.wait(self.interval)
        if self.attachment_worker:
            self.attachment_worker.stop()
        self.status = "stopped"

    def poll(self) -> tuple[int, int]:
        processed = failed = 0
        account_id, wxid = self.binding["account_id"], self.binding["wxid"]
        if hasattr(self.repo, "get_config"):
            config = self.repo.get_config(account_id)
            if not config.get("enabled", True):
                self.stop_event.set()
                return 0, 0
            selected = {str(item) for item in config.get("selected_conversations", [])}
            # Profile reconciliation must also run while message collection is
            # paused by an empty whitelist.
            self._backfill_participant_names(account_id)
            # An empty whitelist pauses message collection, but metadata still
            # needs to be reconciled so names and newly discovered chats stay
            # current in the knowledge-base directory.
        else:
            # Keep lightweight test doubles and older repository adapters
            # compatible while the persisted config migration rolls out.
            config = {"selected_conversations": [], "history_start_at": None, "config_updated_at": None}
            selected = None
        self.binding.update(config)
        if selected is None:
            self._backfill_participant_names(account_id)
        bound_at = config.get("history_start_at") or config.get("config_updated_at") or self.binding["bound_at"]
        for session in self.db.get_sessions(limit=1000):
            chat_id = str(session.get("username") or "")
            if not chat_id: continue
            collect_messages = selected is None or chat_id in selected
            if chat_id in self._corrupt_chats: continue
            try:
                # wechat-python resolves names through WeChatDB.get_nickname;
                # use that same lookup before persisting session metadata.
                resolved_name = resolve_session_name(self.db, session, chat_id)
                if resolved_name:
                    session = {**session, "display_name": resolved_name}
                conversation_id = self.repo.upsert_conversation(account_id, session)
                if not collect_messages:
                    continue
                seq = self.repo.checkpoint(account_id, conversation_id)
                if seq is None:
                    recent = _messages_across_shards(self.db, chat_id, limit=1000)
                    candidates = [m for m in recent if (occurred_at(m.get("create_time")) or datetime.min.replace(tzinfo=timezone.utc)) >= bound_at]
                    candidates.sort(key=lambda item: int(item.get("sort_seq") or 0))
                else:
                    candidates = []
                    cursor = seq
                    while True:
                        batch = _new_messages_across_shards(self.db, chat_id, since_seq=cursor, limit=200)
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
                    self._enrich_sender_name(raw)
                    parsed_attachments = self.attachment_parser.parse(wxid, chat_id, raw) if self.attachment_parser.is_file_message(raw) else None
                    if is_obvious_noise(raw):
                        last_id = f"{chat_id}:{raw.get('local_id')}"
                        last_time = when
                        continue
                    if self.evaluator is not None and not self.evaluator.is_valuable("wechat", {**raw, "chat_id": chat_id}):
                        last_id = f"{chat_id}:{raw.get('local_id')}"
                        last_time = when
                        continue
                    if self.repo.persist_message(account_id, conversation_id, wxid, chat_id, raw, parsed_attachments):
                        processed += 1
                        self.last_collected_at = datetime.now(timezone.utc)
                    last_id = f"{chat_id}:{raw.get('local_id')}"
                    last_time = when
                if seq is None and not candidates:
                    latest = _messages_across_shards(self.db, chat_id, limit=1)
                    if latest: max_seq = int(latest[0].get("sort_seq") or 0)
                self.repo.save_checkpoint(account_id, conversation_id, max_seq, last_id, last_time)
            except Exception as exc:
                failed += 1
                detail = str(exc)
                if "database disk image is malformed" in detail.lower():
                    self._corrupt_chats.add(chat_id)
                else:
                    self.last_error = f"{chat_id}: {detail}"
        return processed, failed

    def _enrich_sender_name(self, raw: dict[str, Any]) -> None:
        sender_id = str(raw.get("sender_username") or raw.get("real_sender_id") or raw.get("sender_id") or "").strip()
        if not sender_id:
            return
        if sender_id not in self._participant_names:
            nickname = ""
            get_nickname = getattr(self.db, "get_nickname", None)
            if get_nickname:
                try:
                    nickname = str(get_nickname(sender_id) or "").strip()
                except Exception:
                    nickname = ""
            self._participant_names[sender_id] = nickname or None
        nickname = self._participant_names[sender_id]
        if nickname and nickname.lower() != sender_id.lower() and not nickname.lower().startswith("wxid_"):
            raw["sender_name"] = nickname

    def _backfill_participant_names(self, account_id: int) -> None:
        if self._participant_backfill_complete:
            return
        unresolved = getattr(self.repo, "unresolved_participants", None)
        update = getattr(self.repo, "update_participant_display_name", None)
        if not unresolved or not update:
            self._participant_backfill_complete = True
            return
        for participant_id in unresolved(account_id):
            raw: dict[str, Any] = {"sender_id": participant_id}
            self._enrich_sender_name(raw)
            if raw.get("sender_name"):
                update(account_id, participant_id, raw["sender_name"])
        self._participant_backfill_complete = True


def _message_rows_across_shards(db: Any, user: str) -> list[dict[str, Any]]:
    """Read a conversation from every encrypted message shard.

    Some WeChat versions retain the same conversation table in more than one
    shard. WeChatDB's public helpers stop at the first matching table, which
    can hide newer rows in a later shard.
    """
    if not hasattr(db, "_message_dbs") or not hasattr(db, "_open"):
        try:
            return list(db.get_messages(user, limit=100000))
        except Exception:
            return []
    table = "Msg_" + hashlib.md5(user.encode()).hexdigest()
    rows: list[dict[str, Any]] = []
    for rel in db._message_dbs():
        conn = None
        try:
            conn = db._open(rel)
            exists = conn.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (table,)).fetchone()
            if not exists:
                continue
            raw_rows = conn.execute(
                f"SELECT local_id,local_type,real_sender_id,create_time,message_content,source,packed_info_data,sort_seq FROM \"{table}\""
            ).fetchall()
            rows.extend(db._msg_row_to_dict(row) for row in raw_rows)
        finally:
            if conn is not None:
                conn.close()
    rows.sort(key=lambda item: int(item.get("sort_seq") or 0), reverse=True)
    return rows


def _messages_across_shards(db: Any, user: str, limit: int = 1000) -> list[dict[str, Any]]:
    rows = _message_rows_across_shards(db, user)
    return rows[: max(0, limit)]


def _new_messages_across_shards(db: Any, user: str, since_seq: int, limit: int = 200) -> list[dict[str, Any]]:
    if not hasattr(db, "_message_dbs") or not hasattr(db, "_open"):
        try:
            return list(db.get_new_messages(user, since_seq=since_seq, limit=limit))
        except Exception:
            return []
    rows = [row for row in _message_rows_across_shards(db, user) if int(row.get("sort_seq") or 0) > since_seq]
    rows.sort(key=lambda item: int(item.get("sort_seq") or 0))
    return rows[: max(0, limit)]


class Manager:
    def __init__(self):
        database_url = os.getenv("COLLECTOR_DATABASE_URL") or os.getenv("CORE_DATABASE_URL") or ""
        if not database_url: raise RuntimeError("COLLECTOR_DATABASE_URL is required")
        self.repo = WeChatRepository(database_url)
        self.repo.ensure_schema()
        self.workers: dict[int, WeChatWorker] = {}
        self.lock = threading.Lock()

    @property
    def worker(self) -> WeChatWorker | None:
        """Compatibility accessor for older tests and shutdown hooks."""
        return next(iter(self.workers.values()), None)

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

    def bind(self, request: BindRequest, internal_account_id: int = 1, rebind: bool = False) -> dict[str, Any]:
        with self.lock:
            requested_dir = str(Path(request.db_dir).resolve())
            existing = self.repo.active_binding(internal_account_id)
            if existing and existing["wxid"] == request.wxid:
                if existing["db_dir"] == requested_dir:
                    if existing["account_id"] not in self.workers:
                        self._start_binding(existing)
                    return self.status(internal_account_id)
            if existing and not rebind:
                raise RuntimeError("wechat connector is already bound")
            db, info = self.open_db(request.db_dir, request.wxid)
            if existing:
                old_worker = self.workers.pop(existing["account_id"], None)
                if old_worker: old_worker.stop()
            bound_at = datetime.now(timezone.utc)
            account_id = self.repo.bind(request.wxid, requested_dir, str(info.get("nick_name") or request.wxid), bound_at, internal_account_id, rebind)
            binding = {"account_id": account_id, "internal_account_id": internal_account_id, "wxid": request.wxid,
                       "db_dir": requested_dir, "bound_at": bound_at}
            worker = WeChatWorker(self.repo, db, binding, float(os.getenv("WECHAT_POLL_INTERVAL", "5")), MessageValueClient())
            self.workers[account_id] = worker
            worker.start()
            return self.status(internal_account_id)

    def _start_binding(self, binding: dict[str, Any]) -> None:
        db, _ = self.open_db(binding["db_dir"], binding["wxid"])
        worker = WeChatWorker(self.repo, db, binding, float(os.getenv("WECHAT_POLL_INTERVAL", "5")), MessageValueClient())
        self.workers[binding["account_id"]] = worker
        worker.start()

    def restore(self) -> None:
        for binding in self.repo.active_bindings():
            try:
                self._start_binding(binding)
            except Exception as exc:
                self.repo.heartbeat(binding["account_id"], "error", failed=1, error=str(exc))

    def stop(self, internal_account_id: int) -> dict[str, Any]:
        with self.lock:
            binding = self.repo.active_binding(internal_account_id)
            worker = next((item for item in self.workers.values()
                           if item.binding.get("internal_account_id") == internal_account_id), None)
            if not binding and not worker:
                return {"status": "stopped", "wxid": None, "db_dir": None, "bound_at": None,
                        "last_collected_at": None, "last_error": None}
            account_id = binding["account_id"] if binding else worker.binding["account_id"]
            worker = self.workers.pop(account_id, worker)
            if worker: worker.stop()
            self.repo.disable(account_id)
            return self.status(internal_account_id)

    def worker_for_user(self, internal_account_id: int) -> WeChatWorker | None:
        binding = self.repo.active_binding(internal_account_id)
        return self.workers.get(binding["account_id"]) if binding else None

    def status(self, internal_account_id: int) -> dict[str, Any]:
        worker = self.worker_for_user(internal_account_id)
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
    if manager:
        for worker in list(manager.workers.values()): worker.stop()


def current() -> Manager:
    if manager is None: raise HTTPException(503, "collector is starting")
    return manager


@app.get("/health")
def health(): return {"service": "wechat-collector", "status": "ok"}


def internal_user(x_info_agent_user_id: int = Header(alias="X-Info-Agent-User-ID"),
                  x_info_agent_collector_token: str = Header(alias="X-Info-Agent-Collector-Token")) -> int:
    expected = os.getenv("COLLECTOR_INTERNAL_TOKEN", "local-development-only")
    if not expected or x_info_agent_collector_token != expected:
        raise HTTPException(401, "invalid collector credential")
    if x_info_agent_user_id <= 0:
        raise HTTPException(401, "invalid user identity")
    return x_info_agent_user_id


@app.post("/bind")
def bind(request: BindRequest, user_id: int = Depends(internal_user)):
    try: return current().bind(request, user_id, False)
    except RuntimeError as exc: raise HTTPException(409, str(exc)) from exc
    except ValueError as exc: raise HTTPException(422, str(exc)) from exc


@app.post("/rebind")
def rebind(request: BindRequest, user_id: int = Depends(internal_user)):
    try: return current().bind(request, user_id, True)
    except RuntimeError as exc: raise HTTPException(409, str(exc)) from exc
    except ValueError as exc: raise HTTPException(422, str(exc)) from exc


@app.get("/status")
def status(user_id: int = Depends(internal_user)): return current().status(user_id)


@app.post("/stop")
def stop(user_id: int = Depends(internal_user)): return current().stop(user_id)


@app.get("/conversations")
def conversations(search: str = "", conversation_type: str = "", page: int = 1, page_size: int = 50,
                  user_id: int = Depends(internal_user)):
    worker = current().worker_for_user(user_id)
    if not worker: return {"conversations": [], "page": page, "page_size": page_size, "total": 0}
    items = []
    needle = search.strip().lower()
    selected_ids = set(current().repo.get_config(worker.binding["account_id"]).get("selected_conversations", []))
    for session in worker.db.get_sessions(limit=1000):
        chat_id = str(session.get("username") or "")
        kind = "group" if chat_id.endswith("@chatroom") else "direct"
        name = resolve_session_name(worker.db, session, chat_id)
        avatar = str(session.get("avatar_url") or session.get("avatar") or session.get("head_img_url") or "")
        if conversation_type and conversation_type != kind: continue
        if needle and needle not in (chat_id + " " + name).lower(): continue
        items.append({"external_id": chat_id, "name": name, "avatar_url": avatar, "platform": "wechat", "conversation_type": kind, "selected": chat_id in selected_ids})
    start = max(0, (page - 1) * min(max(page_size, 1), 100))
    size = min(max(page_size, 1), 100)
    return {"conversations": items[start:start + size], "page": page, "page_size": size, "total": len(items)}


@app.get("/config")
def config(user_id: int = Depends(internal_user)):
    worker = current().worker_for_user(user_id)
    return current().repo.get_config(worker.binding["account_id"]) if worker else {"listen_mode": "whitelist", "selected_conversations": [], "history_start_at": None}


@app.put("/config")
def save_config(request: ConfigRequest, user_id: int = Depends(internal_user)):
    worker = current().worker_for_user(user_id)
    if not worker: raise HTTPException(503, "wechat collector is not bound")
    try:
        value = current().repo.save_config(worker.binding["account_id"], request.selected_conversations, request.history_start_at)
        worker.binding.update(value)
        return value
    except ValueError as exc: raise HTTPException(422, str(exc)) from exc
