package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type execCall struct {
	sql  string
	args []any
}
type recordingExecer struct{ calls []execCall }

func (r *recordingExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.calls = append(r.calls, execCall{sql: sql, args: args})
	return pgconn.CommandTag{}, nil
}

func TestPersistFeishuAttachmentsWritesEveryAttachmentAndNulls(t *testing.T) {
	raw := map[string]any{"message_id": "om_external", "msg_type": "file", "body": map[string]any{"content": `{"attachments":[{"file_key":"key-a","file_name":"a.pdf","size":12,"mime_type":"application/pdf"},{"file_key":"key-b","file_name":"b.bin","file_size":"23","mime":"application/octet-stream"},{"file_name":"metadata-only"}]}`}}
	tx := &recordingExecer{}
	if err := persistFeishuAttachments(context.Background(), tx, raw, "msg_internal", 10, 20, 30); err != nil {
		t.Fatal(err)
	}
	if len(tx.calls) != 4 {
		t.Fatalf("expected 3 attachment inserts plus one download task, got %d", len(tx.calls))
	}
	var inserts []execCall
	for _, call := range tx.calls {
		if strings.Contains(call.sql, "INSERT INTO ingestion.attachments") {
			inserts = append(inserts, call)
		}
	}
	if len(inserts) != 3 {
		t.Fatalf("expected 3 attachment inserts, got %#v", tx.calls)
	}
	if inserts[0].args[3] != "key-a" || inserts[0].args[8] != int64(12) || inserts[0].args[6] != "application/pdf" {
		t.Fatalf("unexpected first args: %#v", inserts[0].args)
	}
	if inserts[0].args[10] != "pending" {
		t.Fatalf("supported document should be pending: %#v", inserts[0].args)
	}
	if inserts[1].args[3] != "key-b" || inserts[1].args[8] != int64(23) || inserts[1].args[6] != "application/octet-stream" {
		t.Fatalf("unexpected second args: %#v", inserts[1].args)
	}
	if inserts[1].args[10] != "not_required" {
		t.Fatalf("binary attachment should not be parsed: %#v", inserts[1].args)
	}
	if inserts[2].args[3] != "synthetic:msg_internal:2" || inserts[2].args[5] != nil || inserts[2].args[6] != nil || inserts[2].args[8] != nil {
		t.Fatalf("missing values must remain NULL: %#v", inserts[2].args)
	}
	if inserts[2].args[10] != "not_required" {
		t.Fatalf("metadata-only attachment should not be parsed: %#v", inserts[2].args)
	}
	foundTask := false
	for _, call := range tx.calls {
		if strings.Contains(call.sql, "attachment_download") {
			foundTask = true
			break
		}
	}
	if !foundTask {
		t.Fatalf("supported attachment should enqueue download task: %#v", tx.calls)
	}
	for _, call := range tx.calls[:3] {
		if strings.Contains(call.sql, "is_deleted") {
			t.Fatalf("message refresh must not change attachment deletion state: %s", call.sql)
		}
	}
}

func TestPersistFeishuDocumentExtensionsArePending(t *testing.T) {
	for _, extension := range []string{"pdf", "docx", "xlsx", "pptx", "txt", "md", "csv"} {
		t.Run(extension, func(t *testing.T) {
			raw := map[string]any{"msg_type": "file", "attachments": []any{map[string]any{"file_key": "key-" + extension, "file_name": "document." + extension}}}
			tx := &recordingExecer{}
			if err := persistFeishuAttachments(context.Background(), tx, raw, "msg", 1, 2, 3); err != nil {
				t.Fatal(err)
			}
			if len(tx.calls) != 2 || !strings.Contains(tx.calls[0].sql, "attachments") || tx.calls[0].args[10] != "pending" {
				t.Fatalf("expected pending, calls=%#v", tx.calls)
			}
		})
	}
}
