package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/core/internal/config"
)

type searchRequest struct {
	Query         string   `json:"query"`
	Platform      string   `json:"platform"`
	ResourceTypes []string `json:"resource_types"`
	StartAt       string   `json:"start_at"`
	EndAt         string   `json:"end_at"`
	Limit         int      `json:"limit"`
}

// searchInput is the remote-compatible global-search contract. The legacy
// searchRequest below remains available for older one-shot clients.
type searchInput struct {
	Query           string   `json:"query"`
	Platforms       []string `json:"platforms"`
	ResourceTypes   []string `json:"resource_types"`
	SenderName      string   `json:"sender_name"`
	OccurredAfter   string   `json:"occurred_after"`
	OccurredBefore  string   `json:"occurred_before"`
	ConversationIDs []int64  `json:"conversation_ids"`
	Page            int      `json:"page"`
	PageSize        int      `json:"page_size"`
}

type allowedSearchScope struct {
	Platforms        []string
	SourceAccountIDs []int64
	ConversationIDs  []int64
}

func canonicalPlatform(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "feishu":
		return "feishu"
	case "wecom", "work_wechat", "enterprise_wechat":
		return "wecom"
	case "wechat", "personal_wechat", "personal-wechat", "personalwechat":
		return "wechat"
	default:
		return ""
	}
}

func requestedPlatforms(value string) []string {
	if strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "all") {
		return []string{"feishu", "wecom", "wechat"}
	}
	platform := canonicalPlatform(value)
	if platform == "" {
		return nil
	}
	return []string{platform}
}

func platformStorageValues(platform string) []string {
	switch canonicalPlatform(platform) {
	case "wechat":
		return []string{"wechat", "personal_wechat"}
	case "wecom":
		return []string{"wecom"}
	case "feishu":
		return []string{"feishu"}
	default:
		return nil
	}
}

func canonicalPlatformForOutput(platform string) string {
	if strings.EqualFold(strings.TrimSpace(platform), "all") {
		return "all"
	}
	return canonicalPlatform(platform)
}

func parseSearchInt64(value any) (int64, bool) {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return 0, false
	}
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && id > 0
}

func loadAllowedSearchScope(c *gin.Context, pool *pgxpool.Pool, platform string) (allowedSearchScope, error) {
	scope := allowedSearchScope{Platforms: requestedPlatforms(platform)}
	if len(scope.Platforms) == 0 {
		return scope, nil
	}
	for _, item := range scope.Platforms {
		storagePlatforms := platformStorageValues(item)
		if len(storagePlatforms) == 0 {
			continue
		}
		var accountID int64
		var selectedJSON []byte
		err := pool.QueryRow(c, `SELECT sa.id,COALESCE(b.selected_conversations,'[]'::jsonb)
            FROM ingestion.source_accounts sa
            LEFT JOIN LATERAL (
                SELECT selected_conversations
                FROM ingestion.collector_bindings b
                WHERE b.source_account_id=sa.id AND b.collector_type=ANY($2::text[])
                ORDER BY CASE WHEN b.collector_type=$3 THEN 0 ELSE 1 END
                LIMIT 1
            ) b ON true
            WHERE sa.internal_account_id=$1 AND sa.platform=ANY($2::text[]) AND sa.status='active'
            ORDER BY CASE WHEN sa.platform=$3 THEN 0 ELSE 1 END, sa.updated_at DESC NULLS LAST,sa.id DESC LIMIT 1`, c.GetInt64("user_id"), storagePlatforms, item).Scan(&accountID, &selectedJSON)
		if err != nil {
			if err == pgx.ErrNoRows {
				continue
			}
			return scope, err
		}
		var selected []string
		if json.Unmarshal(selectedJSON, &selected) != nil || len(selected) == 0 {
			continue
		}
		rows, err := pool.Query(c, `SELECT id FROM ingestion.conversations
            WHERE source_account_id=$1 AND platform=ANY($2::text[]) AND external_conversation_id=ANY($3::text[])`, accountID, storagePlatforms, selected)
		if err != nil {
			return scope, err
		}
		resolved := 0
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				scope.ConversationIDs = append(scope.ConversationIDs, id)
				resolved++
			}
		}
		rows.Close()
		if resolved > 0 {
			scope.SourceAccountIDs = append(scope.SourceAccountIDs, accountID)
		}
	}
	return scope, nil
}

