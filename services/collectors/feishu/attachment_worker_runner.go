package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/feishu-collector/storage"
)

// runFeishuAttachmentWorker is deliberately a separate mode of the binary so
// attachment downloads never run on the message polling goroutine.
func runFeishuAttachmentWorker(pool *pgxpool.Pool, account, redisURL, redisDB, appID, appSecret string) {
	store, err := storage.NewFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "minio:", err)
		return
	}
	dl := NewRedisFeishuAttachmentDownloader(http.DefaultClient, account, redisURL, redisDB, appID, appSecret)
	w := &FeishuAttachmentWorker{Pool: pool, Adapter: FeishuAttachmentAdapter{Pool: pool, Downloader: dl}, Store: store,
		WorkerID: "feishu-attachment-worker:" + externalAccountID(account), Account: account, MaxAttempts: 5}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for {
		if _, err := w.RunOnce(ctx, 10); err != nil {
			fmt.Fprintln(os.Stderr, "attachment worker:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
