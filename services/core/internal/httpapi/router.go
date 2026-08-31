package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"info-agent/core/internal/config"
	"info-agent/core/internal/redisstore"
	"info-agent/core/internal/tasks"
)

func conversationAvatar(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range []string{"avatar_url", "avatar", "icon", "image_key"} {
		if v, ok := value[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func conversationName(raw []byte, fallback string) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil {
		for _, key := range []string{"name", "chat_name", "display_name", "nickname"} {
			if v, ok := value[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return fallback
}

func NewRouter(pool *pgxpool.Pool, cfg config.Config, redisClient *redisstore.Client) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.POST("/api/auth/register", func(c *gin.Context) { register(c, pool) })
	r.POST("/api/auth/login", func(c *gin.Context) { login(c, pool, cfg) })
	r.GET("/api/auth/me", authRequired(cfg), func(c *gin.Context) { me(c, pool) })
	r.GET("/health", func(c *gin.Context) { health(c, pool) })
	r.GET("/api/info", info)
	protected := authRequired(cfg)
	r.POST("/api/qa/ask", protected, func(c *gin.Context) { askQuestion(c, pool, cfg) })
	r.GET("/api/profile", protected, func(c *gin.Context) { getProfile(c, pool, cfg) })
	r.PATCH("/api/profile", protected, func(c *gin.Context) { patchProfile(c, pool, cfg) })
	r.POST("/api/profile/avatar", protected, func(c *gin.Context) { uploadProfileAvatar(c, pool, cfg) })
	r.DELETE("/api/profile/avatar", protected, func(c *gin.Context) { deleteProfileAvatar(c, pool, cfg) })
	r.GET("/api/connectors", protected, func(c *gin.Context) { listConnectors(c, pool) })
	r.POST("/api/connectors/feishu/authorize", protected, func(c *gin.Context) { beginFeishuOAuth(c, cfg, redisClient, false) })
	r.GET("/api/connectors/feishu/callback", func(c *gin.Context) { feishuCallback(c, pool, redisClient, cfg) })
	r.POST("/api/connectors/wechat/bind", protected, func(c *gin.Context) { proxyWechatConnector(c, cfg, "/bind") })
	r.POST("/api/connectors/wechat/rebind", protected, func(c *gin.Context) { proxyWechatConnector(c, cfg, "/rebind") })
	r.DELETE("/api/connectors/:platform", protected, func(c *gin.Context) { unbindConnector(c, pool, cfg, redisClient) })
	r.GET("/api/tasks", protected, func(c *gin.Context) { listTasks(c, pool) })
	r.GET("/api/tasks/:id", protected, func(c *gin.Context) { getTask(c, pool) })
	r.POST("/api/tasks/:id/retry", protected, func(c *gin.Context) { retryTask(c, pool) })
	r.GET("/api/workers", protected, func(c *gin.Context) { listWorkers(c, pool) })
	r.GET("/api/messages", protected, func(c *gin.Context) { listMessages(c, pool) })
	r.GET("/api/messages/:id/attachments", protected, func(c *gin.Context) { listMessageAttachments(c, pool) })
	r.GET("/api/attachments/:id", protected, func(c *gin.Context) { getAttachment(c, pool) })
	r.GET("/api/knowledge-bases", protected, func(c *gin.Context) { listKnowledgeBases(c, pool) })
	r.GET("/api/knowledge-bases/:platform/conversations", protected, func(c *gin.Context) { listKnowledgeConversations(c, pool) })
	r.GET("/api/knowledge-bases/:platform/conversations/:id", protected, func(c *gin.Context) { getKnowledgeConversation(c, pool) })
	r.GET("/api/knowledge-bases/:platform/conversations/:id/messages", protected, func(c *gin.Context) { listKnowledgeConversationMessages(c, pool) })
	r.POST("/api/ingestion/wechat/bind", protected, func(c *gin.Context) { proxyWechat(c, cfg, "/bind") })
	r.POST("/api/ingestion/wechat/rebind", protected, func(c *gin.Context) { proxyWechat(c, cfg, "/rebind") })
	r.GET("/api/ingestion/wechat/status", protected, func(c *gin.Context) { proxyWechat(c, cfg, "/status") })
	r.GET("/api/ingestion/wechat/conversations", protected, func(c *gin.Context) {
		path := "/conversations"
		if c.Request.URL.RawQuery != "" {
			path += "?" + c.Request.URL.RawQuery
		}
		proxyWechat(c, cfg, path)
	})
	r.GET("/api/ingestion/wechat/config", protected, func(c *gin.Context) { connectorConfig(c, pool, "wechat", false) })
	r.PUT("/api/ingestion/wechat/config", protected, func(c *gin.Context) { updateConnectorConfig(c, pool, cfg, "wechat") })
	r.GET("/api/ingestion/feishu/conversations", protected, func(c *gin.Context) { listConnectorConversations(c, pool, "feishu") })
	r.GET("/api/ingestion/feishu/config", protected, func(c *gin.Context) { connectorConfig(c, pool, "feishu", false) })
	r.PUT("/api/ingestion/feishu/config", protected, func(c *gin.Context) { updateConnectorConfig(c, pool, cfg, "feishu") })
	r.POST("/api/ingestion/wechat/stop", protected, func(c *gin.Context) { proxyWechat(c, cfg, "/stop") })
	r.GET("/api/ingestion/feishu/authorize", authRequired(cfg), func(c *gin.Context) { beginFeishuOAuth(c, cfg, redisClient, true) })
	r.GET("/api/ingestion/feishu/callback", func(c *gin.Context) { feishuCallback(c, pool, redisClient, cfg) })
	return r
}

func attachmentFields() string {
	return `a.id,a.file_name,a.extension,a.mime_type,a.file_category,a.file_size,a.parse_status,a.preview_capability,a.is_deleted`
}

func attachmentJSON(id int64, name, ext, mime, category string, size *int64, parse, preview string, deleted bool) gin.H {
	return gin.H{"id": id, "file_name": name, "extension": ext, "mime_type": mime, "file_category": category, "file_size": size, "parse_status": parse, "preview_capability": preview, "is_deleted": deleted}
}

func listMessageAttachments(c *gin.Context, pool *pgxpool.Pool) {
	messageID := c.Param("id")
	rows, err := pool.Query(c, `SELECT `+attachmentFields()+` FROM ingestion.attachments a JOIN ingestion.messages m ON m.id=a.message_id JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id WHERE a.message_id=$1 AND sa.internal_account_id=$2 ORDER BY a.id`, messageID, c.GetInt64("user_id"))
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to list attachments"})
		return
	}
	defer rows.Close()
	out := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var name, ext, mime, cat, parse, preview string
		var size *int64
		var deleted bool
		if rows.Scan(&id, &name, &ext, &mime, &cat, &size, &parse, &preview, &deleted) == nil {
			out = append(out, attachmentJSON(id, name, ext, mime, cat, size, parse, preview, deleted))
		}
	}
	c.JSON(200, gin.H{"attachments": out})
}

func getAttachment(c *gin.Context, pool *pgxpool.Pool) {
	var id int64
	if _, err := fmt.Sscan(c.Param("id"), &id); err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid attachment id"})
		return
	}
	var name, ext, mime, cat, parse, preview string
	var size *int64
	var deleted bool
	err := pool.QueryRow(c, `SELECT `+attachmentFields()+` FROM ingestion.attachments a JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id WHERE a.id=$1 AND sa.internal_account_id=$2`, id, c.GetInt64("user_id")).Scan(&id, &name, &ext, &mime, &cat, &size, &parse, &preview, &deleted)
	if err != nil {
		c.JSON(404, gin.H{"error": "attachment not found"})
		return
	}
	c.JSON(200, attachmentJSON(id, name, ext, mime, cat, size, parse, preview, deleted))
}

// listKnowledgeBases exposes the fixed platform-level knowledge bases. A
// platform is enabled only when the current user has an active source account.
func listKnowledgeBases(c *gin.Context, pool *pgxpool.Pool) {
	platforms := []struct {
		key  string
		name string
	}{
		{key: "feishu", name: "飞书"},
		{key: "wecom", name: "企业微信"},
		{key: "wechat", name: "个人微信"},
	}
	type knowledgeBase struct {
		Platform                  string     `json:"platform"`
		DisplayName               string     `json:"display_name"`
		Bound                     bool       `json:"bound"`
		Enabled                   bool       `json:"enabled"`
		SelectedConversationCount int        `json:"selected_conversation_count"`
		LastSyncAt                *time.Time `json:"last_sync_at"`
	}
	result := make([]knowledgeBase, 0, len(platforms))
	userID := c.GetInt64("user_id")
	for _, platform := range platforms {
		var item knowledgeBase
		item.Platform, item.DisplayName = platform.key, platform.name
		var accountID *int64
		var enabled *bool
		var selected *int
		err := pool.QueryRow(c, `SELECT sa.id, (b.enabled AND jsonb_array_length(COALESCE(b.selected_conversations,'[]'::jsonb)) > 0),
                jsonb_array_length(COALESCE(b.selected_conversations,'[]'::jsonb)), sa.last_collected_at
            FROM ingestion.source_accounts sa
            LEFT JOIN ingestion.collector_bindings b ON b.source_account_id=sa.id AND b.collector_type=$2
            WHERE sa.internal_account_id=$1 AND sa.platform=$2 AND sa.status='active'
            ORDER BY sa.updated_at DESC NULLS LAST, sa.id DESC LIMIT 1`, userID, platform.key).
			Scan(&accountID, &enabled, &selected, &item.LastSyncAt)
		if err == nil && accountID != nil {
			item.Bound = true
			item.Enabled = enabled != nil && *enabled
			if selected != nil {
				item.SelectedConversationCount = *selected
			}
		}
		result = append(result, item)
	}
	c.JSON(http.StatusOK, gin.H{"knowledge_bases": result})
}

func knowledgePlatform(value string) bool {
	return value == "feishu" || value == "wecom" || value == "wechat"
}

func knowledgeConversationID(c *gin.Context) (int64, bool) {
	var id int64
	if _, err := fmt.Sscan(c.Param("id"), &id); err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation id"})
		return 0, false
	}
	return id, true
}

