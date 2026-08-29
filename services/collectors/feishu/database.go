package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func externalAccountID(value string) string {
	return strings.TrimPrefix(value, "feishu_")
}

func textBody(raw map[string]any) string {
	if raw["msg_type"] != "text" {
		return ""
	}
	body, _ := raw["body"].(map[string]any)
	content, _ := body["content"].(string)
	var parsed map[string]any
	if json.Unmarshal([]byte(content), &parsed) == nil {
		if value, ok := parsed["text"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(content)
}

func occurredAt(raw map[string]any) (*time.Time, string) {
	value := fmt.Sprint(raw["create_time"])
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, value
	}
	if n > 100000000000 {
		n /= 1000
	}
	t := time.Unix(n, 0).UTC()
	return &t, value
}

func canonicalID(account, message string) string {
	h := sha256.Sum256([]byte(account + ":" + message))
	return "msg_" + hex.EncodeToString(h[:])[:24]
}

func persistMessage(ctx context.Context, pool *pgxpool.Pool, accountExternal string, raw map[string]any) error {
	messageID, _ := raw["message_id"].(string)
	chatID, _ := raw["chat_id"].(string)
	if messageID == "" || chatID == "" {
		return fmt.Errorf("message_id and chat_id are required")
	}
	accountExternal = externalAccountID(accountExternal)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID int64
	err = tx.QueryRow(ctx, `SELECT id FROM ingestion.source_accounts WHERE platform='feishu' AND external_account_id=$1`, accountExternal).Scan(&accountID)
	if err != nil {
		return fmt.Errorf("source account %s not found: %w", accountExternal, err)
	}
	var conversationID int64
	payload, _ := json.Marshal(raw)
	err = tx.QueryRow(ctx, `INSERT INTO ingestion.conversations (source_account_id, platform, external_conversation_id, conversation_type, name, raw_payload, last_seen_at)
		VALUES ($1,'feishu',$2::varchar,CASE WHEN $2::text LIKE 'oc_%' THEN 'group' ELSE 'unknown' END,$3,$4,now())
		ON CONFLICT (source_account_id, external_conversation_id) DO UPDATE SET last_seen_at=now(), raw_payload=EXCLUDED.raw_payload, updated_at=now()
		RETURNING id`, accountID, chatID, raw["name"], payload).Scan(&conversationID)
	if err != nil {
		return fmt.Errorf("upsert conversation: %w", err)
	}
	sender, _ := raw["sender"].(map[string]any)
	senderID, _ := sender["id"].(string)
	var participantID any
	if senderID != "" {
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO ingestion.participants (source_account_id, external_participant_id, id_type, display_name, source)
			VALUES ($1,$2,$3,$4,'feishu') ON CONFLICT (source_account_id, external_participant_id)
			DO UPDATE SET display_name=COALESCE(EXCLUDED.display_name, ingestion.participants.display_name), updated_at=now() RETURNING id`, accountID, senderID, sender["id_type"], sender["name"]).Scan(&id)
		if err != nil {
			return fmt.Errorf("upsert participant: %w", err)
		}
		participantID = id
	}
	var rawID int64
	hash := sha256.Sum256(payload)
	err = tx.QueryRow(ctx, `INSERT INTO ingestion.raw_messages (source_account_id, conversation_id, source, source_message_id, collected_at, occurred_at_raw, raw_payload, content_hash)
		VALUES ($1,$2,'feishu',$3,now(),$4,$5,$6) ON CONFLICT (source_account_id, source_message_id)
		DO UPDATE SET raw_payload=EXCLUDED.raw_payload, updated_at=now() RETURNING id`, accountID, conversationID, messageID, fmt.Sprint(raw["create_time"]), payload, hex.EncodeToString(hash[:])).Scan(&rawID)
	if err != nil {
		return fmt.Errorf("upsert raw message: %w", err)
	}
	when, rawWhen := occurredAt(raw)
	var occurred any = when
	if when == nil {
		occurred = nil
	}
	metadata := map[string]any{"message_position": raw["message_position"], "tenant_key": sender["tenant_key"]}
	metadataJSON, _ := json.Marshal(metadata)
	_, err = tx.Exec(ctx, `INSERT INTO ingestion.messages (id, source, source_account_id, source_message_id, conversation_id, sender_id, occurred_at, occurred_at_raw, message_type, text, is_deleted, is_updated, metadata, raw_message_id)
		VALUES ($1,'feishu',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (source_account_id, source_message_id) DO UPDATE SET text=EXCLUDED.text, occurred_at=EXCLUDED.occurred_at, message_type=EXCLUDED.message_type, is_deleted=EXCLUDED.is_deleted, is_updated=EXCLUDED.is_updated, metadata=EXCLUDED.metadata, raw_message_id=EXCLUDED.raw_message_id, updated_at=now()`,
		canonicalID(accountExternal, messageID), accountID, messageID, conversationID, participantID, occurred, rawWhen, fmt.Sprint(raw["msg_type"]), textBody(raw), raw["deleted"] == true, raw["updated"] == true, metadataJSON, rawID)
	if err != nil {
		return fmt.Errorf("upsert canonical message: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion.worker_tasks (task_type, entity_id, payload)
        VALUES ('vectorization',$1,$2::jsonb) ON CONFLICT (task_type, entity_id) DO NOTHING`, canonicalID(accountExternal, messageID), payload); err != nil {
		return fmt.Errorf("enqueue vectorization: %w", err)
	}
	return tx.Commit(ctx)
}

func loadCheckpoint(ctx context.Context, pool *pgxpool.Pool, account string, conversation string) (string, error) {
	var token string
	err := pool.QueryRow(ctx, `SELECT COALESCE(cc.cursor,'') FROM ingestion.collector_checkpoints cc JOIN ingestion.conversations c ON c.id=cc.conversation_id WHERE cc.source_account_id=(SELECT id FROM ingestion.source_accounts WHERE platform='feishu' AND external_account_id=$1) AND c.external_conversation_id=$2`, externalAccountID(account), conversation).Scan(&token)
	if err != nil {
		return "", nil
	}
	return token, nil
}

func saveCheckpoint(ctx context.Context, pool *pgxpool.Pool, account, conversation, token, lastID string, lastTime *time.Time) error {
	_, err := pool.Exec(ctx, `INSERT INTO ingestion.collector_checkpoints(source_account_id,conversation_id,cursor,last_message_id,last_message_time,last_success_at,updated_at)
SELECT sa.id,c.id,$3,$4,$5,now(),now() FROM ingestion.source_accounts sa JOIN ingestion.conversations c ON c.source_account_id=sa.id AND c.external_conversation_id=$2 WHERE sa.platform='feishu' AND sa.external_account_id=$1
ON CONFLICT(source_account_id,conversation_id) DO UPDATE SET cursor=EXCLUDED.cursor,last_message_id=EXCLUDED.last_message_id,last_message_time=EXCLUDED.last_message_time,last_success_at=now(),updated_at=now()`, externalAccountID(account), conversation, token, lastID, lastTime)
	return err
}

func heartbeat(ctx context.Context, pool *pgxpool.Pool, name, status string, processed, failed int64, errText *string) {
	_, _ = pool.Exec(ctx, `INSERT INTO ingestion.worker_runs(name,status,last_heartbeat,last_error,processed_count,failed_count,updated_at)
VALUES($1,$2,now(),$3,$4,$5,now()) ON CONFLICT(name) DO UPDATE SET status=EXCLUDED.status,last_heartbeat=now(),last_error=EXCLUDED.last_error,processed_count=ingestion.worker_runs.processed_count+$4,failed_count=ingestion.worker_runs.failed_count+$5,updated_at=now()`, name, status, errText, processed, failed)
}
