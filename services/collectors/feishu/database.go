package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

func canonicalMessageType(rawType string) string {
	switch strings.ToLower(strings.TrimSpace(rawType)) {
	case "text":
		return "text"
	case "file", "media":
		return "file"
	case "image":
		return "image"
	case "audio":
		return "audio"
	case "video":
		return "video"
	case "post":
		return "mixed"
	case "interactive", "share_chat":
		return "mixed"
	default:
		return "unknown"
	}
}

func classifyFeishuAttachment(name, mime string) (string, string) {
	ext := strings.ToLower(filepath.Ext(name))
	mime = strings.ToLower(mime)
	if ext == ".zip" || ext == ".rar" || ext == ".7z" || ext == ".tar" || ext == ".gz" {
		return "archive", "download_only"
	}
	if ext == ".exe" || ext == ".msi" || ext == ".dmg" || ext == ".apk" || ext == ".ipa" || ext == ".deb" || ext == ".rpm" {
		return "installer", "download_only"
	}
	if strings.HasPrefix(mime, "image/") {
		return "image", "inline"
	}
	if strings.HasPrefix(mime, "audio/") {
		return "audio", "inline"
	}
	if strings.HasPrefix(mime, "video/") {
		return "video", "inline"
	}
	if ext == ".pdf" || ext == ".txt" || ext == ".md" || ext == ".csv" {
		return "document", "inline"
	}
	if ext == ".doc" || ext == ".docx" || ext == ".xls" || ext == ".xlsx" || ext == ".ppt" || ext == ".pptx" {
		return "document", "rendered"
	}
	return "unknown", "pending"
}

type attachmentExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func persistFeishuAttachments(ctx context.Context, tx attachmentExecer, raw map[string]any, messageID string, rawID, accountID, conversationID int64) error {
	values, err := parseFeishuAttachments(raw)
	if err != nil {
		return err
	}
	for index, value := range values {
		externalID := fmt.Sprintf("synthetic:%s:%d", messageID, index)
		if value.ResourceKey != nil {
			externalID = *value.ResourceKey
		}
		name, mime := "", ""
		if value.FileName != nil {
			name = *value.FileName
		}
		if value.MIMEType != nil {
			mime = *value.MIMEType
		}
		category, preview := classifyFeishuAttachment(name, mime)
		var fileName, mimeType, fileSize any
		if value.FileName != nil {
			fileName = *value.FileName
		}
		if value.MIMEType != nil {
			mimeType = *value.MIMEType
		}
		if value.FileSize != nil {
			fileSize = *value.FileSize
		}
		var extension any
		if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), "."); ext != "" {
			extension = ext
		}
		parseStatus := "not_required"
		if extensionValue, ok := extension.(string); ok && map[string]bool{
			"pdf": true, "docx": true, "xlsx": true, "pptx": true,
			"txt": true, "md": true, "csv": true,
		}[extensionValue] {
			parseStatus = "pending"
		}
		_, err := tx.Exec(ctx, `INSERT INTO ingestion.attachments(message_id,raw_message_id,source_account_id,user_id,platform,external_attachment_id,file_name,extension,mime_type,file_category,file_size,preview_capability,parse_status)
            VALUES($1,$2,$3,(SELECT internal_account_id FROM ingestion.source_accounts WHERE id=$3),'feishu',$4,$5,$6,$7,$8,$9,$10,$11)
            ON CONFLICT(source_account_id,external_attachment_id) DO UPDATE SET file_name=EXCLUDED.file_name,mime_type=EXCLUDED.mime_type,file_category=EXCLUDED.file_category,file_size=EXCLUDED.file_size,preview_capability=EXCLUDED.preview_capability,parse_status=EXCLUDED.parse_status,updated_at=now()`,
			messageID, rawID, accountID, externalID, fileName, extension, mimeType, category, fileSize, preview, parseStatus)
		if err != nil {
			return err
		}
		if value.ResourceKey != nil && parseStatus == "pending" {
			_, err = tx.Exec(ctx, `INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload)
                SELECT 'attachment_download','attachment:'||a.id,jsonb_build_object('platform','feishu','attachment_id',a.id)
                FROM ingestion.attachments a WHERE a.source_account_id=$1 AND a.external_attachment_id=$2
                ON CONFLICT(task_type,entity_id) DO NOTHING`, accountID, externalID)
			if err != nil {
				return err
			}
		}
	}
	return nil
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

