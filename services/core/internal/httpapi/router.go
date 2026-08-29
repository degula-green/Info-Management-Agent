package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/core/internal/config"
	"info-agent/core/internal/redisstore"
	"info-agent/core/internal/tasks"
)

var oauthStates = struct {
	sync.Mutex
	values map[string]time.Time
}{values: map[string]time.Time{}}

func NewRouter(pool *pgxpool.Pool, cfg config.Config, redisClient *redisstore.Client) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.GET("/health", func(c *gin.Context) { health(c, pool) })
	r.GET("/api/info", info)
	r.GET("/api/tasks", func(c *gin.Context) { listTasks(c, pool) })
	r.GET("/api/tasks/:id", func(c *gin.Context) { getTask(c, pool) })
	r.POST("/api/tasks/:id/retry", func(c *gin.Context) { retryTask(c, pool) })
	r.GET("/api/workers", func(c *gin.Context) { listWorkers(c, pool) })
	r.GET("/api/messages", func(c *gin.Context) { listMessages(c, pool) })
	r.POST("/api/ingestion/wechat/bind", func(c *gin.Context) { proxyWechat(c, cfg, "/bind") })
	r.POST("/api/ingestion/wechat/rebind", func(c *gin.Context) { proxyWechat(c, cfg, "/rebind") })
	r.GET("/api/ingestion/wechat/status", func(c *gin.Context) { proxyWechat(c, cfg, "/status") })
	r.POST("/api/ingestion/wechat/stop", func(c *gin.Context) { proxyWechat(c, cfg, "/stop") })
	r.GET("/api/ingestion/feishu/authorize", func(c *gin.Context) { feishuAuthorize(c, cfg) })
	r.GET("/api/ingestion/feishu/callback", func(c *gin.Context) { feishuCallback(c, pool, redisClient, cfg) })
	return r
}

func proxyWechat(c *gin.Context, cfg config.Config, path string) {
	var body []byte
	if c.Request.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(c.Request.Body, 16*1024))
	}
	method := http.MethodGet
	if c.Request.Method == http.MethodPost {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(c, method, strings.TrimRight(cfg.WechatCollectorURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		c.JSON(502, gin.H{"error": "wechat collector request failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(503, gin.H{"error": "wechat collector unavailable"})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	c.Data(resp.StatusCode, "application/json", data)
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
        COALESCE(d.status,'pending') FROM ingestion.messages m
        JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id
        LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
        LEFT JOIN LATERAL (SELECT status FROM vector_store.documents WHERE raw_message_id=m.raw_message_id ORDER BY updated_at DESC LIMIT 1) d ON true
        WHERE ($1='' OR sa.external_account_id=$1) ORDER BY COALESCE(m.occurred_at,m.created_at) DESC LIMIT $2`, c.Query("account"), limit)
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
	}
	out := []message{}
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.ID, &m.Source, &m.Account, &m.Conversation, &m.Text, &m.Type, &m.OccurredAt, &m.CreatedAt, &m.VectorStatus); err != nil {
			c.JSON(500, gin.H{"error": "failed to read messages"})
			return
		}
		out = append(out, m)
	}
	c.JSON(200, gin.H{"messages": out})
}

func feishuAuthorize(c *gin.Context, cfg config.Config) {
	if cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Feishu OAuth is not configured"})
		return
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		c.JSON(500, gin.H{"error": "failed to create oauth state"})
		return
	}
	state := fmt.Sprintf("%x", b)
	oauthStates.Lock()
	oauthStates.values[state] = time.Now().Add(10 * time.Minute)
	oauthStates.Unlock()
	q := url.Values{"app_id": {cfg.FeishuAppID}, "redirect_uri": {cfg.FeishuRedirectURI}, "state": {state}}
	c.Redirect(http.StatusFound, "https://open.feishu.cn/open-apis/authen/v1/authorize?"+q.Encode())
}

func feishuCallback(c *gin.Context, pool *pgxpool.Pool, redisClient *redisstore.Client, cfg config.Config) {
	if c.Query("error") != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": c.Query("error")})
		return
	}
	state := c.Query("state")
	oauthStates.Lock()
	expiry, ok := oauthStates.values[state]
	delete(oauthStates.values, state)
	oauthStates.Unlock()
	if !ok || time.Now().After(expiry) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired oauth state"})
		return
	}
	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing authorization code"})
		return
	}
	payload, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code, "app_id": cfg.FeishuAppID, "app_secret": cfg.FeishuAppSecret})
	req, err := http.NewRequest(http.MethodPost, "https://open.feishu.cn/open-apis/authen/v1/access_token", strings.NewReader(string(payload)))
	if err != nil {
		c.JSON(500, gin.H{"error": "oauth request failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(502, gin.H{"error": "feishu token exchange failed"})
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
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Code != 0 {
		c.JSON(502, gin.H{"error": "feishu token exchange failed", "message": body.Msg})
		return
	}
	if redisClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis is not configured"})
		return
	}
	credential := map[string]any{"access_token": body.Data.AccessToken, "refresh_token": body.Data.RefreshToken, "open_id": body.Data.OpenID, "name": body.Data.Name, "expires_at": time.Now().Add(2 * time.Hour).Unix()}
	encoded, _ := json.Marshal(credential)
	if err := redisClient.Set(c.Request.Context(), "credential:feishu:"+body.Data.OpenID, encoded, 30*24*time.Hour); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "failed to save credential"})
		return
	}
	_ = pool
	c.JSON(http.StatusOK, gin.H{"status": "authorized", "external_account_id": body.Data.OpenID, "account_name": body.Data.Name})
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
