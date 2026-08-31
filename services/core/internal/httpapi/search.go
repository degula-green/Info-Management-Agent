package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"log"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/core/internal/config"
)

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

func searchDocuments(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input searchInput
	bindErr := c.ShouldBindJSON(&input)
	log.Printf("search request query=%q bind_err=%v", input.Query, bindErr)
	if bindErr != nil || strings.TrimSpace(input.Query) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required"})
		return
	}
	if input.Platforms == nil { input.Platforms = []string{} }
	if input.ResourceTypes == nil { input.ResourceTypes = []string{} }
	if input.ConversationIDs == nil { input.ConversationIDs = []int64{} }
	if input.Page < 1 {
		input.Page = 1
	}
	if input.PageSize < 1 || input.PageSize > 100 {
		input.PageSize = 20
	}
	userID := c.GetInt64("user_id")
	accounts, conversations := permittedSearchScope(c, pool, userID, input.Platforms, input.ConversationIDs)
	payload, _ := json.Marshal(gin.H{"query": strings.TrimSpace(input.Query), "platforms": input.Platforms,
		"resource_types": input.ResourceTypes, "sender_name": input.SenderName, "occurred_after": input.OccurredAfter,
		"occurred_before": input.OccurredBefore, "page": input.Page, "page_size": input.PageSize,
		"scope": gin.H{"user_id": userID, "source_account_ids": accounts, "conversation_ids": conversations}})
	req, err := http.NewRequestWithContext(c, http.MethodPost, strings.TrimRight(cfg.RAGServiceURL, "/")+"/search", bytes.NewReader(payload))
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	c.Data(resp.StatusCode, "application/json", data)
}

func permittedSearchScope(c *gin.Context, pool *pgxpool.Pool, userID int64, platforms []string, requested []int64) ([]int64, []int64) {
	rows, err := pool.Query(c, `SELECT sa.id, sa.platform, COALESCE(b.selected_conversations,'[]'::jsonb)
        FROM ingestion.source_accounts sa LEFT JOIN ingestion.collector_bindings b
          ON b.source_account_id=sa.id AND b.collector_type=sa.platform AND b.enabled=true
        WHERE sa.internal_account_id=$1 AND sa.status='active'`, userID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var accounts, conversations []int64
	for rows.Next() {
		var account int64
		var platform string
		var selected []byte
		if rows.Scan(&account, &platform, &selected) != nil {
			continue
		}
		if len(platforms) > 0 {
			allowed := false
			for _, p := range platforms {
				if p == platform {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
		}
		var external []string
		if json.Unmarshal(selected, &external) != nil || len(external) == 0 {
			continue
		}
		cr, e := pool.Query(c, `SELECT id FROM ingestion.conversations WHERE source_account_id=$1 AND external_conversation_id = ANY($2::text[])`, account, external)
		if e != nil {
			continue
		}
		count := 0
		for cr.Next() {
			var id int64
			if cr.Scan(&id) == nil {
				conversations = append(conversations, id)
				count++
			}
		}
		cr.Close()
		if count > 0 {
			accounts = append(accounts, account)
		}
	}
	if len(requested) > 0 {
		allowed := map[int64]bool{}
		for _, id := range requested {
			allowed[id] = true
		}
		filtered := conversations[:0]
		for _, id := range conversations {
			if allowed[id] {
				filtered = append(filtered, id)
			}
		}
		conversations = filtered
		if len(conversations) == 0 {
			accounts = nil
		}
	}
	return accounts, conversations
}