func callRAG(c *gin.Context, cfg config.Config, path string, body any) (map[string]any, int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	endpoint := strings.TrimRight(cfg.RAGServiceURL, "/") + path
	req, err := http.NewRequestWithContext(c, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("RAG returned invalid JSON")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, resp.StatusCode, fmt.Errorf("RAG returned HTTP %d", resp.StatusCode)
	}
	return result, resp.StatusCode, nil
}

func searchBody(input searchRequest, scope allowedSearchScope) map[string]any {
	limit := input.Limit
	if limit < 1 || limit > 50 {
		limit = 10
	}
	body := map[string]any{
		"query": input.Query, "platforms": scope.Platforms,
		"source_account_ids": scope.SourceAccountIDs,
		"conversation_ids":   scope.ConversationIDs,
		"resource_types":     input.ResourceTypes, "limit": limit, "candidate_top_k": 50,
		"use_vector": true,
	}
	if strings.TrimSpace(input.StartAt) != "" {
		body["start_at"] = input.StartAt
	}
	if strings.TrimSpace(input.EndAt) != "" {
		body["end_at"] = input.EndAt
	}
	return body
}

func enrichSearchItems(c *gin.Context, pool *pgxpool.Pool, raw any) []gin.H {
	items, ok := raw.([]any)
	if !ok {
		return []gin.H{}
	}
	out := make([]gin.H, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		result := gin.H{}
		for key, val := range item {
			result[key] = val
		}
		messageID := strings.TrimSpace(fmt.Sprint(item["message_id"]))
		if messageID == "<nil>" {
			messageID = ""
		}
		if messageID != "" {
			var text, conversationName, senderName, platform string
			var conversationID, senderID *int64
			var occurredAt *time.Time
			err := pool.QueryRow(c, `SELECT m.text,COALESCE(c.name,''),COALESCE(p.display_name,''),sa.platform,
                m.conversation_id,m.sender_id,m.occurred_at
                FROM ingestion.messages m JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id
                LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
                LEFT JOIN ingestion.participants p ON p.id=m.sender_id
                WHERE m.id=$1 AND sa.internal_account_id=$2`, messageID, c.GetInt64("user_id")).Scan(&text, &conversationName, &senderName, &platform, &conversationID, &senderID, &occurredAt)
			if err == nil {
				result["message_id"] = messageID
				result["message_text"], result["conversation_name"], result["sender_name"], result["platform"] = text, conversationName, senderName, canonicalPlatformForOutput(platform)
				result["source"] = canonicalPlatformForOutput(platform)
				result["conversation_id"], result["sender_id"], result["occurred_at"] = conversationID, senderID, occurredAt
				if conversationID != nil {
					result["context"] = conversationContext(c, pool, *conversationID, messageID)
				}
			}
		}
		if id, ok := parseSearchInt64(item["file_id"]); ok {
			var fileName, uploader, conversationName, platform, text string
			var conversationID *int64
			var occurredAt *time.Time
			_ = pool.QueryRow(c, `SELECT COALESCE(a.file_name,''),COALESCE(p.display_name,''),COALESCE(c.name,''),sa.platform,
                    m.conversation_id,m.occurred_at,COALESCE(m.text,'')
                    FROM ingestion.attachments a JOIN ingestion.messages m ON m.id=a.message_id
                    LEFT JOIN ingestion.participants p ON p.id=m.sender_id
                    LEFT JOIN ingestion.conversations c ON c.id=m.conversation_id
                    JOIN ingestion.source_accounts sa ON sa.id=a.source_account_id
                    WHERE a.id=$1 AND sa.internal_account_id=$2`, id, c.GetInt64("user_id")).Scan(&fileName, &uploader, &conversationName, &platform, &conversationID, &occurredAt, &text)
			result["file_id"] = strconv.FormatInt(id, 10)
			result["file_name"], result["uploader"], result["conversation_name"], result["platform"] = fileName, uploader, conversationName, canonicalPlatformForOutput(platform)
			result["source"] = canonicalPlatformForOutput(platform)
			result["conversation_id"], result["occurred_at"] = conversationID, occurredAt
			if text != "" {
				result["message_text"] = text
			}
		}
		out = append(out, result)
	}
	return out
}

