package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"info-agent/core/internal/config"
	"info-agent/core/internal/redisstore"
)

const maxAvatarSize = 5 << 20

type profileRecord struct {
	ID                int64
	Username          string
	Nickname          string
	Email             string
	AvatarStorageKey  *string
	AvatarContentType *string
	UpdatedAt         time.Time
}

type oauthStateRecord struct {
	UserID int64  `json:"user_id"`
	Intent string `json:"intent"`
}

func apiError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func loadProfile(ctx context.Context, pool *pgxpool.Pool, userID int64) (profileRecord, error) {
	var p profileRecord
	err := pool.QueryRow(ctx, `SELECT id,COALESCE(username,''),COALESCE(nickname,''),COALESCE(email,''),
        avatar_storage_key,avatar_content_type,updated_at FROM identity.users WHERE id=$1`, userID).
		Scan(&p.ID, &p.Username, &p.Nickname, &p.Email, &p.AvatarStorageKey, &p.AvatarContentType, &p.UpdatedAt)
	return p, err
}

func avatarStore(cfg config.Config) (*minio.Client, time.Duration, error) {
	if cfg.MinioEndpoint == "" || cfg.MinioAccessKey == "" || cfg.MinioSecretKey == "" || cfg.MinioBucket == "" {
		return nil, 0, errors.New("avatar storage is not configured")
	}
	endpoint := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(cfg.MinioEndpoint), "https://"), "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinioAccessKey, cfg.MinioSecretKey, ""),
		Secure: cfg.MinioSecure,
	})
	if err != nil {
		return nil, 0, err
	}
	ttl, err := time.ParseDuration(cfg.AvatarURLTTL)
	if err != nil || ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return client, ttl, nil
}

func profileJSON(ctx context.Context, cfg config.Config, p profileRecord) gin.H {
	var avatarURL *string
	if p.AvatarStorageKey != nil && *p.AvatarStorageKey != "" {
		if store, ttl, err := avatarStore(cfg); err == nil {
			if signed, err := store.PresignedGetObject(ctx, cfg.MinioBucket, *p.AvatarStorageKey, ttl, nil); err == nil {
				value := signed.String()
				avatarURL = &value
			}
		}
	}
	return gin.H{"id": p.ID, "username": p.Username, "nickname": p.Nickname, "email": p.Email,
		"avatar_url": avatarURL, "updated_at": p.UpdatedAt}
}

func getProfile(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	p, err := loadProfile(c, pool, c.GetInt64("user_id"))
	if err != nil {
		apiError(c, http.StatusNotFound, "profile_not_found", "profile not found")
		return
	}
	c.JSON(http.StatusOK, profileJSON(c, cfg, p))
}

