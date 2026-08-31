package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func qaConversationID(c *gin.Context) (int64, error) { return strconv.ParseInt(c.Param("id"), 10, 64) }

func createQAConversation(c *gin.Context, pool *pgxpool.Pool) {
	var in struct {
		Title string `json:"title"`
	}
	_ = c.ShouldBindJSON(&in)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "新的对话"
	}
	var id int64
	if err := pool.QueryRow(c, `INSERT INTO qa_conversations(user_id,title) VALUES($1,$2) RETURNING id`, c.GetInt64("user_id"), title).Scan(&id); err != nil {
		c.JSON(500, gin.H{"error": "failed to create qa conversation"})
		return
	}
	c.JSON(201, gin.H{"id": id, "title": title, "message_count": 0})
}

func listQAConversations(c *gin.Context, pool *pgxpool.Pool) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	userID := c.GetInt64("user_id")
	rows, err := pool.Query(c, `SELECT id,title,message_count,last_message_at,created_at,updated_at FROM qa_conversations WHERE user_id=$1 ORDER BY COALESCE(last_message_at,updated_at) DESC,id DESC LIMIT $2 OFFSET $3`, userID, size, (page-1)*size)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to list qa conversations"})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id int64
		var title string
		var count int
		var last, created, updated any
		if rows.Scan(&id, &title, &count, &last, &created, &updated) == nil {
			items = append(items, gin.H{"id": id, "title": title, "message_count": count, "last_message_at": last, "created_at": created, "updated_at": updated})
		}
	}
	var total int
	_ = pool.QueryRow(c, `SELECT count(*) FROM qa_conversations WHERE user_id=$1`, userID).Scan(&total)
	c.JSON(200, gin.H{"items": items, "page": page, "page_size": size, "total": total})
}

func getQAConversation(c *gin.Context, pool *pgxpool.Pool) {
	id, err := qaConversationID(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid conversation id"})
		return
	}
	userID := c.GetInt64("user_id")
	var title string
	var count int
	var last, created, updated any
	if err = pool.QueryRow(c, `SELECT title,message_count,last_message_at,created_at,updated_at FROM qa_conversations WHERE id=$1 AND user_id=$2`, id, userID).Scan(&title, &count, &last, &created, &updated); err != nil {
		c.JSON(404, gin.H{"error": "qa conversation not found"})
		return
	}
	rows, err := pool.Query(c, `SELECT id,question,answer,answer_status,error_message,scope_snapshot,citations,retrieval_meta,request_id,created_at,completed_at FROM qa_messages WHERE conversation_id=$1 AND user_id=$2 ORDER BY created_at,id`, id, userID)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to load qa messages"})
		return
	}
	defer rows.Close()
	messages := make([]gin.H, 0)
	for rows.Next() {
		var mid int64
		var q, a, status string
		var em, request *string
		var scope, cites, meta []byte
		var created, completed any
		if rows.Scan(&mid, &q, &a, &status, &em, &scope, &cites, &meta, &request, &created, &completed) == nil {
			var sv, cv, mv any
			_ = json.Unmarshal(scope, &sv)
			_ = json.Unmarshal(cites, &cv)
			_ = json.Unmarshal(meta, &mv)
			messages = append(messages, gin.H{"id": mid, "question": q, "answer": a, "answer_status": status, "error_message": em, "scope_snapshot": sv, "citations": cv, "retrieval_meta": mv, "request_id": request, "created_at": created, "completed_at": completed})
		}
	}
	c.JSON(200, gin.H{"id": id, "title": title, "message_count": count, "last_message_at": last, "created_at": created, "updated_at": updated, "messages": messages})
}

func updateQAConversation(c *gin.Context, pool *pgxpool.Pool) {
	id, err := qaConversationID(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid conversation id"})
		return
	}
	var in struct {
		Title *string `json:"title"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Title == nil || strings.TrimSpace(*in.Title) == "" {
		c.JSON(400, gin.H{"error": "title is required"})
		return
	}
	var title string
	if err = pool.QueryRow(c, `UPDATE qa_conversations SET title=$1,updated_at=now() WHERE id=$2 AND user_id=$3 RETURNING title`, strings.TrimSpace(*in.Title), id, c.GetInt64("user_id")).Scan(&title); err != nil {
		c.JSON(404, gin.H{"error": "qa conversation not found"})
		return
	}
	c.JSON(200, gin.H{"id": id, "title": title})
}

func deleteQAConversation(c *gin.Context, pool *pgxpool.Pool) {
	id, err := qaConversationID(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid conversation id"})
		return
	}
	tag, err := pool.Exec(c, `DELETE FROM qa_conversations WHERE id=$1 AND user_id=$2`, id, c.GetInt64("user_id"))
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to delete qa conversation"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "qa conversation not found"})
		return
	}
	c.Status(http.StatusNoContent)
}