func conversationContext(c *gin.Context, pool *pgxpool.Pool, conversationID int64, messageID string) []gin.H {
	rows, err := pool.Query(c, `WITH target AS (
            SELECT COALESCE(occurred_at,created_at) AS at
            FROM ingestion.messages WHERE id=$3 AND conversation_id=$1
        ), picked AS (
            SELECT m.id,COALESCE(p.display_name,'') AS display_name,COALESCE(m.text,'') AS text,m.occurred_at,COALESCE(m.occurred_at,m.created_at) AS sort_at
            FROM ingestion.messages m
            JOIN ingestion.source_accounts sa ON sa.id=m.source_account_id
            LEFT JOIN ingestion.participants p ON p.id=m.sender_id
            CROSS JOIN target
            WHERE m.conversation_id=$1 AND sa.internal_account_id=$2 AND m.is_deleted=false
            ORDER BY ABS(EXTRACT(EPOCH FROM (COALESCE(m.occurred_at,m.created_at) - target.at))),m.id
            LIMIT 7
        )
        SELECT id,display_name,text,occurred_at FROM picked ORDER BY sort_at,id`, conversationID, c.GetInt64("user_id"), messageID)
	if err != nil {
		return []gin.H{}
	}
	defer rows.Close()
	out := make([]gin.H, 0, 7)
	for rows.Next() {
		var id, sender, text string
		var occurred *time.Time
		if rows.Scan(&id, &sender, &text, &occurred) == nil {
			out = append(out, gin.H{"id": id, "sender": sender, "content": text, "time": occurred})
		}
	}
	return out
}

func searchConversationResults(c *gin.Context, pool *pgxpool.Pool, scope allowedSearchScope, query string) []gin.H {
	if len(scope.ConversationIDs) == 0 {
		return []gin.H{}
	}
	rows, err := pool.Query(c, `SELECT c.id,c.name,c.conversation_type,c.last_seen_at,sa.platform
        FROM ingestion.conversations c
        JOIN ingestion.source_accounts sa ON sa.id=c.source_account_id
        WHERE c.id=ANY($1::bigint[]) AND sa.internal_account_id=$2
          AND (c.name ILIKE $3 OR c.external_conversation_id ILIKE $3)
        ORDER BY c.last_seen_at DESC NULLS LAST,c.id DESC LIMIT 20`, scope.ConversationIDs, c.GetInt64("user_id"), "%"+query+"%")
	if err != nil {
		return []gin.H{}
	}
	defer rows.Close()
	out := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var name, kind, platform string
		var lastSeen *time.Time
		if rows.Scan(&id, &name, &kind, &lastSeen, &platform) == nil {
			platform = canonicalPlatformForOutput(platform)
			out = append(out, gin.H{
				"id": fmt.Sprintf("conversation:%d", id), "kind": "chat", "title": name,
				"subtitle": fmt.Sprintf("%s · %s · 最近消息 %s", platform, kind, formatSearchTime(lastSeen)),
				"platform": platform, "source": platform, "conversation_id": id,
				"conversation_name": name, "conversation_type": kind, "last_seen_at": lastSeen,
				"score": 1.0,
			})
		}
	}
	return out
}

