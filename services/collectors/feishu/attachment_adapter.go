package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// FeishuAttachmentAdapter resolves a persisted attachment without consulting
// raw_payload again. external_attachment_id is the durable file_key.
type FeishuAttachmentAdapter struct {
	Pool       attachmentQueryRower
	Downloader AttachmentDownloader
}

type attachmentQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (a FeishuAttachmentAdapter) DownloadByID(ctx context.Context, attachmentID int64) (io.ReadCloser, FileInfo, error) {
	if a.Pool == nil || a.Downloader == nil {
		return nil, FileInfo{}, fmt.Errorf("attachment adapter is not configured")
	}
	var ref AttachmentRef
	var account string
	var size *int64
	err := a.Pool.QueryRow(ctx, `SELECT m.source_message_id, a.external_attachment_id, COALESCE(a.file_name,''), a.file_size, COALESCE(a.mime_type,''), sa.external_account_id
		FROM ingestion.attachments a JOIN ingestion.messages m ON m.id=a.message_id JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id
		WHERE a.id=$1 AND a.platform='feishu'`, attachmentID).Scan(&ref.MessageID, &ref.ResourceKey, &ref.FileName, &size, &ref.FileType, &account)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("load feishu attachment %d: %w", attachmentID, err)
	}
	if ref.MessageID == "" || ref.ResourceKey == "" || strings.HasPrefix(ref.ResourceKey, "synthetic:") {
		return nil, FileInfo{}, fmt.Errorf("attachment %d has no external message id or file_key", attachmentID)
	}
	ref.Platform, ref.AccountID, ref.ResourceType = "feishu", account, "file"
	if size != nil {
		ref.FileSize = *size
	}
	return a.Downloader.Download(ctx, ref)
}

func NewRedisFeishuAttachmentDownloader(client *http.Client, account, redisURL, redisDB, appID, appSecret string) *FeishuAttachmentDownloader {
	key := "credential:feishu:" + account
	load := func(ctx context.Context) (credential, error) {
		data, err := redisGet(ctx, redisURL, redisDB, key)
		if err != nil {
			return credential{}, err
		}
		var c credential
		if err := json.Unmarshal(data, &c); err != nil {
			return credential{}, err
		}
		return c, nil
	}
	return NewFeishuAttachmentDownloader(client,
		func(ctx context.Context) (string, error) { c, err := load(ctx); return c.AccessToken, err },
		func(ctx context.Context) (string, error) {
			c, err := load(ctx)
			if err != nil {
				return "", err
			}
			fresh, err := refreshAccessToken(ctx, appID, appSecret, c.RefreshToken, redisURL, redisDB, key)
			return fresh.AccessToken, err
		},
	)
}
