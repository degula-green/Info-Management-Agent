from __future__ import annotations

import hashlib
import json
from datetime import datetime, timezone
from typing import Any

import psycopg
from psycopg.types.json import Jsonb


def canonical_id(account: str, source_message_id: str) -> str:
    value = hashlib.sha256(f"{account}:{source_message_id}".encode()).hexdigest()[:24]
    return f"msg_{value}"


def occurred_at(value: Any) -> datetime | None:
    try:
        number = int(value)
        if number > 10_000_000_000:
            number //= 1000
        return datetime.fromtimestamp(number, timezone.utc)
    except (TypeError, ValueError, OverflowError, OSError):
        return None


class WeChatRepository:
    def __init__(self, database_url: str):
        self.database_url = database_url

    def connect(self):
        return psycopg.connect(self.database_url)

    def ensure_schema(self) -> None:
        with self.connect() as conn:
            conn.execute("""CREATE TABLE IF NOT EXISTS ingestion.collector_bindings (
                id BIGSERIAL PRIMARY KEY,
                source_account_id BIGINT NOT NULL REFERENCES ingestion.source_accounts(id) ON DELETE CASCADE,
                collector_type VARCHAR(32) NOT NULL CHECK (collector_type IN ('wechat')),
                db_directory TEXT NOT NULL, bound_at TIMESTAMPTZ NOT NULL,
                enabled BOOLEAN NOT NULL DEFAULT true, last_error TEXT,
                created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                UNIQUE (collector_type), UNIQUE (source_account_id, collector_type))""")

    def bind(self, wxid: str, db_dir: str, account_name: str, bound_at: datetime) -> int:
        with self.connect() as conn:
            row = conn.execute("""INSERT INTO ingestion.source_accounts
                (internal_account_id,platform,external_account_id,account_name,credential_ref,status)
                VALUES(1,'wechat',%s,%s,%s,'active')
                ON CONFLICT(platform,external_account_id) DO UPDATE SET account_name=EXCLUDED.account_name,
                credential_ref=EXCLUDED.credential_ref,status='active',updated_at=now() RETURNING id""",
                (wxid, account_name or wxid, f"local-wechat:{wxid}"),).fetchone()
            account_id = int(row[0])
            conn.execute("UPDATE ingestion.collector_bindings SET enabled=false,updated_at=now() WHERE collector_type='wechat'")
            conn.execute("""INSERT INTO ingestion.collector_bindings
                (source_account_id,collector_type,db_directory,bound_at,enabled,last_error)
                VALUES(%s,'wechat',%s,%s,true,NULL)
                ON CONFLICT(collector_type) DO UPDATE SET source_account_id=EXCLUDED.source_account_id,
                db_directory=EXCLUDED.db_directory,bound_at=EXCLUDED.bound_at,enabled=true,last_error=NULL,updated_at=now()""",
                (account_id, db_dir, bound_at))
            return account_id

    def active_binding(self) -> dict[str, Any] | None:
        with self.connect() as conn:
            row = conn.execute("""SELECT b.source_account_id,sa.external_account_id,b.db_directory,b.bound_at
                FROM ingestion.collector_bindings b JOIN ingestion.source_accounts sa ON sa.id=b.source_account_id
                WHERE b.collector_type='wechat' AND b.enabled=true""").fetchone()
        return {"account_id": int(row[0]), "wxid": row[1], "db_dir": row[2], "bound_at": row[3]} if row else None

    def disable(self) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.collector_bindings SET enabled=false,updated_at=now() WHERE collector_type='wechat'")

    def reset_checkpoints(self, account_id: int) -> None:
        with self.connect() as conn:
            conn.execute("DELETE FROM ingestion.collector_checkpoints WHERE source_account_id=%s", (account_id,))

    def upsert_conversation(self, account_id: int, session: dict[str, Any]) -> int:
        chat_id = str(session.get("username") or "")
        kind = "group" if chat_id.endswith("@chatroom") else "direct"
        with self.connect() as conn:
            row = conn.execute("""INSERT INTO ingestion.conversations
                (source_account_id,platform,external_conversation_id,conversation_type,name,raw_payload,last_seen_at)
                VALUES(%s,'wechat',%s,%s,%s,%s,now())
                ON CONFLICT(source_account_id,external_conversation_id) DO UPDATE SET
                raw_payload=EXCLUDED.raw_payload,last_seen_at=now(),updated_at=now() RETURNING id""",
                (account_id, chat_id, kind, chat_id, Jsonb(session))).fetchone()
            return int(row[0])

    def checkpoint(self, account_id: int, conversation_id: int) -> int | None:
        with self.connect() as conn:
            row = conn.execute("SELECT last_sort_seq FROM ingestion.collector_checkpoints WHERE source_account_id=%s AND conversation_id=%s", (account_id, conversation_id)).fetchone()
        if not row or row[0] in (None, ""): return None
        try: return int(row[0])
        except (TypeError, ValueError): return None

    def save_checkpoint(self, account_id: int, conversation_id: int, seq: int, message_id: str | None = None, message_time: datetime | None = None) -> None:
        with self.connect() as conn:
            conn.execute("""INSERT INTO ingestion.collector_checkpoints
                (source_account_id,conversation_id,cursor,last_message_id,last_message_time,last_sort_seq,last_success_at,retry_count)
                VALUES(%s,%s,%s,%s,%s,%s,now(),0)
                ON CONFLICT(source_account_id,conversation_id) DO UPDATE SET cursor=EXCLUDED.cursor,
                last_message_id=COALESCE(EXCLUDED.last_message_id,ingestion.collector_checkpoints.last_message_id),
                last_message_time=COALESCE(EXCLUDED.last_message_time,ingestion.collector_checkpoints.last_message_time),
                last_sort_seq=EXCLUDED.last_sort_seq,last_success_at=now(),last_error=NULL,retry_count=0,updated_at=now()""",
                (account_id, conversation_id, str(seq), message_id, message_time, str(seq)))

    def persist_message(self, account_id: int, conversation_id: int, wxid: str, chat_id: str, raw: dict[str, Any]) -> bool:
        local_id = str(raw.get("local_id") or "")
        if not local_id: return False
        source_message_id = f"{chat_id}:{local_id}"
        message_id = canonical_id(wxid, source_message_id)
        payload = Jsonb({**raw, "chat_id": chat_id})
        when = occurred_at(raw.get("create_time"))
        message_type = str(raw.get("type") or "unknown").strip().lower()
        # wechatauto returns localized type names for WeChat messages. Keep the
        # canonical ingestion contract in English while preserving text content.
        type_aliases = {
            "文本": "text", "文字": "text", "text": "text",
            "图片": "image", "image": "image", "文件": "file", "file": "file",
            "语音": "audio", "audio": "audio", "视频": "video", "video": "video",
            "链接": "link", "link": "link", "系统消息": "system", "system": "system",
        }
        message_type = type_aliases.get(message_type, message_type)
        allowed = {"text", "image", "file", "audio", "video", "link", "system"}
        canonical_type = message_type if message_type in allowed else "unknown"
        content = str(raw.get("content") or "")
        text = content if canonical_type == "text" else (content if canonical_type == "unknown" and content else "")
        sender = str(raw.get("sender_username") or raw.get("sender_id") or "")
        with self.connect() as conn:
            participant_id = None
            if sender:
                participant_id = conn.execute("""INSERT INTO ingestion.participants
                    (source_account_id,external_participant_id,id_type,display_name,source)
                    VALUES(%s,%s,'wechat',%s,'wechat') ON CONFLICT(source_account_id,external_participant_id)
                    DO UPDATE SET updated_at=now() RETURNING id""", (account_id, sender, sender)).fetchone()[0]
            content_hash = hashlib.sha256(json.dumps(raw, ensure_ascii=False, default=str).encode()).hexdigest()
            raw_row = conn.execute("""INSERT INTO ingestion.raw_messages
                (source_account_id,conversation_id,source,source_message_id,collected_at,occurred_at_raw,raw_payload,content_hash)
                VALUES(%s,%s,'wechat',%s,now(),%s,%s,%s)
                ON CONFLICT(source_account_id,source_message_id) DO NOTHING RETURNING id""",
                (account_id, conversation_id, source_message_id, str(raw.get("create_time") or ""), payload, content_hash)).fetchone()
            if not raw_row: return False
            raw_id = int(raw_row[0])
            conn.execute("""INSERT INTO ingestion.messages
                (id,source,source_account_id,source_message_id,conversation_id,sender_id,occurred_at,
                occurred_at_raw,message_type,text,metadata,raw_message_id)
                VALUES(%s,'wechat',%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
                ON CONFLICT(source_account_id,source_message_id) DO NOTHING""",
                (message_id, account_id, source_message_id, conversation_id, participant_id, when,
                 str(raw.get("create_time") or ""), canonical_type, text, Jsonb({"sort_seq": raw.get("sort_seq")}), raw_id))
            conn.execute("""INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload)
                VALUES('vectorization',%s,%s) ON CONFLICT(task_type,entity_id) DO NOTHING""",
                (message_id, Jsonb({"source": "wechat", "source_message_id": source_message_id})))
            return True

    def heartbeat(self, status: str, processed: int = 0, failed: int = 0, error: str | None = None) -> None:
        with self.connect() as conn:
            conn.execute("""INSERT INTO ingestion.worker_runs
                (name,status,last_heartbeat,last_error,processed_count,failed_count,updated_at)
                VALUES('wechat-collector',%s,now(),%s,%s,%s,now())
                ON CONFLICT(name) DO UPDATE SET status=EXCLUDED.status,last_heartbeat=now(),
                last_error=EXCLUDED.last_error,processed_count=ingestion.worker_runs.processed_count+EXCLUDED.processed_count,
                failed_count=ingestion.worker_runs.failed_count+EXCLUDED.failed_count,updated_at=now()""",
                (status, error, processed, failed))