func searchQAHistory(c *gin.Context, pool *pgxpool.Pool, platform, query string) []gin.H {
	trimmed := strings.TrimSpace(platform)
	canonical := "all"
	storagePlatforms := []string{}
	if trimmed != "" && !strings.EqualFold(trimmed, "all") {
		canonical = canonicalPlatform(trimmed)
		storagePlatforms = platformStorageValues(canonical)
	}
	if canonical == "" {
		return []gin.H{}
	}
	rows, err := pool.Query(c, `SELECT id,question,answer,platform,citations,created_at
        FROM ingestion.ai_qa_sessions
        WHERE user_id=$1 AND ($2='all' OR platform=ANY($3::text[]))
          AND (question ILIKE $4 OR answer ILIKE $4)
        ORDER BY created_at DESC LIMIT 20`, c.GetInt64("user_id"), canonical, storagePlatforms, "%"+query+"%")
	if err != nil {
		return []gin.H{}
	}
	defer rows.Close()
	out := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var question, answer, sessionPlatform string
		var citations []byte
		var createdAt time.Time
		if rows.Scan(&id, &question, &answer, &sessionPlatform, &citations, &createdAt) != nil {
			continue
		}
		sessionPlatform = canonicalPlatformForOutput(sessionPlatform)
		var parsed any
		_ = json.Unmarshal(citations, &parsed)
		out = append(out, gin.H{
			"id": fmt.Sprintf("qa:%d", id), "kind": "qa", "title": question,
			"content": answer, "answer": answer, "question": question,
			"subtitle": fmt.Sprintf("%s · %s", sessionPlatform, createdAt.Format(time.RFC3339)),
			"platform": sessionPlatform, "source": sessionPlatform, "session_id": id,
			"citations": parsed, "created_at": createdAt, "score": 0.95,
		})
	}
	return out
}

func formatSearchTime(value *time.Time) string {
	if value == nil {
		return "暂无"
	}
	return value.Format("2006-01-02 15:04")
}

func mergeSearchItems(ragItems []gin.H, conversations, qaItems []gin.H) []gin.H {
	out := make([]gin.H, 0, len(ragItems)+len(conversations)+len(qaItems))
	seen := make(map[string]bool)
	appendItem := func(item gin.H) {
		key := fmt.Sprint(item["id"])
		if key == "<nil>" || key == "" {
			key = fmt.Sprintf("%s:%s:%s", item["kind"], item["platform"], item["chunk_id"])
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, item)
	}
	for _, item := range conversations {
		appendItem(item)
	}
	for _, item := range ragItems {
		if _, exists := item["kind"]; !exists {
			if _, isFile := item["file_id"]; isFile {
				item["kind"] = "file"
			} else {
				item["kind"] = "message"
			}
		}
		appendItem(item)
	}
	for _, item := range qaItems {
		appendItem(item)
	}
	return out
}

func search(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input searchRequest
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Query) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_query", "message": "query is required"}})
		return
	}
	scope, err := loadAllowedSearchScope(c, pool, input.Platform)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "scope_failed", "message": "failed to resolve search scope"}})
		return
	}
	if len(scope.SourceAccountIDs) == 0 || len(scope.ConversationIDs) == 0 {
		qaItems := searchQAHistory(c, pool, input.Platform, strings.TrimSpace(input.Query))
		c.JSON(http.StatusOK, gin.H{"query": input.Query, "items": qaItems, "total": len(qaItems), "degraded": false, "timings": gin.H{}})
		return
	}
	result, status, err := callRAG(c, cfg, "/search", searchBody(input, scope))
	if err != nil && status >= 500 {
		c.JSON(status, gin.H{"error": gin.H{"code": "search_unavailable", "message": "search service unavailable"}})
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	ragItems := enrichSearchItems(c, pool, result["items"])
	merged := mergeSearchItems(ragItems, searchConversationResults(c, pool, scope, strings.TrimSpace(input.Query)), searchQAHistory(c, pool, input.Platform, strings.TrimSpace(input.Query)))
	result["items"], result["total"] = merged, len(merged)
	c.JSON(http.StatusOK, result)
}

