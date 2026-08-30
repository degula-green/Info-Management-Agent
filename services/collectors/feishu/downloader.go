package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

type AttachmentRef struct {
	Platform     string
	AccountID    string
	MessageID    string
	ResourceKey  string
	ResourceType string
	FileName     string
	FileSize     int64
	FileType     string
}

type feishuAttachmentMetadata struct {
	ResourceKey *string
	FileName    *string
	FileSize    *int64
	MIMEType    *string
}

type FileInfo struct {
	Name        string
	Size        int64
	ContentType string
	Extension   string
}

type AttachmentDownloader interface {
	Download(ctx context.Context, attachment AttachmentRef) (io.ReadCloser, FileInfo, error)
}

type FeishuAttachmentDownloader struct {
	Client       *http.Client
	BaseURL      string
	AccessToken  func(context.Context) (string, error)
	RefreshToken func(context.Context) (string, error)
}

func NewFeishuAttachmentDownloader(client *http.Client, accessToken func(context.Context) (string, error), refreshToken func(context.Context) (string, error)) *FeishuAttachmentDownloader {
	if client == nil {
		client = http.DefaultClient
	}
	return &FeishuAttachmentDownloader{Client: client, BaseURL: "https://open.feishu.cn/open-apis", AccessToken: accessToken, RefreshToken: refreshToken}
}

func parseFeishuAttachment(raw map[string]any) (AttachmentRef, bool, error) {
	attachments, err := parseFeishuAttachments(raw)
	if err != nil {
		return AttachmentRef{}, false, err
	}
	for _, attachment := range attachments {
		if attachment.ResourceKey != nil {
			return attachmentRef(raw, attachment), true, nil
		}
	}
	if strings.EqualFold(strings.TrimSpace(fmt.Sprint(raw["msg_type"])), "file") {
		return AttachmentRef{}, false, fmt.Errorf("feishu file content has no file_key")
	}
	return AttachmentRef{}, false, nil
}

func parseFeishuAttachments(raw map[string]any) ([]feishuAttachmentMetadata, error) {
	items := attachmentMaps(raw["attachments"])
	items = append(items, attachmentMaps(raw["files"])...)
	if len(items) == 0 {
		body, _ := raw["body"].(map[string]any)
		content, _ := body["content"].(string)
		if content == "" {
			return nil, nil
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			return nil, fmt.Errorf("parse feishu file content: %w", err)
		}
		items = append(items, attachmentMaps(parsed["attachments"])...)
		items = append(items, attachmentMaps(parsed["files"])...)
		if len(items) == 0 && hasAttachmentMetadata(parsed) {
			items = append(items, parsed)
		}
	}
	result := make([]feishuAttachmentMetadata, 0, len(items))
	for _, item := range items {
		result = append(result, feishuAttachmentMetadata{
			ResourceKey: optionalString(item, "file_key"), FileName: optionalString(item, "file_name", "name"),
			FileSize: optionalInt64(item, "size", "file_size"), MIMEType: optionalString(item, "mime_type", "file_type", "mime"),
		})
	}
	return result, nil
}

func attachmentMaps(value any) []map[string]any {
	values, _ := value.([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func hasAttachmentMetadata(item map[string]any) bool {
	for _, key := range []string{"file_key", "file_name", "name", "size", "file_size", "mime_type", "file_type", "mime"} {
		if item[key] != nil {
			return true
		}
	}
	return false
}

func optionalString(item map[string]any, keys ...string) *string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			value = strings.TrimSpace(value)
			return &value
		}
	}
	return nil
}

func optionalInt64(item map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		switch value := item[key].(type) {
		case float64:
			result := int64(value)
			return &result
		case int64:
			result := value
			return &result
		case int:
			result := int64(value)
			return &result
		case string:
			if result, err := strconv.ParseInt(value, 10, 64); err == nil {
				return &result
			}
		}
	}
	return nil
}

func attachmentRef(raw map[string]any, value feishuAttachmentMetadata) AttachmentRef {
	ref := AttachmentRef{Platform: "feishu", MessageID: fmt.Sprint(raw["message_id"]), ResourceType: "file"}
	if value.ResourceKey != nil {
		ref.ResourceKey = *value.ResourceKey
	}
	if value.FileName != nil {
		ref.FileName = *value.FileName
	}
	if value.FileSize != nil {
		ref.FileSize = *value.FileSize
	}
	if value.MIMEType != nil {
		ref.FileType = *value.MIMEType
	}
	return ref
}

func (d *FeishuAttachmentDownloader) Download(ctx context.Context, attachment AttachmentRef) (io.ReadCloser, FileInfo, error) {
	if attachment.MessageID == "" || attachment.ResourceKey == "" {
		return nil, FileInfo{}, fmt.Errorf("message_id and resource key are required")
	}
	base := strings.TrimRight(d.BaseURL, "/")
	u := base + "/im/v1/messages/" + url.PathEscape(attachment.MessageID) + "/resources/" + url.PathEscape(attachment.ResourceKey) + "?type=" + url.QueryEscape("file")
	if d.AccessToken == nil {
		return nil, FileInfo{}, fmt.Errorf("feishu access token provider is required")
	}
	token, err := d.AccessToken(ctx)
	if err != nil {
		return nil, FileInfo{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, FileInfo{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := d.Client.Do(req)
		if err != nil {
			return nil, FileInfo{}, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && d.RefreshToken != nil {
			resp.Body.Close()
			token, err = d.RefreshToken(ctx)
			if err != nil {
				return nil, FileInfo{}, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, FileInfo{}, fmt.Errorf("feishu resource download: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return resp.Body, responseFileInfo(resp, attachment), nil
	}
	return nil, FileInfo{}, fmt.Errorf("feishu resource download unauthorized after token refresh")
}

func responseFileInfo(resp *http.Response, ref AttachmentRef) FileInfo {
	name := ref.FileName
	if value := resp.Header.Get("Content-Disposition"); value != "" {
		if _, params, err := mime.ParseMediaType(value); err == nil && params["filename"] != "" {
			name = params["filename"]
		}
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = ref.FileType
	}
	size := resp.ContentLength
	if size < 0 {
		size = ref.FileSize
	}
	ext := path.Ext(name)
	if ext != "" {
		ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	}
	return FileInfo{Name: name, Size: size, ContentType: contentType, Extension: ext}
}