func patchProfile(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input struct {
		Nickname *string `json:"nickname"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Nickname == nil {
		apiError(c, http.StatusBadRequest, "invalid_profile", "nickname is required")
		return
	}
	nickname := strings.TrimSpace(*input.Nickname)
	if len([]rune(nickname)) < 1 || len([]rune(nickname)) > 64 {
		apiError(c, http.StatusBadRequest, "invalid_nickname", "nickname must contain 1 to 64 characters")
		return
	}
	// `nickname` is the authentication/profile field. Keep this update scoped
	// to the profile contract; legacy display_name values may have independent
	// constraints and are not part of the editable profile API.
	if _, err := pool.Exec(c, `UPDATE identity.users SET nickname=$1,updated_at=now() WHERE id=$2`, nickname, c.GetInt64("user_id")); err != nil {
		apiError(c, http.StatusInternalServerError, "profile_update_failed", "failed to update profile")
		return
	}
	getProfile(c, pool, cfg)
}

func validAvatar(file io.Reader) ([]byte, string, string, error) {
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarSize+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(data) == 0 || len(data) > maxAvatarSize {
		return nil, "", "", errors.New("avatar must be between 1 byte and 5 MB")
	}
	mime := http.DetectContentType(data)
	extensions := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}
	ext, ok := extensions[mime]
	if !ok {
		return nil, "", "", errors.New("avatar must be JPEG, PNG, or WebP")
	}
	return data, mime, ext, nil
}

func uploadProfileAvatar(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		apiError(c, http.StatusBadRequest, "avatar_required", "avatar file is required")
		return
	}
	defer file.Close()
	data, contentType, ext, err := validAvatar(file)
	if err != nil {
		status := http.StatusUnsupportedMediaType
		if strings.Contains(err.Error(), "5 MB") {
			status = http.StatusRequestEntityTooLarge
		}
		apiError(c, status, "invalid_avatar", err.Error())
		return
	}
	store, _, err := avatarStore(cfg)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "avatar_storage_unavailable", err.Error())
		return
	}
	exists, err := store.BucketExists(c, cfg.MinioBucket)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "avatar_storage_unavailable", "avatar storage is unavailable")
		return
	}
	if !exists {
		if err := store.MakeBucket(c, cfg.MinioBucket, minio.MakeBucketOptions{}); err != nil {
			apiError(c, http.StatusServiceUnavailable, "avatar_storage_unavailable", "avatar storage is unavailable")
			return
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		apiError(c, http.StatusInternalServerError, "avatar_upload_failed", "failed to create avatar key")
		return
	}
	key := fmt.Sprintf("avatars/%d/%s%s", c.GetInt64("user_id"), hex.EncodeToString(random), ext)
	if _, err := store.PutObject(c, cfg.MinioBucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType}); err != nil {
		apiError(c, http.StatusServiceUnavailable, "avatar_upload_failed", "failed to store avatar")
		return
	}
	var oldKey *string
	err = pool.QueryRow(c, `SELECT avatar_storage_key FROM identity.users WHERE id=$1`, c.GetInt64("user_id")).Scan(&oldKey)
	if err == nil {
		_, err = pool.Exec(c, `UPDATE identity.users SET avatar_storage_key=$1,avatar_content_type=$2,avatar_updated_at=now(),updated_at=now()
            WHERE id=$3`, key, contentType, c.GetInt64("user_id"))
	}
	if err != nil {
		_ = store.RemoveObject(c, cfg.MinioBucket, key, minio.RemoveObjectOptions{})
		apiError(c, http.StatusInternalServerError, "avatar_upload_failed", "failed to update profile avatar")
		return
	}
	if oldKey != nil && *oldKey != "" && *oldKey != key {
		if err := store.RemoveObject(context.WithoutCancel(c), cfg.MinioBucket, *oldKey, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("delete previous avatar %q: %v", *oldKey, err)
		}
	}
	getProfile(c, pool, cfg)
}

func deleteProfileAvatar(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var oldKey *string
	err := pool.QueryRow(c, `SELECT avatar_storage_key FROM identity.users WHERE id=$1`, c.GetInt64("user_id")).Scan(&oldKey)
	if err == nil {
		_, err = pool.Exec(c, `UPDATE identity.users SET avatar_storage_key=NULL,avatar_content_type=NULL,avatar_updated_at=NULL,updated_at=now()
            WHERE id=$1`, c.GetInt64("user_id"))
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		apiError(c, http.StatusInternalServerError, "avatar_delete_failed", "failed to clear profile avatar")
		return
	}
	if oldKey != nil && *oldKey != "" {
		if store, _, e := avatarStore(cfg); e == nil {
			if e := store.RemoveObject(context.WithoutCancel(c), cfg.MinioBucket, *oldKey, minio.RemoveObjectOptions{}); e != nil {
				log.Printf("delete avatar %q: %v", *oldKey, e)
			}
		}
	}
	getProfile(c, pool, cfg)
}

type connectorView struct {
	Platform                  string     `json:"platform"`
	DisplayName               string     `json:"display_name"`
	Availability              string     `json:"availability"`
	Bound                     bool       `json:"bound"`
	CleanupPending            bool       `json:"cleanup_pending"`
	Status                    string     `json:"status"`
	AccountName               *string    `json:"account_name"`
	AccountAvatarURL          *string    `json:"account_avatar_url"`
	BoundAt                   *time.Time `json:"bound_at"`
	LastSyncAt                *time.Time `json:"last_sync_at"`
	SelectedConversationCount int        `json:"selected_conversation_count"`
	LastError                 *string    `json:"last_error"`
	Actions                   []string   `json:"actions"`
}

const (
	credentialCleanupFailed = "credential_cleanup_failed"
	wechatStopFailed        = "wechat_stop_failed"
)

func connectorFor(ctx context.Context, pool *pgxpool.Pool, userID int64, platform string) connectorView {
	platform = canonicalPlatform(platform)
	labels := map[string]string{"feishu": "飞书", "wecom": "企业微信", "wechat": "个人微信"}
	v := connectorView{Platform: platform, DisplayName: labels[platform], Availability: "available", Status: "unbound", Actions: []string{"bind"}}
	if platform == "wecom" {
		v.Availability, v.Actions = "coming_soon", []string{}
		return v
	}
	storagePlatforms := platformStorageValues(platform)
	var accountID int64
	var enabled *bool
	var selected []byte
	var heartbeat *time.Time
	var accountStatus string
	err := pool.QueryRow(ctx, `SELECT sa.id,sa.status,sa.account_name,sa.last_collected_at,b.bound_at,b.enabled,b.selected_conversations,
        COALESCE(b.last_error,wr.last_error),wr.last_heartbeat
        FROM ingestion.source_accounts sa
        LEFT JOIN LATERAL (
            SELECT bound_at,enabled,selected_conversations,last_error
            FROM ingestion.collector_bindings b
            WHERE b.source_account_id=sa.id AND b.collector_type=ANY($2::text[])
            ORDER BY CASE WHEN b.collector_type=$3 THEN 0 ELSE 1 END
            LIMIT 1
        ) b ON true
        LEFT JOIN LATERAL (SELECT last_error,last_heartbeat FROM ingestion.worker_runs WHERE source_account_id=sa.id ORDER BY last_heartbeat DESC NULLS LAST LIMIT 1) wr ON true
		WHERE sa.internal_account_id=$1 AND sa.platform=ANY($2::text[])
		          AND (sa.status='active' OR (sa.status='disabled' AND EXISTS (
              SELECT 1 FROM ingestion.collector_bindings pending
              WHERE pending.source_account_id=sa.id
                AND pending.last_error=ANY($4::text[])
          )))
		ORDER BY CASE WHEN sa.platform=$3 THEN 0 ELSE 1 END, sa.updated_at DESC NULLS LAST,sa.id DESC LIMIT 1`, userID, storagePlatforms, platform, []string{credentialCleanupFailed, wechatStopFailed}).
		Scan(&accountID, &accountStatus, &v.AccountName, &v.LastSyncAt, &v.BoundAt, &enabled, &selected, &v.LastError, &heartbeat)
	if err != nil {
		return v
	}
	if accountStatus == "disabled" {
		v.CleanupPending = true
		v.Status = "error"
		v.Actions = []string{"unbind"}
		return v
	}
	v.Bound = true
	v.Actions = []string{"configure", "unbind"}
	var selectedIDs []string
	_ = json.Unmarshal(selected, &selectedIDs)
	v.SelectedConversationCount = len(selectedIDs)
	switch {
	case v.LastError != nil && strings.TrimSpace(*v.LastError) != "":
		v.Status = "error"
	case enabled == nil || !*enabled || len(selectedIDs) == 0:
		v.Status = "paused"
	case heartbeat == nil || time.Since(*heartbeat) > 2*time.Minute:
		v.Status = "offline"
	default:
		v.Status = "active"
	}
	return v
}

func listConnectors(c *gin.Context, pool *pgxpool.Pool) {
	userID := c.GetInt64("user_id")
	items := []connectorView{connectorFor(c, pool, userID, "feishu"), connectorFor(c, pool, userID, "wecom"), connectorFor(c, pool, userID, "wechat")}
	c.JSON(http.StatusOK, gin.H{"connectors": items})
}

func beginFeishuOAuth(c *gin.Context, cfg config.Config, redisClient *redisstore.Client, redirect bool) {
	if cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" || redisClient == nil {
		apiError(c, http.StatusServiceUnavailable, "connector_not_configured", "Feishu OAuth is not configured")
		return
	}
	var input struct {
		Intent string `json:"intent"`
	}
	if c.Request.Method == http.MethodPost {
		if c.ShouldBindJSON(&input) != nil {
			apiError(c, http.StatusBadRequest, "invalid_oauth_intent", "intent must be bind or rebind")
			return
		}
	} else {
		input.Intent = "bind"
	}
	if input.Intent != "bind" && input.Intent != "rebind" {
		apiError(c, http.StatusBadRequest, "invalid_oauth_intent", "intent must be bind or rebind")
		return
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		apiError(c, http.StatusInternalServerError, "oauth_state_failed", "failed to create OAuth state")
		return
	}
	state := hex.EncodeToString(random)
	payload, _ := json.Marshal(oauthStateRecord{UserID: c.GetInt64("user_id"), Intent: input.Intent})
	if err := redisClient.Set(c, "oauth:feishu:state:"+state, payload, 10*time.Minute); err != nil {
		apiError(c, http.StatusServiceUnavailable, "oauth_state_failed", "failed to persist OAuth state")
		return
	}
	q := url.Values{"app_id": {cfg.FeishuAppID}, "redirect_uri": {cfg.FeishuRedirectURI}, "state": {state},
		"scope": {"im:chat:readonly im:message:readonly contact:user.base:readonly"}}
	authorizeURL := "https://open.feishu.cn/open-apis/authen/v1/authorize?" + q.Encode()
	if redirect {
		c.Redirect(http.StatusFound, authorizeURL)
		return
	}
	c.JSON(http.StatusOK, gin.H{"authorize_url": authorizeURL})
}

func connectorRedirect(cfg config.Config, path string, values url.Values) string {
	base := strings.TrimRight(cfg.WebBaseURL, "/") + path
	if len(values) > 0 {
		base += "?" + values.Encode()
	}
	return base
}

func proxyWechatConnector(c *gin.Context, cfg config.Config, path string) {
	var body []byte
	if c.Request.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(c.Request.Body, 16*1024))
	}
	req, err := http.NewRequestWithContext(c, c.Request.Method, strings.TrimRight(cfg.WechatCollectorURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		apiError(c, http.StatusBadGateway, "wechat_collector_failed", "failed to create collector request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Info-Agent-User-ID", fmt.Sprint(c.GetInt64("user_id")))
	req.Header.Set("X-Info-Agent-Collector-Token", cfg.CollectorToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "wechat_collector_unavailable", "WeChat collector is unavailable")
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 400 {
		var detail struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &detail)
		code := "wechat_bind_failed"
		if strings.Contains(strings.ToLower(detail.Detail), "another user") {
			code = "connector_owned_by_another_user"
		}
		apiError(c, resp.StatusCode, code, wechatConnectorErrorMessage(detail.Detail))
		return
	}
	c.Data(resp.StatusCode, "application/json", data)
}

func wechatConnectorErrorMessage(detail string) string {
	detail = strings.TrimSpace(detail)
	switch {
	case strings.Contains(detail, "db_dir must be an existing local absolute path"):
		return "本机微信数据目录不存在或不是绝对路径"
	case strings.Contains(detail, "database file must be located below"):
		return "所选数据库文件不在微信账号的 db_storage 目录下"
	case strings.Contains(detail, "db_dir must point to a directory or a database file"):
		return "本机微信数据目录必须指向目录或数据库文件"
	case strings.Contains(detail, "db_dir must contain exactly one WeChat account directory"):
		return "目录中存在多个微信账号，请选择对应账号目录或填写匹配的微信 ID"
	case strings.Contains(detail, "db_dir does not contain a readable WeChat db_storage directory"):
		return "目录中未找到可读取的微信 db_storage 数据"
	case detail == "":
		return "个人微信连接器请求失败"
	default:
		return detail
	}
}

func stopWechatCollector(ctx context.Context, cfg config.Config, userID int64) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(cfg.WechatCollectorURL, "/")+"/stop", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Info-Agent-User-ID", fmt.Sprint(userID))
	req.Header.Set("X-Info-Agent-Collector-Token", cfg.CollectorToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("wechat collector stop returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func unbindConnector(c *gin.Context, pool *pgxpool.Pool, cfg config.Config, redisClient *redisstore.Client) {
	platform := canonicalPlatform(c.Param("platform"))
	if platform == "wecom" {
		apiError(c, http.StatusConflict, "connector_not_available", "WeCom is not available")
		return
	}
	if platform != "feishu" && platform != "wechat" {
		apiError(c, http.StatusNotFound, "connector_not_found", "connector not found")
		return
	}
	storagePlatforms := platformStorageValues(platform)
	cleanupCode := credentialCleanupFailed
	if platform == "wechat" {
		cleanupCode = wechatStopFailed
	}
	tx, err := pool.Begin(c)
	if err != nil {
		apiError(c, http.StatusInternalServerError, "connector_unbind_failed", "failed to start unbind")
		return
	}
	defer tx.Rollback(c)
	var accountID int64
	var externalID string
	err = tx.QueryRow(c, `SELECT sa.id,sa.external_account_id FROM ingestion.source_accounts sa
        WHERE sa.internal_account_id=$1 AND sa.platform=ANY($2::text[])
          AND (sa.status='active' OR (sa.status='disabled' AND EXISTS (
              SELECT 1 FROM ingestion.collector_bindings pending
              WHERE pending.source_account_id=sa.id
                AND pending.collector_type=ANY($2::text[])
                AND pending.last_error=ANY($4::text[])
          )))
        ORDER BY CASE WHEN sa.platform=$3 THEN 0 ELSE 1 END, sa.updated_at DESC NULLS LAST,sa.id DESC LIMIT 1 FOR UPDATE OF sa`,
		c.GetInt64("user_id"), storagePlatforms, platform, []string{credentialCleanupFailed, wechatStopFailed}).Scan(&accountID, &externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(c)
		c.JSON(http.StatusOK, connectorFor(c, pool, c.GetInt64("user_id"), platform))
		return
	}
	if err != nil {
		apiError(c, http.StatusInternalServerError, "connector_unbind_failed", "failed to load connector")
		return
	}
	result, err := tx.Exec(c, `UPDATE ingestion.collector_bindings SET enabled=false,last_error=$1,updated_at=now() WHERE source_account_id=$2 AND collector_type=ANY($3::text[])`, cleanupCode, accountID, storagePlatforms)
	if err != nil || result.RowsAffected() == 0 {
		apiError(c, http.StatusInternalServerError, "connector_unbind_failed", "failed to stop connector")
		return
	}
	if _, err = tx.Exec(c, `UPDATE ingestion.source_accounts SET status='disabled',updated_at=now() WHERE id=$1`, accountID); err != nil {
		apiError(c, http.StatusInternalServerError, "connector_unbind_failed", "failed to deactivate connector")
		return
	}
	if platform == "feishu" {
		if _, err = tx.Exec(c, `UPDATE identity.users SET feishu_open_id=NULL,feishu_name=NULL,feishu_avatar=NULL,updated_at=now() WHERE id=$1`, c.GetInt64("user_id")); err != nil {
			apiError(c, http.StatusInternalServerError, "connector_unbind_failed", "failed to clear connector profile")
			return
		}
	}
	if err = tx.Commit(c); err != nil {
		apiError(c, http.StatusInternalServerError, "connector_unbind_failed", "failed to commit unbind")
		return
	}
	var cleanupErr error
	if platform == "feishu" {
		if redisClient == nil {
			cleanupErr = errors.New("redis client is not configured")
		} else {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(c), 10*time.Second)
			cleanupErr = redisClient.Delete(cleanupCtx, "credential:feishu:"+externalID)
			cancel()
		}
	} else if platform == "wechat" {
		cleanupErr = stopWechatCollector(c, cfg, c.GetInt64("user_id"))
	}
	if cleanupErr != nil {
		if platform == "feishu" {
			log.Printf("connector cleanup failed: platform=%s user_id=%d source_account_id=%d redis_key=%s code=%s: %v", platform, c.GetInt64("user_id"), accountID, "credential:feishu:"+externalID, cleanupCode, cleanupErr)
		} else {
			log.Printf("connector cleanup failed: platform=%s user_id=%d source_account_id=%d code=%s: %v", platform, c.GetInt64("user_id"), accountID, cleanupCode, cleanupErr)
		}
		apiError(c, http.StatusServiceUnavailable, cleanupCode, map[string]string{credentialCleanupFailed: "飞书认证凭据清理失败，请重试", wechatStopFailed: "个人微信采集器停止失败，请重试"}[cleanupCode])
		return
	}
	result, err = pool.Exec(context.WithoutCancel(c), `UPDATE ingestion.collector_bindings SET last_error=NULL,updated_at=now() WHERE source_account_id=$1 AND collector_type=ANY($2::text[])`, accountID, storagePlatforms)
	if err != nil || result.RowsAffected() == 0 {
		log.Printf("connector cleanup state update failed: platform=%s user_id=%d source_account_id=%d: %v", platform, c.GetInt64("user_id"), accountID, err)
		apiError(c, http.StatusServiceUnavailable, cleanupCode, "解绑清理状态保存失败，请重试")
		return
	}
	c.JSON(http.StatusOK, connectorFor(c, pool, c.GetInt64("user_id"), platform))
}
