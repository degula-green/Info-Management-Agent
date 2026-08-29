package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Task struct {
	ID          int64          `json:"id"`
	Type        string         `json:"task_type"`
	EntityID    string         `json:"entity_id"`
	Payload     map[string]any `json:"payload"`
	Status      string         `json:"status"`
	Attempts    int            `json:"attempts"`
	MaxAttempts int            `json:"max_attempts"`
	NextRunAt   time.Time      `json:"next_run_at"`
	LockedBy    *string        `json:"locked_by,omitempty"`
	LockedUntil *time.Time     `json:"locked_until,omitempty"`
	LastError   *string        `json:"last_error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
}

type Repository struct{ Pool *pgxpool.Pool }

func (r Repository) Enqueue(ctx context.Context, typ, entity string, payload map[string]any) error {
	b, _ := json.Marshal(payload)
	_, err := r.Pool.Exec(ctx, `INSERT INTO ingestion.worker_tasks(task_type,entity_id,payload) VALUES($1,$2,$3)
ON CONFLICT(task_type,entity_id) DO UPDATE SET payload=EXCLUDED.payload, updated_at=now(),
status=CASE WHEN ingestion.worker_tasks.status IN ('failed','dead') THEN 'pending' ELSE ingestion.worker_tasks.status END,
next_run_at=CASE WHEN ingestion.worker_tasks.status IN ('failed','dead') THEN now() ELSE ingestion.worker_tasks.next_run_at END`, typ, entity, b)
	return err
}

func (r Repository) List(ctx context.Context, typ, status string, limit, offset int) ([]Task, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id,task_type,entity_id,payload,status,attempts,max_attempts,next_run_at,locked_by,locked_until,last_error,created_at,updated_at,completed_at FROM ingestion.worker_tasks WHERE ($1='' OR task_type=$1) AND ($2='' OR status=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`, typ, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var raw []byte
		if err := rows.Scan(&t.ID, &t.Type, &t.EntityID, &raw, &t.Status, &t.Attempts, &t.MaxAttempts, &t.NextRunAt, &t.LockedBy, &t.LockedUntil, &t.LastError, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &t.Payload)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r Repository) Get(ctx context.Context, id int64) (Task, error) {
	var t Task
	var raw []byte
	err := r.Pool.QueryRow(ctx, `SELECT id,task_type,entity_id,payload,status,attempts,max_attempts,next_run_at,locked_by,locked_until,last_error,created_at,updated_at,completed_at FROM ingestion.worker_tasks WHERE id=$1`, id).Scan(&t.ID, &t.Type, &t.EntityID, &raw, &t.Status, &t.Attempts, &t.MaxAttempts, &t.NextRunAt, &t.LockedBy, &t.LockedUntil, &t.LastError, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal(raw, &t.Payload)
	return t, nil
}

func (r Repository) Retry(ctx context.Context, id int64) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE ingestion.worker_tasks SET status='pending', next_run_at=now(), locked_by=NULL, locked_until=NULL, last_error=NULL, updated_at=now() WHERE id=$1 AND status IN ('failed','dead')`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return fmt.Errorf("task not retryable")
	}
	return err
}
