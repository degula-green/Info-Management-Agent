from __future__ import annotations

import hashlib
import json
import re
from datetime import datetime, timezone
from typing import Any

import psycopg
from psycopg.types.json import Jsonb
from services.collectors.attachments import extract_attachments
from services.collectors.wechat.attachments import SUPPORTED_EXTENSIONS


def canonical_message_type(raw_type: Any) -> str:
    value = str(raw_type or "").strip().lower()
    aliases = {"文本": "text", "文字": "text", "text": "text", "图片": "image", "image": "image",
               "文件": "file", "file": "file", "49": "file", "语音": "audio", "audio": "audio", "视频": "video", "video": "video",
               "链接": "link", "link": "link", "系统消息": "system", "system": "system", "图文": "mixed", "mixed": "mixed"}
    return aliases.get(value, "unknown")


def canonical_type_from_message(raw: dict[str, Any]) -> str:
    value = canonical_message_type(raw.get("type") or raw.get("local_type") or raw.get("message_type"))
    if value != "unknown":
        return value
    content = str(raw.get("content") or "")
    if re.search(r"<appmsg\b[^>]*>.*?<type>\s*6\s*</type>", content, re.I | re.S):
        return "file"
    return value


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


def display_name(value: Any, external_id: str) -> str | None:
    candidate = str(value or "").strip()
    if not candidate or candidate.lower() == external_id.lower() or candidate.lower().startswith("wxid_"):
        return None
    return candidate


