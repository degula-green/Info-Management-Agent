package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"info-agent/core/internal/config"
)

func TestValidAvatarAcceptsPNG(t *testing.T) {
	pngHeader := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0}
	data, contentType, extension, err := validAvatar(bytes.NewReader(pngHeader))
	if err != nil {
		t.Fatalf("validAvatar returned error: %v", err)
	}
	if len(data) != len(pngHeader) || contentType != "image/png" || extension != ".png" {
		t.Fatalf("unexpected avatar result: len=%d type=%q extension=%q", len(data), contentType, extension)
	}
}

func TestValidAvatarRejectsUnsupportedAndOversizedFiles(t *testing.T) {
	if _, _, _, err := validAvatar(strings.NewReader("plain text")); err == nil {
		t.Fatal("expected unsupported image error")
	}
	if _, _, _, err := validAvatar(bytes.NewReader(make([]byte, maxAvatarSize+1))); err == nil || !strings.Contains(err.Error(), "5 MB") {
		t.Fatalf("expected avatar size error, got %v", err)
	}
}

func TestConnectorRedirectUsesConfiguredWebBaseURL(t *testing.T) {
	got := connectorRedirect(config.Config{WebBaseURL: "https://app.example.test/"}, "/profile", url.Values{"status": {"bound"}})
	if got != "https://app.example.test/profile?status=bound" {
		t.Fatalf("unexpected redirect: %s", got)
	}
}

func TestStopWechatCollectorRejectsNonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	if err := stopWechatCollector(context.Background(), config.Config{WechatCollectorURL: server.URL}, 42); err == nil {
		t.Fatal("expected non-success collector response to fail")
	}
}

func TestStopWechatCollectorSendsInternalIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/stop" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Info-Agent-User-ID"); got != "42" {
			t.Fatalf("unexpected user header: %q", got)
		}
		if got := r.Header.Get("X-Info-Agent-Collector-Token"); got != "internal-token" {
			t.Fatalf("unexpected collector token header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := stopWechatCollector(context.Background(), config.Config{WechatCollectorURL: server.URL, CollectorToken: "internal-token"}, 42); err != nil {
		t.Fatalf("stopWechatCollector returned error: %v", err)
	}
}

func TestWechatConnectorErrorMessageLocalizesPathErrors(t *testing.T) {
	got := wechatConnectorErrorMessage("db_dir must be an existing local absolute path")
	if got != "本机微信数据目录不存在或不是绝对路径" {
		t.Fatalf("unexpected localized error: %q", got)
	}
	if got := wechatConnectorErrorMessage("custom collector error"); got != "custom collector error" {
		t.Fatalf("unexpected custom error: %q", got)
	}
}