func listKnowledgeConversations(c *gin.Context, pool *pgxpool.Pool) {
	platform := c.Param("platform")
	if !knowledgePlatform(platform) {
		c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
		return
	}
	accountID, ok := connectorAccountID(c, pool, platform)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "knowledge base is not bound"})
		return
	}
	page, size := 1, 50
	if _, err := fmt.Sscan(c.DefaultQuery("page", "1"), &page); err != nil || page < 1 {
		page = 1
	}
	if _, err := fmt.Sscan(c.DefaultQuery("page_size", "50"), &size); err != nil || size < 1 {
		size = 50
	}
	if size > 100 {
		size = 100
	}
	search, typ := strings.ToLower(strings.TrimSpace(c.Query("search"))), strings.TrimSpace(c.Query("type"))
	pattern := "%" + search + "%"
	rows, err := pool.Query(c, `SELECT c.id,c.external_conversation_id,COALESCE(c.name,''),c.conversation_type,
            c.last_seen_at,c.is_active,
            EXISTS(SELECT 1 FROM ingestion.collector_bindings b WHERE b.source_account_id=$1 AND b.collector_type=$2
                   AND b.selected_conversations ? c.external_conversation_id)
        FROM ingestion.conversations c
        WHERE c.source_account_id=$1 AND c.platform=$2
          AND ($3='' OR lower(c.external_conversation_id||' '||COALESCE(c.name,'')) LIKE $4)
          AND ($5='' OR c.conversation_type=$5)
        ORDER BY c.last_seen_at DESC NULLS LAST,c.id DESC LIMIT $6 OFFSET $7`,
		accountID, platform, search, pattern, typ, size, (page-1)*size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list conversations"})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var externalID, name, kind string
		var seen *time.Time
		var active, selected bool
		if rows.Scan(&id, &externalID, &name, &kind, &seen, &active, &selected) == nil {
			items = append(items, gin.H{"id": id, "external_id": externalID, "name": name, "conversation_type": kind, "last_seen_at": seen, "is_active": active, "selected": selected})
		}
	}
	var total int
	_ = pool.QueryRow(c, `SELECT count(*) FROM ingestion.conversations c WHERE c.source_account_id=$1 AND c.platform=$2
        AND ($3='' OR lower(c.external_conversation_id||' '||COALESCE(c.name,'')) LIKE $4)
        AND ($5='' OR c.conversation_type=$5)`, accountID, platform, search, pattern, typ).Scan(&total)
	c.JSON(http.StatusOK, gin.H{"conversations": items, "page": page, "page_size": size, "total": total})
}

