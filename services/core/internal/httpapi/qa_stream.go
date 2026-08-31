package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/core/internal/config"
)

// askQuestion proxies the authenticated, scoped QA request to RAG and keeps
// the stream format stable for the web client. A conversation id is optional,
// so the route also supports a one-off question without history.
func askQuestion(c *gin.Context, pool *pgxpool.Pool, cfg config.Config) {
	var input struct {
		Question        string   `json:"question"`
		Query           string   `json:"query"` // compatibility with the old one-shot client
		Platforms       []string `json:"platforms"`
		Platform        string   `json:"platform"`
		ConversationIDs []int64  `json:"conversation_ids"`
		TopK            int      `json:"top_k"`
		ConversationID  int64    `json:"conversation_id"`
	}
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is required"})
		return
	}
	if strings.TrimSpace(input.Question) == "" {
		input.Question = input.Query
	}
	input.Question = strings.TrimSpace(input.Question)
	if input.Question == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is required"})
		return
	}
	if input.Platforms == nil {
		input.Platforms = []string{}
	}
	if input.Platform != "" && len(input.Platforms) == 0 && !strings.EqualFold(strings.TrimSpace(input.Platform), "all") {
		input.Platforms = []string{input.Platform}
	}
	if input.ConversationIDs == nil {
		input.ConversationIDs = []int64{}
	}
	if input.TopK < 1 || input.TopK > 10 {
		input.TopK = 8
	}
	if input.ConversationID == 0 {
		if raw, ok := c.Get("qa_conversation_id"); ok {
			input.ConversationID, _ = strconv.ParseInt(fmt.Sprint(raw), 10, 64)
		}
	}

	userID := c.GetInt64("user_id")
	var qaMessageID int64
	if input.ConversationID > 0 {
		var title string
		if err := pool.QueryRow(c, `SELECT title FROM qa_conversations WHERE id=$1 AND user_id=$2`, input.ConversationID, userID).Scan(&title); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "qa conversation not found"})
			return
		}
		scopeSnapshot, _ := json.Marshal(gin.H{"platforms": input.Platforms, "conversation_ids": input.ConversationIDs})
		requestID := fmt.Sprint(time.Now().UnixNano())
		if err := pool.QueryRow(c, `INSERT INTO qa_messages(conversation_id,user_id,question,scope_snapshot,request_id) VALUES($1,$2,$3,$4,$5) RETURNING id`, input.ConversationID, userID, input.Question, scopeSnapshot, requestID).Scan(&qaMessageID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save qa question"})
			return
		}
		_, _ = pool.Exec(c, `UPDATE qa_conversations SET title=CASE WHEN message_count=0 AND title='新的对话' THEN LEFT($1,30) ELSE title END, message_count=message_count+1, last_message_at=now(), updated_at=now() WHERE id=$2 AND user_id=$3`, input.Question, input.ConversationID, userID)
	}

	accounts, conversations := permittedSearchScope(c, pool, userID, input.Platforms, input.ConversationIDs)
	payload, _ := json.Marshal(gin.H{
		"question": input.Question, "platforms": input.Platforms, "top_k": input.TopK,
		"scope": gin.H{"user_id": userID, "source_account_ids": accounts, "conversation_ids": conversations},
	})
	req, err := http.NewRequestWithContext(c, http.MethodPost, strings.TrimRight(cfg.RAGServiceURL, "/")+"/qa/ask", bytes.NewReader(payload))
	if err != nil {
		markQAFailed(c, pool, qaMessageID, "rag request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "rag request failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		markQAFailed(c, pool, qaMessageID, "rag service unavailable")
		c.JSON(http.StatusBadGateway, gin.H{"error": "rag service unavailable"})
		return
	}
	defer resp.Body.Close()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)
	var captured bytes.Buffer
	_, _ = io.Copy(c.Writer, io.TeeReader(resp.Body, &captured))
	if qaMessageID > 0 {
		persistQAStream(c, pool, qaMessageID, captured.Bytes())
	}
}

func markQAFailed(c *gin.Context, pool *pgxpool.Pool, messageID int64, message string) {
	if messageID > 0 {
		_, _ = pool.Exec(c, `UPDATE qa_messages SET answer_status='failed',error_message=$1,completed_at=now() WHERE id=$2`, message, messageID)
	}
}

func persistQAStream(c *gin.Context, pool *pgxpool.Pool, messageID int64, body []byte) {
	var answer strings.Builder
	citations := make([]map[string]any, 0)
	var retrieval map[string]any
	requestID := ""
	status := "completed"
	scanner := bufio.NewScanner(bytes.NewReader(body))
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data) != nil {
			continue
		}
		switch event {
		case "meta":
			if value, ok := data["request_id"].(string); ok {
				requestID = value
			}
		case "delta":
			if value, ok := data["text"].(string); ok {
				answer.WriteString(value)
			}
		case "citation":
			citations = append(citations, data)
		case "done":
			if value, ok := data["retrieval"].(map[string]any); ok {
				retrieval = value
			}
		case "error":
			status = "failed"
		}
	}
	cites, _ := json.Marshal(citations)
	meta, _ := json.Marshal(retrieval)
	_, _ = pool.Exec(c, `UPDATE qa_messages SET answer=$1,answer_status=$2,citations=$3,retrieval_meta=$4,request_id=COALESCE(NULLIF($5,''),request_id),completed_at=now() WHERE id=$6`, answer.String(), status, cites, meta, requestID, messageID)
}
