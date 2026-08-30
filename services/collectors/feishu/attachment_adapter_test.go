package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type attachmentRow struct {
	messageID, resourceKey, fileName, mimeType, account string
	size                                                *int64
	err                                                 error
}

func (r attachmentRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string), *dest[1].(*string), *dest[2].(*string) = r.messageID, r.resourceKey, r.fileName
	*dest[3].(**int64), *dest[4].(*string), *dest[5].(*string) = r.size, r.mimeType, r.account
	return nil
}

type attachmentQuery struct {
	row          attachmentRow
	attachmentID any
}

func (q *attachmentQuery) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	q.attachmentID = args[0]
	return q.row
}

type recordingDownloader struct {
	ref   AttachmentRef
	calls int
}

func (d *recordingDownloader) Download(_ context.Context, ref AttachmentRef) (io.ReadCloser, FileInfo, error) {
	d.ref, d.calls = ref, d.calls+1
	return io.NopCloser(strings.NewReader("file")), FileInfo{Name: ref.FileName}, nil
}

func TestDownloadByIDUsesExternalMessageIDAndPersistedFileKey(t *testing.T) {
	size := int64(42)
	query := &attachmentQuery{row: attachmentRow{messageID: "om_external", resourceKey: "file_key_real", fileName: "a.pdf", mimeType: "application/pdf", account: "ou_account", size: &size}}
	downloader := &recordingDownloader{}
	body, _, err := (FeishuAttachmentAdapter{Pool: query, Downloader: downloader}).DownloadByID(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if query.attachmentID != int64(99) || downloader.calls != 1 || downloader.ref.MessageID != "om_external" || downloader.ref.ResourceKey != "file_key_real" || downloader.ref.AccountID != "ou_account" {
		t.Fatalf("query=%v downloader=%+v", query.attachmentID, downloader)
	}
}

func TestDownloadByIDRejectsSyntheticResourceKey(t *testing.T) {
	query := &attachmentQuery{row: attachmentRow{messageID: "om_external", resourceKey: "synthetic:msg_internal:0", account: "ou_account"}}
	downloader := &recordingDownloader{}
	_, _, err := (FeishuAttachmentAdapter{Pool: query, Downloader: downloader}).DownloadByID(context.Background(), 100)
	if err == nil || downloader.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, downloader.calls)
	}
}