func getKnowledgeConversation(c *gin.Context, pool *pgxpool.Pool) {
	platform := c.Param("platform")
	if !knowledgePlatform(platform) {
		c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
		return
	}
	id, ok := knowledgeConversationID(c)
	if !ok {
		return
	}
	accountID, ok := connectorAccountID(c, pool, platform)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "knowledge base is not bound"})
		return
	}
	var externalID, name, kind string
	var seen *time.Time
	var active, selected bool
	err := pool.QueryRow(c, `SELECT c.external_conversation_id,COALESCE(c.name,''),c.conversation_type,c.last_seen_at,c.is_active,
        EXISTS(SELECT 1 FROM ingestion.collector_bindings b WHERE b.source_account_id=c.source_account_id AND b.collector_type=$2
               AND b.selected_conversations ? c.external_conversation_id)
        FROM ingestion.conversations c WHERE c.id=$1 AND c.source_account_id=$3 AND c.platform=$2`, id, platform, accountID).
		Scan(&externalID, &name, &kind, &seen, &active, &selected)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "external_id": externalID, "name": name, "conversation_type": kind, "last_seen_at": seen, "is_active": active, "selected": selected})
}

func listKnowledgeConversationMessages(c *gin.Context, pool *pgxpool.Pool) {
	platform := c.Param("platform")
	if !knowledgePlatform(platform) {
		c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
		return
	}
	id, ok := knowledgeConversationID(c)
	if !ok {
		return
	}
	accountID, ok := connectorAccountID(c, pool, platform)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "knowledge base is not bound"})
		return
	}
	limit, offset := 50, 0
	if _, err := fmt.Sscan(c.DefaultQuery("limit", "50"), &limit); err != nil || limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if _, err := fmt.Sscan(c.DefaultQuery("offset", "0"), &offset); err != nil || offset < 0 {
		offset = 0
	}
	rows, err := pool.Query(c, `SELECT m.id,m.source_message_id,m.sender_id,COALESCE(p.display_name,''),COALESCE(p.avatar_url,''),m.occurred_at,
        m.message_type,m.source_message_type,m.text,m.is_deleted,m.is_updated,m.metadata,
        COALESCE(d.status,'pending')
        FROM ingestion.messages m
        LEFT JOIN ingestion.participants p ON p.id=m.sender_id
        LEFT JOIN LATERAL (SELECT status FROM vector_store.documents d WHERE d.raw_message_id=m.raw_message_id ORDER BY d.updated_at DESC LIMIT 1) d ON true
        WHERE m.source_account_id=$1 AND m.conversation_id=$2 AND m.source=$3
        ORDER BY COALESCE(m.occurred_at,m.created_at),m.id LIMIT $4 OFFSET $5`, accountID, id, platform, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list conversation messages"})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var messageID, sourceID string
		var senderID *int64
		var senderName, senderAvatar, kind, sourceKind, text, status string
		var occurred *time.Time
		var deleted, updated bool
		var metadata []byte
		if rows.Scan(&messageID, &sourceID, &senderID, &senderName, &senderAvatar, &occurred, &kind, &sourceKind, &text, &deleted, &updated, &metadata, &status) == nil {
			var meta any = map[string]any{}
			if len(metadata) > 0 {
				_ = json.Unmarshal(metadata, &meta)
			}
			items = append(items, gin.H{"id": messageID, "source_message_id": sourceID, "sender_id": senderID, "sender_name": senderName, "sender_avatar_url": senderAvatar, "occurred_at": occurred, "message_type": kind, "source_message_type": sourceKind, "text": text, "is_deleted": deleted, "is_updated": updated, "metadata": meta, "vector_status": status})
		}
	}
	c.JSON(http.StatusOK, gin.H{"messages": items, "limit": limit, "offset": offset})
}