// searchDocuments serves the remote frontend's paginated search protocol and
// adapts it to the local permission resolver before calling RAG.
func searchDocuments(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input searchInput
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Query) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required"})
		return
	}
	if input.Platforms == nil {
		input.Platforms = []string{}
	}
	if input.ResourceTypes == nil {
		input.ResourceTypes = []string{}
	}
	if input.ConversationIDs == nil {
		input.ConversationIDs = []int64{}
	}
	if input.Page < 1 {
		input.Page = 1
	}
	if input.PageSize < 1 || input.PageSize > 100 {
		input.PageSize = 20
	}
	accounts, conversations := permittedSearchScope(c, pool, c.GetInt64("user_id"), input.Platforms, input.ConversationIDs)
	platforms := make([]string, 0, len(input.Platforms))
	for _, value := range input.Platforms {
		if canonical := canonicalPlatform(value); canonical != "" && !containsString(platforms, canonical) {
			platforms = append(platforms, canonical)
		}
	}
	payload := gin.H{
		"query": strings.TrimSpace(input.Query), "platforms": platforms,
		"resource_types": input.ResourceTypes, "sender_name": strings.TrimSpace(input.SenderName),
		"occurred_after": input.OccurredAfter, "occurred_before": input.OccurredBefore,
		"page": input.Page, "page_size": input.PageSize,
		"scope": gin.H{"user_id": c.GetInt64("user_id"), "source_account_ids": accounts, "conversation_ids": conversations},
	}
	result, status, err := callRAG(c, cfg, "/search", payload)
	if err != nil && status >= 500 {
		c.JSON(status, gin.H{"error": gin.H{"code": "search_unavailable", "message": "search service unavailable"}})
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	result["items"] = enrichSearchItems(c, pool, result["items"])
	c.JSON(status, result)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// permittedSearchScope resolves all requested platform aliases through the
// same selected-conversation policy used by the knowledge-base pages.
func permittedSearchScope(c *gin.Context, pool *pgxpool.Pool, _ int64, platforms []string, requested []int64) ([]int64, []int64) {
	values := platforms
	if len(values) == 0 {
		values = []string{"feishu", "wecom", "wechat"}
	}
	scope := allowedSearchScope{}
	for _, platform := range values {
		part, err := loadAllowedSearchScope(c, pool, platform)
		if err != nil {
			continue
		}
		scope.Platforms = appendUniqueStrings(scope.Platforms, part.Platforms...)
		for _, id := range part.SourceAccountIDs {
			if !containsInt64(scope.SourceAccountIDs, id) {
				scope.SourceAccountIDs = append(scope.SourceAccountIDs, id)
			}
		}
		for _, id := range part.ConversationIDs {
			if !containsInt64(scope.ConversationIDs, id) {
				scope.ConversationIDs = append(scope.ConversationIDs, id)
			}
		}
	}
	if len(requested) > 0 {
		allowed := map[int64]bool{}
		for _, id := range requested {
			allowed[id] = true
		}
		filtered := scope.ConversationIDs[:0]
		for _, id := range scope.ConversationIDs {
			if allowed[id] {
				filtered = append(filtered, id)
			}
		}
		scope.ConversationIDs = filtered
		if len(filtered) == 0 {
			scope.SourceAccountIDs = nil
		}
	}
	return scope.SourceAccountIDs, scope.ConversationIDs
}

func appendUniqueStrings(values []string, additions ...string) []string {
	for _, value := range additions {
		if value != "" && !containsString(values, value) {
			values = append(values, value)
		}
	}
	return values
}

func containsInt64(values []int64, wanted int64) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// askQuestionLegacy retains the pre-history one-shot JSON implementation for
// compatibility with older internal clients. The public QA routes use the
// SSE implementation in qa_stream.go.
func askQuestionLegacy(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input searchRequest
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Query) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_question", "message": "query is required"}})
		return
	}
	scope, err := loadAllowedSearchScope(c, pool, input.Platform)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "scope_failed", "message": "failed to resolve search scope"}})
		return
	}
	if len(scope.SourceAccountIDs) == 0 || len(scope.ConversationIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"query": input.Query, "answer": "当前平台没有可检索的已接入会话。", "citations": []any{}, "items": []any{}})
		return
	}
	result, status, err := callRAG(c, cfg, "/search/answer", searchBody(input, scope))
	if err != nil && status >= 500 {
		c.JSON(status, gin.H{"error": gin.H{"code": "qa_unavailable", "message": "answer service unavailable"}})
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	result["items"] = enrichSearchItems(c, pool, result["items"])
	result["citations"] = enrichSearchItems(c, pool, result["citations"])
	citations, _ := json.Marshal(result["citations"])
	answerText, _ := result["answer"].(string)
	qaDegraded, _ := result["qa_degraded"].(bool)
	if qaDegraded || strings.TrimSpace(answerText) == "" {
		c.JSON(http.StatusOK, result)
		return
	}
	platform := "all"
	if strings.TrimSpace(input.Platform) != "" && !strings.EqualFold(strings.TrimSpace(input.Platform), "all") {
		platform = canonicalPlatform(input.Platform)
	}
	if strings.TrimSpace(platform) == "" {
		platform = "all"
	}
	var sessionID int64
	if err := pool.QueryRow(c, `INSERT INTO ingestion.ai_qa_sessions(user_id,question,answer,platform,citations)
        VALUES($1,$2,$3,$4,$5::jsonb) RETURNING id`, c.GetInt64("user_id"), input.Query, answerText, platform, citations).Scan(&sessionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "qa_persist_failed", "message": "failed to save answer history"}})
		return
	}
	result["session_id"] = sessionID
	c.JSON(http.StatusOK, result)
}