func participantDisplayName(value any) string {
	name, _ := value.(string)
	return strings.TrimSpace(name)
}

func participantAvatarURL(value any) string {
	switch avatar := value.(type) {
	case string:
		return strings.TrimSpace(avatar)
	case map[string]any:
		for _, key := range []string{"avatar_origin", "avatar_640", "avatar_240", "avatar_72", "url"} {
			if url, ok := avatar[key].(string); ok && strings.TrimSpace(url) != "" {
				return strings.TrimSpace(url)
			}
		}
	}
	return ""
}

func updateFeishuParticipantProfile(ctx context.Context, pool *pgxpool.Pool, accountExternal, participantExternal, name, avatar string) error {
	if participantExternal == "" || (name == "" && avatar == "") {
		return nil
	}
	_, err := pool.Exec(ctx, `UPDATE ingestion.participants p SET
        display_name=CASE WHEN $3<>'' THEN $3 ELSE p.display_name END,
        avatar_url=CASE WHEN $4<>'' THEN $4 ELSE p.avatar_url END,
        updated_at=now()
        FROM ingestion.source_accounts sa
        WHERE p.source_account_id=sa.id AND sa.platform='feishu'
          AND sa.external_account_id=$1 AND p.external_participant_id=$2`, externalAccountID(accountExternal), participantExternal, name, avatar)
	return err
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
		senderName := participantDisplayName(sender["name"])
		senderAvatar := participantAvatarURL(sender["avatar"])
		err = tx.QueryRow(ctx, `INSERT INTO ingestion.participants (source_account_id, external_participant_id, id_type, display_name, avatar_url, source)
			VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),'feishu') ON CONFLICT (source_account_id, external_participant_id)
			DO UPDATE SET display_name=COALESCE(NULLIF(EXCLUDED.display_name,''), ingestion.participants.display_name),
            avatar_url=COALESCE(NULLIF(EXCLUDED.avatar_url,''), ingestion.participants.avatar_url),
            updated_at=now() RETURNING id`, accountID, senderID, sender["id_type"], senderName, senderAvatar).Scan(&id)
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
	rawType := fmt.Sprint(raw["msg_type"])
	canonicalType := canonicalMessageType(rawType)
	_, err = tx.Exec(ctx, `INSERT INTO ingestion.messages (id, source, source_account_id, source_message_id, conversation_id, sender_id, occurred_at, occurred_at_raw, message_type, source_message_type, text, is_deleted, is_updated, metadata, raw_message_id)
		VALUES ($1,'feishu',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (source_account_id, source_message_id) DO UPDATE SET text=EXCLUDED.text, occurred_at=EXCLUDED.occurred_at, message_type=EXCLUDED.message_type, source_message_type=EXCLUDED.source_message_type, is_deleted=EXCLUDED.is_deleted, is_updated=EXCLUDED.is_updated, metadata=EXCLUDED.metadata, raw_message_id=EXCLUDED.raw_message_id, updated_at=now()`,
		canonicalID(accountExternal, messageID), accountID, messageID, conversationID, participantID, occurred, rawWhen, canonicalType, rawType, textBody(raw), raw["deleted"] == true, raw["updated"] == true, metadataJSON, rawID)
	if err != nil {
		return fmt.Errorf("upsert canonical message: %w", err)
	}
	if err := persistFeishuAttachments(ctx, tx, raw, canonicalID(accountExternal, messageID), rawID, accountID, conversationID); err != nil {
		return fmt.Errorf("persist attachments: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion.worker_tasks (task_type, entity_id, payload)
        VALUES ('vectorization',$1,$2::jsonb) ON CONFLICT (task_type, entity_id) DO NOTHING`, canonicalID(accountExternal, messageID), payload); err != nil {
		return fmt.Errorf("enqueue vectorization: %w", err)
	}
	return tx.Commit(ctx)
}

func upsertConversationMetadata(ctx context.Context, pool *pgxpool.Pool, accountExternal, chatID, name string, raw map[string]any) error {
	accountExternal = externalAccountID(accountExternal)
	payload, _ := json.Marshal(raw)
	_, err := pool.Exec(ctx, `INSERT INTO ingestion.conversations(source_account_id,platform,external_conversation_id,conversation_type,name,raw_payload,last_seen_at)
        SELECT id,'feishu',$2,CASE WHEN $2 LIKE 'oc_%' THEN 'group' ELSE 'unknown' END,$3,$4::jsonb,now()
        FROM ingestion.source_accounts WHERE platform='feishu' AND external_account_id=$1
        ON CONFLICT(source_account_id,external_conversation_id) DO UPDATE SET name=COALESCE(NULLIF(EXCLUDED.name,''),ingestion.conversations.name),raw_payload=EXCLUDED.raw_payload,last_seen_at=now(),updated_at=now()`, accountExternal, chatID, name, payload)
	return err
}

func loadCheckpoint(ctx context.Context, pool *pgxpool.Pool, account string, conversation string) (string, error) {
	var token string
	err := pool.QueryRow(ctx, `SELECT COALESCE(cc.cursor,'') FROM ingestion.collector_checkpoints cc JOIN ingestion.conversations c ON c.id=cc.conversation_id WHERE cc.source_account_id=(SELECT id FROM ingestion.source_accounts WHERE platform='feishu' AND external_account_id=$1) AND c.external_conversation_id=$2`, externalAccountID(account), conversation).Scan(&token)
	if err != nil {
		return "", nil
	}
	return token, nil
}

type listenConfig struct {
	Selected    map[string]bool
	HistoryFrom *time.Time
	UpdatedAt   *time.Time
}

func loadListenConfig(ctx context.Context, pool *pgxpool.Pool, account string) (listenConfig, error) {
	var raw []byte
	var history, updated *time.Time
	err := pool.QueryRow(ctx, `SELECT selected_conversations,history_start_at,config_updated_at FROM ingestion.collector_bindings b JOIN ingestion.source_accounts sa ON sa.id=b.source_account_id WHERE b.collector_type='feishu' AND sa.external_account_id=$1`, externalAccountID(account)).Scan(&raw, &history, &updated)
	if err != nil {
		return listenConfig{Selected: map[string]bool{}}, err
	}
	var values []string
	_ = json.Unmarshal(raw, &values)
	selected := make(map[string]bool, len(values))
	for _, value := range values {
		selected[value] = true
	}
	return listenConfig{Selected: selected, HistoryFrom: history, UpdatedAt: updated}, nil
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

func accountHeartbeat(ctx context.Context, pool *pgxpool.Pool, account, status string, processed, failed int64, errText *string) {
	var accountID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM ingestion.source_accounts WHERE platform='feishu' AND external_account_id=$1`, externalAccountID(account)).Scan(&accountID); err != nil {
		return
	}
	name := "feishu-collector:" + externalAccountID(account)
	_, _ = pool.Exec(ctx, `INSERT INTO ingestion.worker_runs(name,source_account_id,status,last_heartbeat,last_error,processed_count,failed_count,updated_at)
        VALUES($1,$2,$3,now(),$4,$5,$6,now()) ON CONFLICT(name) DO UPDATE SET source_account_id=EXCLUDED.source_account_id,
        status=EXCLUDED.status,last_heartbeat=now(),last_error=EXCLUDED.last_error,
        processed_count=ingestion.worker_runs.processed_count+$5,failed_count=ingestion.worker_runs.failed_count+$6,updated_at=now()`,
		name, accountID, status, errText, processed, failed)
}