func authRequired(cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" && c.Query("token") != "" {
			raw = "Bearer " + c.Query("token")
		}
		if !strings.HasPrefix(raw, "Bearer ") {
			c.AbortWithStatusJSON(401, gin.H{"error": "authentication required"})
			return
		}
		parts := strings.Split(strings.TrimPrefix(raw, "Bearer "), ".")
		if len(parts) != 3 {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid token"})
			return
		}
		mac := hmac.New(sha256.New, []byte(cfg.JWTSecret))
		mac.Write([]byte(parts[0] + "." + parts[1]))
		expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(parts[2])) {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid token"})
			return
		}
		var claims struct {
			Sub int64 `json:"sub"`
			Exp int64 `json:"exp"`
		}
		decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || json.Unmarshal(decoded, &claims) != nil || claims.Sub == 0 || (claims.Exp > 0 && time.Now().Unix() > claims.Exp) {
			c.AbortWithStatus(401)
			return
		}
		c.Set("user_id", claims.Sub)
		c.Next()
	}
}

func register(c *gin.Context, pool *pgxpool.Pool) {
	var in struct {
		Nickname string `json:"nickname"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Confirm  string `json:"confirm_password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid registration fields"})
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if strings.TrimSpace(in.Username) == "" || in.Email == "" || !validEmail(in.Email) || len(in.Password) < 6 || in.Password != in.Confirm {
		c.JSON(400, gin.H{"error": "invalid registration fields"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(500, gin.H{"error": "password hashing failed"})
		return
	}
	var id int64
	username := strings.TrimSpace(in.Username)
	nickname := strings.TrimSpace(in.Nickname)
	err = pool.QueryRow(c, `INSERT INTO identity.users(user_key,display_name,username,nickname,email,password_hash) VALUES($1::varchar,$2::varchar,$3::text,$4::text,$5::text,$6::text) RETURNING id`, username, nickname, username, nickname, in.Email, string(hash)).Scan(&id)
	if err != nil {
		log.Printf("registration insert failed: %v", err)
		c.JSON(409, gin.H{"error": "username or email already exists"})
		return
	}
	c.JSON(201, gin.H{"id": id, "username": in.Username, "email": in.Email, "nickname": in.Nickname})
}

func validEmail(value string) bool {
	addr, err := mail.ParseAddress(value)
	return err == nil && addr.Address == value && strings.Contains(value, "@")
}

func login(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid credentials"})
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	var id int64
	var nick, hash, username string
	err := pool.QueryRow(c, `SELECT id,COALESCE(nickname,''),COALESCE(username,''),password_hash FROM identity.users WHERE lower(email)=lower($1)`, in.Email).Scan(&id, &nick, &username, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		c.JSON(401, gin.H{"error": "invalid credentials"})
		return
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"sub": id, "username": username, "email": in.Email, "exp": time.Now().Add(24 * time.Hour).Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(cfg.JWTSecret))
	mac.Write([]byte(header + "." + body))
	signed := header + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	c.JSON(200, gin.H{"token": signed, "user": gin.H{"id": id, "username": username, "email": in.Email, "nickname": nick}})
}
func me(c *gin.Context, pool *pgxpool.Pool) {
	id := c.GetInt64("user_id")
	var username, email, nick string
	var fo, fn, fa *string
	var wechatBound bool
	var wxid *string
	if err := pool.QueryRow(c, `SELECT u.username,COALESCE(u.email,''),COALESCE(u.nickname,''),
		(SELECT sa.external_account_id FROM ingestion.source_accounts sa WHERE sa.internal_account_id=u.id AND sa.platform='feishu' AND sa.status='active' ORDER BY sa.updated_at DESC NULLS LAST,sa.id DESC LIMIT 1),
		(SELECT sa.account_name FROM ingestion.source_accounts sa WHERE sa.internal_account_id=u.id AND sa.platform='feishu' AND sa.status='active' ORDER BY sa.updated_at DESC NULLS LAST,sa.id DESC LIMIT 1),
		CASE WHEN EXISTS (SELECT 1 FROM ingestion.source_accounts sa WHERE sa.internal_account_id=u.id AND sa.platform='feishu' AND sa.status='active') THEN u.feishu_avatar END,
        EXISTS (SELECT 1 FROM ingestion.source_accounts sa WHERE sa.internal_account_id=u.id AND sa.platform='wechat' AND sa.status='active'),
        (SELECT sa.external_account_id FROM ingestion.source_accounts sa WHERE sa.internal_account_id=u.id AND sa.platform='wechat' AND sa.status='active' ORDER BY sa.updated_at DESC NULLS LAST,sa.id DESC LIMIT 1)
		FROM identity.users u WHERE u.id=$1`, id).Scan(&username, &email, &nick, &fo, &fn, &fa, &wechatBound, &wxid); err != nil {
		c.JSON(404, gin.H{"error": "user not found"})
		return
	}
	c.JSON(200, gin.H{"id": id, "username": username, "email": email, "nickname": nick,
		"feishu": gin.H{"bound": fo != nil, "open_id": fo, "name": fn, "avatar": fa},
		"wechat": gin.H{"bound": wechatBound, "wxid": wxid}})
}

func proxyWechat(c *gin.Context, cfg config.Config, path string) {
	var body []byte
	if c.Request.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(c.Request.Body, 16*1024))
	}
	method := http.MethodGet
	if c.Request.Method == http.MethodPost {
		method = http.MethodPost
	} else if c.Request.Method == http.MethodPut {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(c, method, strings.TrimRight(cfg.WechatCollectorURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		c.JSON(502, gin.H{"error": "wechat collector request failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if userID := c.GetInt64("user_id"); userID > 0 {
		req.Header.Set("X-Info-Agent-User-ID", strconv.FormatInt(userID, 10))
	}
	req.Header.Set("X-Info-Agent-Collector-Token", cfg.CollectorToken)
	// Do not let an unavailable RAG service leave the browser's SSE request
	// hanging forever. ResponseHeaderTimeout bounds connection/startup time;
	// once streaming has started, cancellation still follows the client context.
	ragClient := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 15 * time.Second}}
	resp, err := ragClient.Do(req)
	if err != nil {
		c.JSON(503, gin.H{"error": "wechat collector unavailable"})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	c.Data(resp.StatusCode, "application/json", data)
}

func askQuestion(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input struct {
		Question        string   `json:"question"`
		Platforms       []string `json:"platforms"`
		ConversationIDs []int64  `json:"conversation_ids"`
		TopK            int      `json:"top_k"`
	}
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Question) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is required"})
		return
	}
	if input.Platforms == nil {
		input.Platforms = []string{}
	}
	if input.ConversationIDs == nil {
		input.ConversationIDs = []int64{}
	}
	if input.TopK < 1 || input.TopK > 10 {
		input.TopK = 8
	}
	userID := c.GetInt64("user_id")
	rows, err := pool.Query(c, `SELECT sa.id, sa.platform, COALESCE(b.selected_conversations,'[]'::jsonb)
        FROM ingestion.source_accounts sa LEFT JOIN ingestion.collector_bindings b
          ON b.source_account_id=sa.id AND b.collector_type=sa.platform AND b.enabled=true
        WHERE sa.internal_account_id=$1 AND sa.status='active'`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve search scope"})
		return
	}
	defer rows.Close()
	accounts := []int64{}
	conversations := []int64{}
	for rows.Next() {
		var account int64
		var platform string
		var selected []byte
		if rows.Scan(&account, &platform, &selected) != nil {
			continue
		}
		if len(input.Platforms) > 0 {
			allowed := false
			for _, wanted := range input.Platforms {
				if wanted == platform {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		var externalIDs []string
		if json.Unmarshal(selected, &externalIDs) != nil || len(externalIDs) == 0 {
			continue
		}
		conversationRows, queryErr := pool.Query(c, `SELECT id FROM ingestion.conversations
			WHERE source_account_id=$1 AND external_conversation_id = ANY($2::text[])`, account, externalIDs)
		if queryErr != nil {
			continue
		}
		accountConversationCount := 0
		for conversationRows.Next() {
			var id int64
			if conversationRows.Scan(&id) == nil {
				conversations = append(conversations, id)
				accountConversationCount++
			}
		}
		conversationRows.Close()
		if accountConversationCount > 0 {
			accounts = append(accounts, account)
		}
	}
	if len(input.ConversationIDs) > 0 {
		allowed := make(map[int64]struct{}, len(input.ConversationIDs))
		for _, id := range input.ConversationIDs {
			allowed[id] = struct{}{}
		}
		filtered := conversations[:0]
		for _, id := range conversations {
			if _, ok := allowed[id]; ok {
				filtered = append(filtered, id)
			}
		}
		conversations = filtered
		// An explicit conversation scope that has no permitted intersection
		// must fail closed; an empty list otherwise means "all conversations"
		// to the RAG service.
		if len(conversations) == 0 {
			accounts = []int64{}
		}
	}
	payload, _ := json.Marshal(gin.H{"question": input.Question, "platforms": input.Platforms, "top_k": input.TopK,
		"scope": gin.H{"user_id": userID, "source_account_ids": accounts, "conversation_ids": conversations}})
	req, err := http.NewRequestWithContext(c, http.MethodPost, strings.TrimRight(cfg.RAGServiceURL, "/")+"/qa/ask", bytes.NewReader(payload))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "rag request failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "rag service unavailable"})
		return
	}
	defer resp.Body.Close()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

type connectorConfigInput struct {
	SelectedConversations []string   `json:"selected_conversations"`
	HistoryStartAt        *time.Time `json:"history_start_at"`
}

func connectorAccountID(c *gin.Context, pool *pgxpool.Pool, platform string) (int64, bool) {
	var id int64
	err := pool.QueryRow(c, `SELECT id FROM ingestion.source_accounts WHERE internal_account_id=$1 AND platform=$2 AND status='active' ORDER BY updated_at DESC NULLS LAST,id DESC LIMIT 1`, c.GetInt64("user_id"), platform).Scan(&id)
	return id, err == nil
}

func connectorConfig(c *gin.Context, pool *pgxpool.Pool, platform string, _ bool) {
	accountID, ok := connectorAccountID(c, pool, platform)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector not bound"})
		return
	}
	var mode string
	var selected []byte
	var history, updated *time.Time
	err := pool.QueryRow(c, `SELECT listen_mode,selected_conversations,history_start_at,config_updated_at FROM ingestion.collector_bindings WHERE source_account_id=$1 AND collector_type=$2`, accountID, platform).Scan(&mode, &selected, &history, &updated)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector config not found"})
		return
	}
	c.Data(http.StatusOK, "application/json", []byte(fmt.Sprintf(`{"listen_mode":%q,"selected_conversations":%s,"history_start_at":%s,"config_updated_at":%s}`, mode, selected, nullableTime(history), nullableTime(updated))))
}

func nullableTime(value *time.Time) string {
	if value == nil {
		return "null"
	}
	b, _ := json.Marshal(value)
	return string(b)
}

func listConnectorConversations(c *gin.Context, pool *pgxpool.Pool, platform string) {
	accountID, ok := connectorAccountID(c, pool, platform)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector not bound"})
		return
	}
	search, typ := strings.ToLower(strings.TrimSpace(c.Query("search"))), strings.TrimSpace(c.Query("type"))
	page, size := 1, 50
	if _, err := fmt.Sscan(c.DefaultQuery("page", "1"), &page); err != nil || page < 1 {
		page = 1
	}
	if _, err := fmt.Sscan(c.DefaultQuery("page_size", "50"), &size); err != nil || size < 1 {
		size = 50
	}
	if size > 100 {
		size = 100
	}
	pattern := "%" + search + "%"
	rows, err := pool.Query(c, `SELECT c.external_conversation_id,COALESCE(c.name,''),c.conversation_type,c.last_seen_at,c.raw_payload,
        EXISTS(SELECT 1 FROM ingestion.collector_bindings b WHERE b.source_account_id=$1 AND b.collector_type=$2 AND b.selected_conversations ? c.external_conversation_id)
        FROM ingestion.conversations c WHERE c.source_account_id=$1 AND ($3='' OR lower(c.external_conversation_id||' '||COALESCE(c.name,'')) LIKE $4) AND ($5='' OR c.conversation_type=$5)
        ORDER BY c.last_seen_at DESC NULLS LAST,c.id DESC LIMIT $6 OFFSET $7`, accountID, platform, search, pattern, typ, size, (page-1)*size)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to list conversations"})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, name, kind string
		var seen *time.Time
		var raw []byte
		var selected bool
		if rows.Scan(&id, &name, &kind, &seen, &raw, &selected) == nil {
			items = append(items, gin.H{"external_id": id, "name": conversationName(raw, name), "avatar_url": conversationAvatar(raw), "platform": platform, "conversation_type": kind, "last_seen_at": seen, "selected": selected})
		}
	}
	var total int
	_ = pool.QueryRow(c, `SELECT count(*) FROM ingestion.conversations c WHERE c.source_account_id=$1 AND ($2='' OR lower(c.external_conversation_id||' '||COALESCE(c.name,'')) LIKE $3) AND ($4='' OR c.conversation_type=$4)`, accountID, search, pattern, typ).Scan(&total)
	c.JSON(200, gin.H{"conversations": items, "page": page, "page_size": size, "total": total})
}

