package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const DefaultMaxBytes int64 = 200 * 1024 * 1024

var allowed = map[string]bool{"pdf": true, "docx": true, "xlsx": true, "pptx": true, "txt": true, "md": true, "csv": true}
var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type Result struct {
	Bucket, Key, ContentHash, ContentType, Extension string
	Size                                             int64
}
type Store struct {
	Client   *minio.Client
	Bucket   string
	MaxBytes int64
}

// NewFromEnv builds the shared store from the collector's standard MinIO
// settings. No credentials are logged or returned to callers.
func NewFromEnv() (*Store, error) {
	maxBytes := DefaultMaxBytes
	if raw := strings.TrimSpace(os.Getenv("CORE_MINIO_MAX_BYTES")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			maxBytes = n
		}
	}
	secure := strings.EqualFold(strings.TrimSpace(os.Getenv("CORE_MINIO_SECURE")), "true")
	return New(os.Getenv("CORE_MINIO_ENDPOINT"), os.Getenv("CORE_MINIO_ACCESS_KEY"), os.Getenv("CORE_MINIO_SECRET_KEY"), os.Getenv("CORE_MINIO_BUCKET"), secure, maxBytes)
}

func New(endpoint, accessKey, secretKey, bucket string, secure bool, maxBytes int64) (*Store, error) {
	endpoint = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(endpoint), "https://"), "http://")
	if endpoint == "" || bucket == "" {
		return nil, fmt.Errorf("minio endpoint and bucket are required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure})
	if err != nil {
		return nil, err
	}
	return &Store{Client: client, Bucket: bucket, MaxBytes: maxBytes}, nil
}

func SafeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Trim(name, " .")
	name = unsafeName.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	if len(name) > 180 {
		name = name[:180]
	}
	return name
}

func ObjectKey(userID, platform, conversationID, messageID, attachmentID, name string) string {
	clean := func(v string) string { v = unsafeName.ReplaceAllString(v, "_"); return strings.Trim(v, "._") }
	return strings.Join([]string{clean(userID), clean(platform), clean(conversationID), clean(messageID), clean(attachmentID) + "-" + SafeName(name)}, "/")
}

func validateHeader(ext string, head []byte) error {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if !allowed[ext] {
		return fmt.Errorf("unsupported attachment extension: %s", ext)
	}
	switch ext {
	case "pdf":
		if len(head) < 5 || string(head[:5]) != "%PDF-" {
			return fmt.Errorf("file header does not match pdf")
		}
	case "docx", "xlsx", "pptx":
		if len(head) < 4 || string(head[:4]) != "PK\x03\x04" {
			return fmt.Errorf("file header does not match office document")
		}
	default:
		if !utf8.Valid(head) {
			return fmt.Errorf("file is not valid UTF-8 text")
		}
	}
	return nil
}

func (s *Store) PutFile(ctx context.Context, path, key, ext, declaredType string) (Result, error) {
	file, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return Result{}, err
	}
	if stat.Size() > s.MaxBytes {
		return Result{}, fmt.Errorf("attachment exceeds %d byte limit", s.MaxBytes)
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return Result{}, err
	}
	head = head[:n]
	if err := validateHeader(ext, head); err != nil {
		return Result{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	digest := sha256.New()
	reader := io.TeeReader(io.LimitReader(file, s.MaxBytes+1), digest)
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return Result{}, err
	}
	if stat.Size() > s.MaxBytes {
		return Result{}, fmt.Errorf("attachment exceeds %d byte limit", s.MaxBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	if err := s.ensureBucket(ctx); err != nil {
		return Result{}, err
	}
	contentType := declaredType
	if contentType == "" {
		contentType = http.DetectContentType(head)
	}
	_, err = s.Client.PutObject(ctx, s.Bucket, key, file, stat.Size(), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return Result{}, err
	}
	return Result{Bucket: s.Bucket, Key: key, ContentHash: hex.EncodeToString(digest.Sum(nil)), ContentType: contentType, Extension: strings.ToLower(strings.TrimPrefix(ext, ".")), Size: stat.Size()}, nil
}

func (s *Store) ensureBucket(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return err
	}
	if !exists {
		return s.Client.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{})
	}
	return nil
}
