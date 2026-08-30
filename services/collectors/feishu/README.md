# Feishu Attachment Download

The collector uses one parser for top-level attachment arrays and the JSON string in `body.content`. It expands every `attachments`/`files` entry, accepts `size` or `file_size`, and accepts `mime_type`, `file_type`, or `mime`. Missing optional metadata is persisted as SQL `NULL`.

`FeishuAttachmentDownloader` streams the resource from:

```text
GET /open-apis/im/v1/messages/{message_id}/resources/{file_key}?type=file
```

The access token comes from the injected provider. A 401 response closes the first response, refreshes the token once, and retries once. Response headers are preferred for file metadata; message metadata is used as fallback. The downloader does not upload to PG or MinIO and does not buffer or limit the file body.

`FeishuAttachmentAdapter.DownloadByID` is the D-worker integration point. It queries PG for the attachment's `m.source_message_id` (the external Feishu message ID) and `a.external_attachment_id` (the persisted Feishu `file_key`), then passes an `AttachmentRef` to the downloader. It never reconstructs the key from `raw_payload`.

An attachment without `file_key` is retained with `synthetic:{message_id}:{index}` as its database idempotency key. The adapter rejects this prefix and never sends it to Feishu. Attachment ingestion does not enqueue temporary `vectorization` or download tasks; D-stage orchestration will use `attachment_download` later.

`NewRedisFeishuAttachmentDownloader` supplies production token callbacks using `credential:feishu:{account}` in Redis and the existing refresh-token endpoint. The collector main loop still does not download attachments; a worker must call the adapter explicitly.

The Feishu app needs the `im:resource` permission in addition to the existing message/chat read permissions. After adding this scope, existing OAuth users must authorize the app again; old access tokens do not gain the new scope automatically.