func updateConnectorConfig(c *gin.Context, pool *pgxpool.Pool, cfg config.Config, platform string) {
	accountID, ok := connectorAccountID(c, pool, platform)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector not bound"})
		return
	}
	var input connectorConfigInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"error": "invalid connector config"})
		return
	}
	clean := make([]string, 0, len(input.SelectedConversations))
	seen := map[string]bool{}
	for _, value := range input.SelectedConversations {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			clean = append(clean, value)
		}
	}
	selected, _ := json.Marshal(clean)
	_, err := pool.Exec(c, `UPDATE ingestion.collector_bindings SET listen_mode='whitelist',selected_conversations=$1::jsonb,history_start_at=$2,config_updated_at=now(),updated_at=now() WHERE source_account_id=$3 AND collector_type=$4`, selected, input.HistoryStartAt, accountID, platform)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to save connector config"})
		return
	}
	connectorConfig(c, pool, platform, false)
}

func listTasks(c *gin.Context, pool *pgxpool.Pool) {
	if pool == nil {
		c.JSON(503, gin.H{"error": "database unavailable"})
		return
	}
	items, err := (tasks.Repository{Pool: pool}).List(c, c.Query("type"), c.Query("status"), 50, 0)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to list tasks"})
		return
	}
	c.JSON(200, gin.H{"tasks": items})
}
func getTask(c *gin.Context, pool *pgxpool.Pool) {
	if pool == nil {
		c.JSON(503, gin.H{"error": "database unavailable"})
		return
	}
	var id int64
	if _, err := fmt.Sscan(c.Param("id"), &id); err != nil {
		c.JSON(400, gin.H{"error": "invalid task id"})
		return
	}
	t, err := (tasks.Repository{Pool: pool}).Get(c, id)
	if err != nil {
		c.JSON(404, gin.H{"error": "task not found"})
		return
	}
	c.JSON(200, t)
}
func retryTask(c *gin.Context, pool *pgxpool.Pool) {
	if pool == nil {
		c.JSON(503, gin.H{"error": "database unavailable"})
		return
	}
	var id int64
	if _, err := fmt.Sscan(c.Param("id"), &id); err != nil {
		c.JSON(400, gin.H{"error": "invalid task id"})
		return
	}
	if err := (tasks.Repository{Pool: pool}).Retry(c, id); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "queued", "id": id})
}
func listWorkers(c *gin.Context, pool *pgxpool.Pool) {
	if pool == nil {
		c.JSON(503, gin.H{"error": "database unavailable"})
		return
	}
	rows, err := pool.Query(c, `SELECT name,status,last_heartbeat,last_error,processed_count,failed_count,updated_at FROM ingestion.worker_runs ORDER BY name`)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to list workers"})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var name, status string
		var hb *time.Time
		var errText *string
		var p, f int64
		var updated time.Time
		if rows.Scan(&name, &status, &hb, &errText, &p, &f, &updated) == nil {
			out = append(out, gin.H{"name": name, "status": status, "last_heartbeat": hb, "last_error": errText, "processed_count": p, "failed_count": f, "updated_at": updated})
		}
	}
	c.JSON(200, gin.H{"workers": out})
}

