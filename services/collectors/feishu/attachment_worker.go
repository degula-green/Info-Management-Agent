package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/feishu-collector/storage"
)

type FeishuAttachmentWorker struct {
	Pool        *pgxpool.Pool
	Adapter     FeishuAttachmentAdapter
	Store       *storage.Store
	TempDir     string
	WorkerID    string
	Account     string
	MaxAttempts int
}

func (w *FeishuAttachmentWorker) Process(ctx context.Context, taskID, attachmentID int64) error {
	var userID, conversationID, messageID, name, ext string
	var sourceMessageID string
	err := w.Pool.QueryRow(ctx, `SELECT sa.internal_account_id::text,c.id::text,m.id,m.source_message_id,COALESCE(a.file_name,''),COALESCE(a.extension,'')
        FROM ingestion.attachments a JOIN ingestion.messages m ON m.id=a.message_id
        JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id JOIN ingestion.conversations c ON c.id=m.conversation_id
        WHERE a.id=$1 AND a.platform='feishu'`, attachmentID).Scan(&userID, &conversationID, &messageID, &sourceMessageID, &name, &ext)
	if err != nil {
		return fmt.Errorf("load attachment: %w", err)
	}
	if ext == "" || name == "" {
		return fmt.Errorf("attachment has no reliable file name or extension")
	}
	if err := w.Pool.QueryRow(ctx, `UPDATE ingestion.attachments SET download_status='downloading',last_error=NULL,updated_at=now() WHERE id=$1 AND download_status IN ('not_started','failed') RETURNING id`, attachmentID).Scan(&attachmentID); err != nil {
		return err
	}
	body, info, err := w.Adapter.DownloadByID(ctx, attachmentID)
	if err != nil {
		return err
	}
	defer body.Close()
	if w.TempDir == "" {
		w.TempDir = os.TempDir()
	}
	dir := filepath.Join(w.TempDir, "info-agent-feishu", strconv.FormatInt(attachmentID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "payload.bin")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if info.Extension != "" {
		ext = info.Extension
	}
	if info.Name != "" {
		name = info.Name
	}
	// Use the PostgreSQL message id in the object key. The external Feishu
	// message id remains a separate field and is never used as PG identity.
	key := storage.ObjectKey(userID, "feishu", conversationID, messageID, strconv.FormatInt(attachmentID, 10), name)
	result, err := w.Store.PutFile(ctx, tmp, key, ext, info.ContentType)
	if err != nil {
		return err
	}
	_, err = w.Pool.Exec(ctx, `UPDATE ingestion.attachments SET file_size=$1,content_hash=$2,storage_provider='minio',storage_bucket=$3,storage_key=$4,download_status='completed',parse_status='pending',last_error=NULL,updated_at=now() WHERE id=$5`, result.Size, result.ContentHash, result.Bucket, result.Key, attachmentID)
	if err != nil {
		return err
	}
	_, err = w.Pool.Exec(ctx, `UPDATE ingestion.worker_tasks SET status='completed',locked_by=NULL,locked_until=NULL,completed_at=now(),last_error=NULL,updated_at=now() WHERE id=$1`, taskID)
	if err == nil {
		_, err = w.Pool.Exec(ctx, `INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload) VALUES('attachment_parse',$1,$2::jsonb) ON CONFLICT(task_type,entity_id) DO UPDATE SET payload=EXCLUDED.payload,status='pending',next_run_at=now(),locked_by=NULL,locked_until=NULL,last_error=NULL,completed_at=NULL,updated_at=now()`, "attachment:"+strconv.FormatInt(attachmentID, 10), json.RawMessage(fmt.Sprintf(`{"platform":"feishu","attachment_id":%d}`, attachmentID)))
	}
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	return err
}

func (w *FeishuAttachmentWorker) Fail(ctx context.Context, taskID, attachmentID int64, cause error) {
	message := cause.Error()
	if len(message) > 4000 {
		message = message[:4000]
	}
	_, _ = w.Pool.Exec(ctx, `UPDATE ingestion.attachments SET download_status='failed',last_error=$1,updated_at=now() WHERE id=$2`, message, attachmentID)
	_, _ = w.Pool.Exec(ctx, `UPDATE ingestion.worker_tasks SET status=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'pending' END,next_run_at=now()+(LEAST(3600,60*(2^LEAST(attempts-1,6)))*interval '1 second'),locked_by=NULL,locked_until=NULL,last_error=$1,updated_at=now() WHERE id=$2`, message, taskID)
}

func (w *FeishuAttachmentWorker) RunOnce(ctx context.Context, limit int) (int, error) {
	rows, err := w.Pool.Query(ctx, `WITH picked AS (
        SELECT t.id,t.entity_id FROM ingestion.worker_tasks t
        JOIN ingestion.attachments a ON a.id=(t.payload->>'attachment_id')::bigint
        JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id
        WHERE t.task_type='attachment_download' AND t.status='pending' AND t.next_run_at<=now()
          AND t.payload->>'platform'='feishu' AND sa.platform='feishu' AND sa.external_account_id=$1
        ORDER BY t.id LIMIT $2 FOR UPDATE OF t SKIP LOCKED)
        UPDATE ingestion.worker_tasks t SET status='processing',attempts=attempts+1,locked_by=$3,
        locked_until=now()+interval '10 minutes',updated_at=now() FROM picked WHERE t.id=picked.id
        RETURNING t.id,t.entity_id`, externalAccountID(w.Account), limit, w.WorkerID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var taskID int64
		var entity string
		if err := rows.Scan(&taskID, &entity); err != nil {
			continue
		}
		var attachmentID int64
		if _, err := fmt.Sscanf(entity, "attachment:%d", &attachmentID); err != nil {
			w.Fail(ctx, taskID, 0, fmt.Errorf("invalid attachment task entity"))
			continue
		}
		if err := w.Process(ctx, taskID, attachmentID); err != nil {
			w.Fail(ctx, taskID, attachmentID, err)
		}
		count++
	}
	return count, rows.Err()
}