def conversation_display_name(session: dict[str, Any], external_id: str) -> str:
    """Resolve a human-readable WeChat conversation name.

    WeChat databases often expose wxid/gh_* identifiers in ``nickname``.
    Never persist those identifiers as the display name when a remark or
    chat-room title is available.
    """
    keys = (
        ("remark_name", "remark", "contact_remark", "alias")
        if not external_id.endswith("@chatroom")
        else ("chatroom_name", "room_name", "display_name", "nickname", "remark_name", "remark")
    ) + ("nickname", "display_name")
    for key in keys:
        value = display_name(session.get(key), external_id)
        if value:
            return value
    return external_id


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
                listen_mode VARCHAR(16) NOT NULL DEFAULT 'whitelist',
                selected_conversations JSONB NOT NULL DEFAULT '[]'::jsonb,
                history_start_at TIMESTAMPTZ,
                config_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                enabled BOOLEAN NOT NULL DEFAULT true, last_error TEXT,
                created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                UNIQUE (source_account_id, collector_type))""")
            conn.execute("ALTER TABLE ingestion.collector_bindings DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_key")
            conn.execute("""UPDATE ingestion.collector_bindings b SET collector_type='wechat',updated_at=now()
                WHERE b.collector_type='personal_wechat' AND NOT EXISTS (
                  SELECT 1 FROM ingestion.collector_bindings existing
                  WHERE existing.id<>b.id AND existing.source_account_id=b.source_account_id
                    AND existing.collector_type='wechat')""")
            conn.execute("ALTER TABLE ingestion.collector_bindings DROP CONSTRAINT IF EXISTS collector_bindings_collector_type_check")
            conn.execute("ALTER TABLE ingestion.collector_bindings ADD CONSTRAINT collector_bindings_collector_type_check CHECK (collector_type IN ('wechat','feishu'))")
            conn.execute("""ALTER TABLE ingestion.collector_bindings
                ADD COLUMN IF NOT EXISTS listen_mode VARCHAR(16) NOT NULL DEFAULT 'whitelist',
                ADD COLUMN IF NOT EXISTS selected_conversations JSONB NOT NULL DEFAULT '[]'::jsonb,
                ADD COLUMN IF NOT EXISTS history_start_at TIMESTAMPTZ,
                ADD COLUMN IF NOT EXISTS config_updated_at TIMESTAMPTZ NOT NULL DEFAULT now()""")
            conn.execute("ALTER TABLE ingestion.worker_runs ADD COLUMN IF NOT EXISTS source_account_id BIGINT REFERENCES ingestion.source_accounts(id) ON DELETE CASCADE")
            conn.execute("ALTER TABLE ingestion.worker_tasks DROP CONSTRAINT IF EXISTS worker_tasks_task_type_check")
            conn.execute("ALTER TABLE ingestion.worker_tasks ADD CONSTRAINT worker_tasks_task_type_check CHECK (task_type IN ('vectorization','collector','attachment_download','attachment_parse'))")

    def bind(self, wxid: str, db_dir: str, account_name: str, bound_at: datetime, internal_account_id: int = 1, rebind: bool = False) -> int:
        with self.connect() as conn:
            existing = conn.execute("""SELECT id,internal_account_id,status FROM ingestion.source_accounts
                WHERE platform='wechat' AND external_account_id=%s FOR UPDATE""", (wxid,)).fetchone()
            if existing and int(existing[1]) != internal_account_id:
                raise RuntimeError("wechat account is already bound to another user")
            active = conn.execute("""SELECT id FROM ingestion.source_accounts WHERE internal_account_id=%s
                AND platform='wechat' AND status='active' LIMIT 1 FOR UPDATE""", (internal_account_id,)).fetchone()
            if active and not rebind and (not existing or int(active[0]) != int(existing[0])):
                raise RuntimeError("wechat connector is already bound")
            if rebind:
                conn.execute("""UPDATE ingestion.collector_bindings b SET enabled=false,updated_at=now()
                    FROM ingestion.source_accounts sa WHERE b.source_account_id=sa.id
                    AND sa.internal_account_id=%s AND sa.platform='wechat'""", (internal_account_id,))
                conn.execute("UPDATE ingestion.source_accounts SET status='inactive',updated_at=now() WHERE internal_account_id=%s AND platform='wechat'", (internal_account_id,))
            if existing:
                account_id = int(existing[0])
                conn.execute("""UPDATE ingestion.source_accounts SET account_name=%s,credential_ref=%s,status='active',updated_at=now()
                    WHERE id=%s""", (account_name or wxid, f"local-wechat:{wxid}", account_id))
            else:
                row = conn.execute("""INSERT INTO ingestion.source_accounts
                    (internal_account_id,platform,external_account_id,account_name,credential_ref,status)
                    VALUES(%s,'wechat',%s,%s,%s,'active') RETURNING id""",
                    (internal_account_id, wxid, account_name or wxid, f"local-wechat:{wxid}"),).fetchone()
                account_id = int(row[0])
            conn.execute("""INSERT INTO ingestion.collector_bindings
                (source_account_id,collector_type,db_directory,bound_at,enabled,last_error,listen_mode,selected_conversations,history_start_at,config_updated_at)
                VALUES(%s,'wechat',%s,%s,true,NULL,'whitelist','[]'::jsonb,NULL,now())
                ON CONFLICT(source_account_id,collector_type) DO UPDATE SET
                db_directory=EXCLUDED.db_directory,bound_at=EXCLUDED.bound_at,enabled=true,last_error=NULL,updated_at=now()""",
                (account_id, db_dir, bound_at))
            return account_id

    def active_bindings(self, internal_account_id: int | None = None) -> list[dict[str, Any]]:
        with self.connect() as conn:
            rows = conn.execute("""SELECT b.source_account_id,sa.internal_account_id,sa.external_account_id,b.db_directory,b.bound_at,
                b.selected_conversations,b.history_start_at,b.config_updated_at
                FROM ingestion.collector_bindings b JOIN ingestion.source_accounts sa ON sa.id=b.source_account_id
                WHERE b.collector_type='wechat' AND b.enabled=true AND sa.status='active'
                  AND (%s::bigint IS NULL OR sa.internal_account_id=%s)
                ORDER BY b.id""", (internal_account_id, internal_account_id)).fetchall()
        return [{"account_id": int(row[0]), "internal_account_id": int(row[1]), "wxid": row[2], "db_dir": row[3], "bound_at": row[4],
                "selected_conversations": row[5] or [], "history_start_at": row[6], "config_updated_at": row[7]} for row in rows]

    def active_binding(self, internal_account_id: int | None = None) -> dict[str, Any] | None:
        rows = self.active_bindings(internal_account_id)
        return rows[0] if rows else None

    def get_config(self, account_id: int) -> dict[str, Any]:
        with self.connect() as conn:
            row = conn.execute("SELECT listen_mode,selected_conversations,history_start_at,config_updated_at,enabled FROM ingestion.collector_bindings WHERE source_account_id=%s AND collector_type='wechat'", (account_id,)).fetchone()
        return {"listen_mode": row[0], "selected_conversations": row[1] or [], "history_start_at": row[2], "config_updated_at": row[3], "enabled": row[4]} if row else {"listen_mode": "whitelist", "selected_conversations": [], "history_start_at": None, "config_updated_at": None, "enabled": False}

    def save_config(self, account_id: int, selected: list[str], history_start_at: datetime | None) -> dict[str, Any]:
        with self.connect() as conn:
            row = conn.execute("""UPDATE ingestion.collector_bindings SET listen_mode='whitelist',selected_conversations=%s::jsonb,
                history_start_at=%s,config_updated_at=now(),updated_at=now() WHERE source_account_id=%s AND collector_type='wechat'
                RETURNING listen_mode,selected_conversations,history_start_at,config_updated_at""", (json.dumps(selected), history_start_at, account_id)).fetchone()
        if not row: raise ValueError("wechat binding not found")
        return {"listen_mode": row[0], "selected_conversations": row[1] or [], "history_start_at": row[2], "config_updated_at": row[3]}

    def disable(self, account_id: int) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.collector_bindings SET enabled=false,updated_at=now() WHERE source_account_id=%s AND collector_type='wechat'", (account_id,))

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
                name=CASE WHEN EXCLUDED.name <> EXCLUDED.external_conversation_id
                          THEN EXCLUDED.name ELSE ingestion.conversations.name END,
                raw_payload=EXCLUDED.raw_payload,last_seen_at=now(),updated_at=now() RETURNING id""",
            (account_id, chat_id, kind, conversation_display_name(session, chat_id), Jsonb(session))).fetchone()
            return int(row[0])

    def checkpoint(self, account_id: int, conversation_id: int) -> int | None:
        with self.connect() as conn:
            row = conn.execute("SELECT last_sort_seq FROM ingestion.collector_checkpoints WHERE source_account_id=%s AND conversation_id=%s", (account_id, conversation_id)).fetchone()
        if not row or row[0] in (None, ""): return None
        try: return int(row[0])
        except (TypeError, ValueError): return None

    def unresolved_participants(self, account_id: int) -> list[str]:
        with self.connect() as conn:
            rows = conn.execute("""SELECT external_participant_id FROM ingestion.participants
                WHERE source_account_id=%s AND source='wechat'
                  AND (COALESCE(btrim(display_name),'')='' OR lower(display_name)=lower(external_participant_id))""", (account_id,)).fetchall()
        return [str(row[0]) for row in rows if row[0]]

    def update_participant_display_name(self, account_id: int, participant_id: str, name: str) -> None:
        valid_name = display_name(name, participant_id)
        if not valid_name:
            return
        with self.connect() as conn:
            conn.execute("""UPDATE ingestion.participants SET display_name=%s,updated_at=now()
                WHERE source_account_id=%s AND source='wechat' AND external_participant_id=%s""",
                         (valid_name, account_id, participant_id))

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

    def persist_message(self, account_id: int, conversation_id: int, wxid: str, chat_id: str, raw: dict[str, Any], parsed_attachments: list[dict[str, Any]] | None = None) -> bool:
        local_id = str(raw.get("local_id") or "")
        if not local_id: return False
        source_message_id = f"{chat_id}:{local_id}"
        message_id = canonical_id(wxid, source_message_id)
        payload = Jsonb({**raw, "chat_id": chat_id})
        when = occurred_at(raw.get("create_time"))
        source_message_type = str(raw.get("type") or raw.get("local_type") or "unknown").strip().lower()
        # wechatauto returns localized type names for WeChat messages. Keep the
        # canonical ingestion contract in English while preserving text content.
        type_aliases = {
            "文本": "text", "文字": "text", "text": "text",
            "图片": "image", "image": "image", "文件": "file", "file": "file",
            "语音": "audio", "audio": "audio", "视频": "video", "video": "video",
            "链接": "link", "link": "link", "系统消息": "system", "system": "system",
        }
        canonical_type = canonical_type_from_message(raw)
        content = str(raw.get("content") or "")
        text = content if canonical_type == "text" else (content if canonical_type == "unknown" and content else "")
        sender = str(raw.get("sender_username") or raw.get("real_sender_id") or raw.get("sender_id") or "")
        sender_display_name = display_name(raw.get("sender_name"), sender)
        with self.connect() as conn:
            participant_id = None
            if sender:
                participant_id = conn.execute("""INSERT INTO ingestion.participants
                    (source_account_id,external_participant_id,id_type,display_name,source)
                    VALUES(%s,%s,'wechat',%s,'wechat') ON CONFLICT(source_account_id,external_participant_id)
                    DO UPDATE SET display_name=COALESCE(EXCLUDED.display_name,ingestion.participants.display_name),
                    updated_at=now() RETURNING id""", (account_id, sender, sender_display_name)).fetchone()[0]
            content_hash = hashlib.sha256(json.dumps(raw, ensure_ascii=False, default=str).encode()).hexdigest()
            raw_row = conn.execute("""INSERT INTO ingestion.raw_messages
                (source_account_id,conversation_id,source,source_message_id,collected_at,occurred_at_raw,raw_payload,content_hash)
                VALUES(%s,%s,'wechat',%s,now(),%s,%s,%s)
                ON CONFLICT(source_account_id,source_message_id) DO NOTHING RETURNING id""",
                (account_id, conversation_id, source_message_id, str(raw.get("create_time") or ""), payload, content_hash)).fetchone()
            is_new_message = bool(raw_row)
            if raw_row:
                raw_id = int(raw_row[0])
            else:
                existing_row = conn.execute("SELECT id FROM ingestion.raw_messages WHERE source_account_id=%s AND source_message_id=%s", (account_id, source_message_id)).fetchone()
                if not existing_row:
                    return False
                # Idempotent replays still need to repair attachments that may
                # have been missed by an older parser version.
                raw_id = int(existing_row[0])
            attachments = parsed_attachments if parsed_attachments is not None else extract_attachments(raw)
            conn.execute("""INSERT INTO ingestion.messages
                (id,source,source_account_id,source_message_id,conversation_id,sender_id,occurred_at,
                occurred_at_raw,message_type,source_message_type,text,metadata,raw_message_id)
                VALUES(%s,'wechat',%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
                ON CONFLICT(source_account_id,source_message_id) DO UPDATE SET message_type=EXCLUDED.message_type,source_message_type=EXCLUDED.source_message_type,text=EXCLUDED.text,metadata=EXCLUDED.metadata,updated_at=now()""",
                (message_id, account_id, source_message_id, conversation_id, participant_id, when,
                 str(raw.get("create_time") or ""), canonical_type, source_message_type, text, Jsonb({"sort_seq": raw.get("sort_seq"), "attachments": attachments}), raw_id))
            for index, attachment in enumerate(attachments):
                external_attachment_id = attachment.get("external_attachment_id") or f"{message_id}:{index}"
                supported = attachment.get("extension", "").lower() in SUPPORTED_EXTENSIONS and not attachment.get("unsupported")
                parse_status = "not_required"
                download_status = "downloading" if supported else ("failed" if attachment.get("parse_error") else "not_started")
                last_error = attachment.get("parse_error")
                conn.execute("""INSERT INTO ingestion.attachments
                    (message_id,raw_message_id,source_account_id,user_id,platform,external_attachment_id,file_name,extension,mime_type,file_category,file_size,preview_capability,download_status,parse_status,last_error)
                    VALUES(%s,%s,%s,(SELECT internal_account_id FROM ingestion.source_accounts WHERE id=%s),'wechat',%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
                    ON CONFLICT(source_account_id,external_attachment_id) DO UPDATE SET file_name=EXCLUDED.file_name,mime_type=EXCLUDED.mime_type,file_category=EXCLUDED.file_category,file_size=EXCLUDED.file_size,preview_capability=EXCLUDED.preview_capability,download_status=CASE WHEN ingestion.attachments.download_status='completed' THEN 'completed' ELSE EXCLUDED.download_status END,parse_status=EXCLUDED.parse_status,last_error=EXCLUDED.last_error,updated_at=now()""",
                    (message_id, raw_id, account_id, account_id, external_attachment_id, attachment.get("file_name"), attachment.get("extension"), attachment.get("mime_type"), attachment.get("file_category", "unknown"), attachment.get("file_size"), attachment.get("preview_capability", "pending"), download_status, parse_status, last_error))
                if supported:
                    attachment_id = conn.execute("SELECT id FROM ingestion.attachments WHERE source_account_id=%s AND external_attachment_id=%s", (account_id, external_attachment_id)).fetchone()[0]
                    conn.execute("""INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload) VALUES('attachment_download',%s,%s)
                        ON CONFLICT(task_type,entity_id) DO NOTHING""", (f"attachment:{attachment_id}", Jsonb({"platform":"wechat","attachment_id":attachment_id})))
            conn.execute("""INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload)
                VALUES('vectorization',%s,%s) ON CONFLICT(task_type,entity_id) DO NOTHING""",
                (message_id, Jsonb({"source": "wechat", "source_message_id": source_message_id})))
            return is_new_message

    def heartbeat(self, account_id: int, status: str, processed: int = 0, failed: int = 0, error: str | None = None) -> None:
        with self.connect() as conn:
            conn.execute("""INSERT INTO ingestion.worker_runs
                (name,source_account_id,status,last_heartbeat,last_error,processed_count,failed_count,updated_at)
                VALUES(%s,%s,%s,now(),%s,%s,%s,now())
                ON CONFLICT(name) DO UPDATE SET status=EXCLUDED.status,last_heartbeat=now(),
                source_account_id=EXCLUDED.source_account_id,last_error=EXCLUDED.last_error,processed_count=ingestion.worker_runs.processed_count+EXCLUDED.processed_count,
                failed_count=ingestion.worker_runs.failed_count+EXCLUDED.failed_count,updated_at=now()""",
                (f"wechat-collector:{account_id}", account_id, status, error, processed, failed))

    def claim_attachment_download_tasks(self, worker_id: str, limit: int = 4) -> list[dict[str, Any]]:
        with self.connect() as conn:
            conn.execute("""UPDATE ingestion.worker_tasks SET status='pending',locked_by=NULL,locked_until=NULL,updated_at=now()
                WHERE task_type='attachment_download' AND status='processing' AND locked_until<now()
                  AND payload->>'platform'='wechat'""")
            rows = conn.execute("""WITH picked AS (
                SELECT id FROM ingestion.worker_tasks
                WHERE task_type='attachment_download' AND status='pending' AND next_run_at<=now()
                  AND payload->>'platform'='wechat'
                ORDER BY id LIMIT %s FOR UPDATE SKIP LOCKED)
                UPDATE ingestion.worker_tasks t SET status='processing',attempts=attempts+1,locked_by=%s,
                    locked_until=now()+interval '10 minutes',updated_at=now()
                FROM picked WHERE t.id=picked.id
                RETURNING t.id,t.entity_id,t.payload""", (limit, worker_id)).fetchall()
        return [{"task_id": int(row[0]), "entity_id": row[1], "payload": row[2]} for row in rows]

    def get_attachment_context(self, attachment_id: int) -> dict[str, Any]:
        with self.connect() as conn:
            row = conn.execute("""SELECT a.id,a.file_name,a.extension,a.mime_type,a.download_status,
                m.id,m.source_message_id,m.occurred_at,c.id,c.external_conversation_id,r.raw_payload,sa.internal_account_id
                FROM ingestion.attachments a JOIN ingestion.messages m ON m.id=a.message_id
                JOIN ingestion.conversations c ON c.id=m.conversation_id
                JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id
                JOIN ingestion.raw_messages r ON r.id=a.raw_message_id WHERE a.id=%s""", (attachment_id,)).fetchone()
        if not row:
            raise ValueError(f"attachment not found: {attachment_id}")
        source_id = str(row[6])
        return {"id": row[0], "file_name": row[1], "extension": row[2], "mime_type": row[3], "download_status": row[4], "message_id": str(row[5]), "source_message_id": source_id, "occurred_at": row[7], "conversation_id": str(row[8]), "chat_id": row[9], "raw_payload": row[10], "user_id": str(row[11]), "local_id": int(source_id.rsplit(":", 1)[-1])}

    def complete_attachment_download(self, attachment_id: int, task_id: int, size: int, digest: str, bucket: str, storage_key: str) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.attachments SET file_size=%s,content_hash=%s,storage_provider='minio',storage_bucket=%s,storage_key=%s,download_status='completed',parse_status='pending',last_error=NULL,updated_at=now() WHERE id=%s", (size, digest, bucket, storage_key, attachment_id))
            conn.execute("UPDATE ingestion.worker_tasks SET status='completed',locked_by=NULL,locked_until=NULL,completed_at=now(),last_error=NULL,updated_at=now() WHERE id=%s", (task_id,))
            conn.execute("INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload) VALUES('attachment_parse',%s,%s) ON CONFLICT(task_type,entity_id) DO UPDATE SET payload=EXCLUDED.payload,status='pending',next_run_at=now(),locked_by=NULL,locked_until=NULL,last_error=NULL,completed_at=NULL,updated_at=now()", (f"attachment:{attachment_id}", Jsonb({"platform":"wechat","attachment_id":attachment_id})))

    def fail_attachment_download(self, attachment_id: int, task_id: int, error: str) -> None:
        with self.connect() as conn:
            conn.execute("UPDATE ingestion.attachments SET download_status='failed',last_error=%s,updated_at=now() WHERE id=%s", (error[:4000], attachment_id))
            conn.execute("""UPDATE ingestion.worker_tasks SET status=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'pending' END,
                next_run_at=now() + (LEAST(3600, 60 * (2 ^ LEAST(attempts-1, 6))) * interval '1 second'),locked_by=NULL,locked_until=NULL,last_error=%s,updated_at=now() WHERE id=%s""", (error[:4000], task_id))

    def dead_attachment_download_ids(self) -> set[int]:
        with self.connect() as conn:
            rows = conn.execute("SELECT (payload->>'attachment_id')::bigint FROM ingestion.worker_tasks WHERE task_type='attachment_download' AND status='dead' AND payload->>'platform'='wechat'").fetchall()
        return {int(row[0]) for row in rows if row[0] is not None}
