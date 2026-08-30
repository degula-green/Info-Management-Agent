package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseFeishuAttachment(t *testing.T) {
	ref, found, err := parseFeishuAttachment(map[string]any{"message_id": "om_1", "msg_type": "file", "body": map[string]any{"content": `{"file_key":"file_v2","file_name":"report.pdf","file_size":42,"file_type":"application/pdf"}`}})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if ref.ResourceKey != "file_v2" || ref.FileName != "report.pdf" || ref.FileSize != 42 || ref.FileType != "application/pdf" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}

func TestParseFeishuAttachmentInvalidContent(t *testing.T) {
	_, _, err := parseFeishuAttachment(map[string]any{"msg_type": "file", "body": map[string]any{"content": "not-json"}})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseFeishuMultipleAttachmentsAndMissingMetadata(t *testing.T) {
	raw := map[string]any{"message_id": "om_multi", "msg_type": "file", "body": map[string]any{"content": `{"attachments":[{"file_key":"k1","file_name":"a.txt"},{"file_key":"k2","file_type":"application/octet-stream"}]}`}}
	attachments, err := parseFeishuAttachments(raw)
	if err != nil || len(attachments) != 2 {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	if attachments[0].ResourceKey == nil || *attachments[0].ResourceKey != "k1" || attachments[0].FileSize != nil || attachments[1].MIMEType == nil || *attachments[1].MIMEType != "application/octet-stream" {
		t.Fatalf("unexpected attachments: %+v", attachments)
	}
}

func TestParseFeishuAttachmentMissingKey(t *testing.T) {
	_, found, err := parseFeishuAttachment(map[string]any{"msg_type": "file", "body": map[string]any{"content": `{"file_name":"missing-key"}`}})
	if found || err == nil || !strings.Contains(err.Error(), "file_key") {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestParseFeishuAttachmentsPreservesUndownloadableMetadata(t *testing.T) {
	raw := map[string]any{"message_id": "om_1"}
	raw["attachments"] = []any{map[string]any{"file_name": "metadata-only.txt"}, map[string]any{"file_key": "real-key"}}
	attachments, err := parseFeishuAttachments(raw)
	if err != nil || len(attachments) != 2 || attachments[0].ResourceKey != nil || attachments[1].ResourceKey == nil || *attachments[1].ResourceKey != "real-key" {
		t.Fatalf("unexpected attachments=%+v err=%v", attachments, err)
	}
}

func TestFeishuAttachmentDownloadStreamAndMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/im/v1/messages/om_1/resources/file_v2" || r.URL.Query().Get("type") != "file" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("missing auth")
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="server.pdf"`)
		io.WriteString(w, "file-bytes")
	}))
	defer server.Close()
	d := NewFeishuAttachmentDownloader(server.Client(), func(context.Context) (string, error) { return "token", nil }, nil)
	d.BaseURL = server.URL + "/open-apis"
	body, info, err := d.Download(context.Background(), AttachmentRef{MessageID: "om_1", ResourceKey: "file_v2", FileName: "fallback.txt", FileSize: 3, FileType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if string(data) != "file-bytes" || info.Name != "server.pdf" || info.ContentType != "application/pdf" || info.Extension != "pdf" {
		t.Fatalf("data=%q info=%+v", data, info)
	}
}

func TestFeishuAttachmentDownloadRefreshesOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer refreshed" {
			t.Fatal("unexpected token")
		}
		io.WriteString(w, "ok")
	}))
	defer server.Close()
	refreshed := false
	d := NewFeishuAttachmentDownloader(server.Client(), func(context.Context) (string, error) { return "old", nil }, func(context.Context) (string, error) { refreshed = true; return "refreshed", nil })
	d.BaseURL = server.URL
	body, _, err := d.Download(context.Background(), AttachmentRef{MessageID: "m", ResourceKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if !refreshed || attempts != 2 {
		t.Fatalf("refreshed=%v attempts=%d", refreshed, attempts)
	}
}

func TestFeishuAttachmentDownloadMetadataFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "abc") }))
	defer server.Close()
	d := NewFeishuAttachmentDownloader(server.Client(), func(context.Context) (string, error) { return "token", nil }, nil)
	d.BaseURL = server.URL
	body, info, err := d.Download(context.Background(), AttachmentRef{MessageID: "m", ResourceKey: "k", FileName: "fallback.txt", FileSize: 3, FileType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if info.Name != "fallback.txt" || info.Size != 3 || !strings.HasPrefix(info.ContentType, "text/plain") || info.Extension != "txt" {
		t.Fatalf("unexpected fallback info: %+v", info)
	}
}

func TestFeishuAttachmentDownloadRefreshFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "expired", http.StatusUnauthorized) }))
	defer server.Close()
	d := NewFeishuAttachmentDownloader(server.Client(), func(context.Context) (string, error) { return "old", nil }, func(context.Context) (string, error) { return "", context.Canceled })
	d.BaseURL = server.URL
	if _, _, err := d.Download(context.Background(), AttachmentRef{MessageID: "m", ResourceKey: "k"}); err != context.Canceled {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFeishuAttachmentDownloadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) }))
	defer server.Close()
	d := NewFeishuAttachmentDownloader(server.Client(), func(context.Context) (string, error) { return "token", nil }, nil)
	d.BaseURL = server.URL
	_, _, err := d.Download(context.Background(), AttachmentRef{MessageID: "m", ResourceKey: "k"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("unexpected error: %v", err)
	}
}
