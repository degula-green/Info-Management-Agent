package httpapi

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"info-agent/core/internal/config"
)

func attachmentObjectStore(cfg config.Config) (*minio.Client, error) {
	if cfg.MinioEndpoint == "" || cfg.MinioAccessKey == "" || cfg.MinioSecretKey == "" {
		return nil, errors.New("attachment storage is not configured")
	}
	endpoint := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(cfg.MinioEndpoint), "https://"), "http://")
	return minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinioAccessKey, cfg.MinioSecretKey, ""),
		Secure: cfg.MinioSecure,
	})
}

func attachmentDisposition(fileName string, download bool) string {
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	if value := mime.FormatMediaType(disposition, map[string]string{"filename": fileName}); value != "" {
		return value
	}
	return disposition
}

func getAttachmentContent(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(c, http.StatusBadRequest, "invalid_attachment_id", "invalid attachment id")
		return
	}

	var fileName, contentType, bucket, objectKey, downloadStatus string
	err = pool.QueryRow(c, `SELECT COALESCE(a.file_name,''),COALESCE(a.mime_type,''),
		COALESCE(a.storage_bucket,''),COALESCE(a.storage_key,''),a.download_status
		FROM ingestion.attachments a
		JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id
		WHERE a.id=$1 AND a.is_deleted=false AND sa.internal_account_id=$2`, id, c.GetInt64("user_id")).
		Scan(&fileName, &contentType, &bucket, &objectKey, &downloadStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(c, http.StatusNotFound, "attachment_not_found", "attachment not found")
		return
	}
	if err != nil {
		apiError(c, http.StatusInternalServerError, "attachment_lookup_failed", "failed to load attachment")
		return
	}
	if downloadStatus != "completed" || bucket == "" || objectKey == "" {
		apiError(c, http.StatusConflict, "attachment_content_not_ready", "attachment source file is not ready")
		return
	}

	store, err := attachmentObjectStore(cfg)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "attachment_storage_unavailable", err.Error())
		return
	}
	stat, err := store.StatObject(c, bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "attachment_storage_unavailable", "attachment source file is unavailable")
		return
	}
	object, err := store.GetObject(c, bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "attachment_storage_unavailable", "attachment source file is unavailable")
		return
	}
	defer object.Close()

	if fileName == "" {
		fileName = "attachment-" + strconv.FormatInt(id, 10)
	}
	if contentType == "" {
		contentType = stat.ContentType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Disposition", attachmentDisposition(fileName, c.Query("download") == "1"))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, stat.Size, contentType, object, nil)
}
