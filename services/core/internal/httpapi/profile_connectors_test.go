package httpapi

import (
	"bytes"
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
