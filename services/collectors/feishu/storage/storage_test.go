package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeNameAndObjectKey(t *testing.T) {
	if got := SafeName(`..\..\secret name?.pdf`); got != "secret_name_.pdf" {
		t.Fatalf("safe name = %q", got)
	}
	key := ObjectKey("u/1", "feishu", "c", "m", "42", "report.pdf")
	if key != "u_1/feishu/c/m/42-report.pdf" {
		t.Fatalf("key = %q", key)
	}
	if strings.Contains(key, "..") || strings.ContainsAny(key, "\\\r\n") {
		t.Fatalf("unsafe key %q", key)
	}
}

func TestValidateHeaders(t *testing.T) {
	cases := []struct {
		ext  string
		head string
		ok   bool
	}{
		{"pdf", "%PDF-1.7", true}, {"pdf", "not-pdf", false},
		{"docx", "PK\x03\x04rest", true}, {"xlsx", "PK\x03\x04rest", true},
		{"txt", "hello world", true}, {"csv", "a,b\n1,2", true},
		{"zip", "PK\x03\x04", false},
	}
	for _, tc := range cases {
		err := validateHeader(tc.ext, []byte(tc.head))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v", tc.ext, err)
		}
	}
}

func TestPutFileRejectsOversizeBeforeClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{MaxBytes: 5}
	if _, err := s.PutFile(context.Background(), path, "x", "txt", "text/plain"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v", err)
	}
}