func listMessages(c *gin.Context, pool *pgxpool.Pool) {
	if pool == nil {
		c.JSON(503, gin.H{"error": "database unavailable"})
		return
	}
	limit := 50
	rows, err := pool.Query(c, `SELECT m.id,m.source,sa.external_account_id,c.external_conversation_id,m.text,m.message_type,m.occurred_at,m.created_at,
		COALESCE(d.status,'pending'),COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'file_name',a.file_name,'extension',a.extension,'mime_type',a.mime_type,'file_category',a.file_category,'parse_status',a.parse_status) ORDER BY a.id) FROM ingestion.attachments a WHERE a.message_id=m.id AND a.is_deleted=false),'[]'::jsonb)
		FROM ingestion.messages m
        JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id
        LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
        LEFT JOIN LATERAL (SELECT status FROM vector_store.documents WHERE raw_message_id=m.raw_message_id ORDER BY updated_at DESC LIMIT 1) d ON true
        WHERE sa.internal_account_id=$1 AND ($2='' OR sa.external_account_id=$2)
        ORDER BY COALESCE(m.occurred_at,m.created_at) DESC LIMIT $3`, c.GetInt64("user_id"), c.Query("account"), limit)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to list messages"})
		return
	}
	defer rows.Close()
	type message struct {
		ID           string     `json:"id"`
		Source       string     `json:"source"`
		Account      string     `json:"source_account_id"`
		Conversation string     `json:"conversation_id,omitempty"`
		Text         string     `json:"text"`
		Type         string     `json:"message_type"`
		OccurredAt   *time.Time `json:"occurred_at,omitempty"`
		CreatedAt    time.Time  `json:"created_at"`
		VectorStatus string     `json:"vector_status"`
		Attachments  []gin.H    `json:"attachments"`
	}
	out := []message{}
	for rows.Next() {
		var m message
		var attachments []byte
		if err := rows.Scan(&m.ID, &m.Source, &m.Account, &m.Conversation, &m.Text, &m.Type, &m.OccurredAt, &m.CreatedAt, &m.VectorStatus, &attachments); err != nil {
			c.JSON(500, gin.H{"error": "failed to read messages"})
			return
		}
		_ = json.Unmarshal(attachments, &m.Attachments)
		if m.Attachments == nil {
			m.Attachments = []gin.H{}
		}
		out = append(out, m)
	}
	c.JSON(200, gin.H{"messages": out})
}