func listQASessions(c *gin.Context, pool *pgxpool.Pool) {
	rows, err := pool.Query(c, `SELECT id,question,answer,platform,citations,created_at
        FROM ingestion.ai_qa_sessions WHERE user_id=$1 ORDER BY created_at DESC LIMIT 100`, c.GetInt64("user_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "qa_history_failed", "message": "failed to list answer history"}})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var question, answer, platform string
		var citations []byte
		var createdAt time.Time
		if rows.Scan(&id, &question, &answer, &platform, &citations, &createdAt) == nil {
			var parsed any
			_ = json.Unmarshal(citations, &parsed)
			platform = canonicalPlatformForOutput(platform)
			items = append(items, gin.H{"id": id, "question": question, "answer": answer, "platform": platform, "citations": parsed, "created_at": createdAt})
		}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": items})
}

func getQASession(c *gin.Context, pool *pgxpool.Pool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_session", "message": "invalid session id"}})
		return
	}
	var question, answer, platform string
	var citations []byte
	var createdAt time.Time
	err = pool.QueryRow(c, `SELECT question,answer,platform,citations,created_at FROM ingestion.ai_qa_sessions WHERE id=$1 AND user_id=$2`, id, c.GetInt64("user_id")).Scan(&question, &answer, &platform, &citations, &createdAt)
	if err == pgx.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "session_not_found", "message": "answer session not found"}})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "qa_history_failed", "message": "failed to load answer history"}})
		return
	}
	var parsed any
	_ = json.Unmarshal(citations, &parsed)
	platform = canonicalPlatformForOutput(platform)
	c.JSON(http.StatusOK, gin.H{"id": id, "question": question, "answer": answer, "platform": platform, "citations": parsed, "created_at": createdAt})
}