func feishuCallback(c *gin.Context, pool *pgxpool.Pool, redisClient *redisstore.Client, cfg config.Config) {
	failed := func(code string) {
		c.Redirect(http.StatusFound, connectorRedirect(cfg, "/profile", url.Values{"connector": {"feishu"}, "error": {code}}))
	}
	if c.Query("error") != "" {
		failed("feishu_oauth_denied")
		return
	}
	state := c.Query("state")
	if state == "" || redisClient == nil {
		failed("oauth_state_invalid")
		return
	}
	encodedState, err := redisClient.Consume(c, "oauth:feishu:state:"+state)
	if err != nil {
		failed("oauth_state_invalid")
		return
	}
	var st oauthStateRecord
	if json.Unmarshal(encodedState, &st) != nil || st.UserID <= 0 || (st.Intent != "bind" && st.Intent != "rebind") {
		failed("oauth_state_invalid")
		return
	}
	code := c.Query("code")
	if code == "" {
		failed("feishu_oauth_failed")
		return
	}
	payload, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code, "app_id": cfg.FeishuAppID, "app_secret": cfg.FeishuAppSecret})
	req, err := http.NewRequestWithContext(c, http.MethodPost, "https://open.feishu.cn/open-apis/authen/v1/access_token", strings.NewReader(string(payload)))
	if err != nil {
		failed("feishu_oauth_failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		failed("feishu_oauth_failed")
		return
	}
	defer resp.Body.Close()
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			OpenID       string `json:"open_id"`
			Name         string `json:"name"`
			Avatar       string `json:"avatar_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Code != 0 {
		failed("feishu_oauth_failed")
		return
	}
	if body.Data.OpenID == "" || body.Data.AccessToken == "" {
		failed("feishu_oauth_failed")
		return
	}
	tx, err := pool.Begin(c)
	if err != nil {
		failed("connector_bind_failed")
		return
	}
	defer tx.Rollback(c)
	var ownerID int64
	var existingStatus string
	err = tx.QueryRow(c, `SELECT internal_account_id,status FROM ingestion.source_accounts
        WHERE platform='feishu' AND external_account_id=$1 FOR UPDATE`, body.Data.OpenID).Scan(&ownerID, &existingStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failed("connector_bind_failed")
		return
	}
	if err == nil && ownerID != st.UserID {
		failed("connector_owned_by_another_user")
		return
	}
	_ = existingStatus
	var activeID int64
	activeErr := tx.QueryRow(c, `SELECT id FROM ingestion.source_accounts WHERE internal_account_id=$1 AND platform='feishu' AND status='active' LIMIT 1 FOR UPDATE`, st.UserID).Scan(&activeID)
	if st.Intent == "bind" && activeErr == nil {
		failed("connector_already_bound")
		return
	}
	if st.Intent == "rebind" {
		_, _ = tx.Exec(c, `UPDATE ingestion.collector_bindings b SET enabled=false,updated_at=now()
            FROM ingestion.source_accounts sa WHERE b.source_account_id=sa.id AND sa.internal_account_id=$1 AND sa.platform='feishu'`, st.UserID)
		_, _ = tx.Exec(c, `UPDATE ingestion.source_accounts SET status='inactive',updated_at=now()
            WHERE internal_account_id=$1 AND platform='feishu'`, st.UserID)
	}
	var accountID int64
	err = tx.QueryRow(c, `INSERT INTO ingestion.source_accounts(internal_account_id,platform,external_account_id,account_name,credential_ref,status)
        VALUES($1,'feishu',$2,$3,$4,'active')
        ON CONFLICT(platform,external_account_id) DO UPDATE SET account_name=EXCLUDED.account_name,
        credential_ref=EXCLUDED.credential_ref,status='active',updated_at=now()
        RETURNING id`, st.UserID, body.Data.OpenID, body.Data.Name, "credential:feishu:"+body.Data.OpenID).Scan(&accountID)
	if err != nil {
		failed("connector_bind_failed")
		return
	}
	_, err = tx.Exec(c, `INSERT INTO ingestion.collector_bindings(source_account_id,collector_type,db_directory,bound_at,enabled,listen_mode,selected_conversations,config_updated_at)
        VALUES($1,'feishu','',now(),true,'whitelist','[]'::jsonb,now())
        ON CONFLICT(source_account_id,collector_type) DO UPDATE SET enabled=true,last_error=NULL,bound_at=now(),updated_at=now()`, accountID)
	if err != nil {
		failed("connector_bind_failed")
		return
	}
	credential := map[string]any{"access_token": body.Data.AccessToken, "refresh_token": body.Data.RefreshToken, "open_id": body.Data.OpenID, "name": body.Data.Name, "avatar": body.Data.Avatar, "expires_at": time.Now().Add(2 * time.Hour).Unix()}
	encoded, _ := json.Marshal(credential)
	if err := redisClient.Set(c.Request.Context(), "credential:feishu:"+body.Data.OpenID, encoded, 30*24*time.Hour); err != nil {
		failed("credential_store_failed")
		return
	}
	_, _ = tx.Exec(c, `UPDATE identity.users SET feishu_open_id=$1,feishu_name=$2,feishu_avatar=$3,updated_at=now() WHERE id=$4`, body.Data.OpenID, body.Data.Name, body.Data.Avatar, st.UserID)
	if err := tx.Commit(c); err != nil {
		_ = redisClient.Delete(context.WithoutCancel(c), "credential:feishu:"+body.Data.OpenID)
		failed("connector_bind_failed")
		return
	}
	c.Redirect(http.StatusFound, connectorRedirect(cfg, "/profile", url.Values{"connector": {"feishu"}, "status": {"bound"}}))
}

func health(c *gin.Context, pool *pgxpool.Pool) {
	if pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"service": "core", "status": "degraded", "database": "not_configured"})
		return
	}
	if err := pool.Ping(context.WithoutCancel(c.Request.Context())); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"service": "core", "status": "degraded", "database": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"service": "core", "status": "ok", "database": "ok"})
}

func info(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"service": "core", "message": "core service is ready"})
}
