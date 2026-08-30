# 文档预处理与向量化

## 已实现链路

```text
MinIO 原始附件
  -> attachment_parse worker
  -> MinerU / 本地解析
  -> CanonicalDocument + Markdown（MinIO 派生产物）
  -> vector_store.documents / chunks
  -> vectorization worker
  -> 1536 维 embedding
  -> Elasticsearch info-agent-chunks-v1
```

本阶段不提供附件预览、下载 API、搜索接口或前端页面。附件下载仍由飞书 Go worker、微信 Python worker 负责。

## 文件路由与状态

| 扩展名 | 处理方式 |
| --- | --- |
| `pdf/docx/pptx/xlsx` | MinerU `pipeline` 本地库解析 |
| `txt/md` | UTF-8 本地文本解析 |
| `csv` | 本地 CSV 结构化文本解析 |
| 其他分类 | 不解析，不创建向量化任务 |

解析 worker 只领取已上传 MinIO 且 `download_status=completed`、`parse_status=pending` 的附件。成功后状态为 `completed`；失败后按既有 worker task 退避重试，并将附件标记为 `failed`。没有有效文本也视为失败，不创建向量化任务。

派生产物位于：

```text
{user_id}/{platform}/{conversation_id}/{message_id}/derived/
  {attachment_id}/{attachment_parser_version}/
    document.md
    canonical.json
    assets/...
```

`canonical.json` 保存规范化正文、标题、解析器版本和派生资源引用。PostgreSQL 只保存正文、摘要 metadata 和对象 key，不保存派生资源二进制。

## 幂等与数据边界

- 消息文本使用 `raw_message_id + document_type + processor_version`。
- 附件文档使用 `attachment_id + document_type + attachment_parser_version`。
- 同一消息下多个附件因此独立处理，不会互相覆盖。
- 下载器完成时重置解析任务；相同附件内容哈希和解析版本的已完成文档会直接跳过。
- 内容哈希或解析版本改变时，旧附件文档 PG chunks 和 ES chunks 先被删除，再重新解析和写入。
- 文档 chunk 以标题、段落、表格边界优先切分；超过现有大小限制才按字符拆分。`chunk_id` 是 `document_id + VECTOR_PROCESSOR_VERSION + position` 的稳定哈希。
- PG `vector_store.chunks.embedding` 始终写 `NULL`，向量只写 ES。ES `_id` 等于 `chunk_id`，维度严格为 1536。

## 配置与部署

新增配置：

```dotenv
RAG_ATTACHMENT_PARSE_WORKER_ENABLED=true
RAG_ATTACHMENT_PARSE_CONCURRENCY=1
RAG_ATTACHMENT_PARSE_MAX_FILE_SIZE=209715200
RAG_ATTACHMENT_PARSE_POLL_INTERVAL=5
RAG_ATTACHMENT_PARSE_BATCH_SIZE=1
MINERU_DEVICE=cpu
MINERU_MODEL_DIR=
MINERU_VERSION=3.4.5
ATTACHMENT_PARSER_VERSION=attachment-v1
```

复用现有 `CORE_MINIO_*` 配置。默认单文件最大 200 MB、单个解析 worker 实例；GPU 只能通过部署配置显式启用。

MinerU 固定为 `mineru[pipeline]==3.4.5`，由 RAG Python 依赖安装。模型文件必须在部署阶段预下载并固定在运行环境可访问的位置；运行中的 worker 不应依赖临时网络下载。请在使用前确认 MinerU Open Source License 对当前部署方式的适用要求。

`scripts/start-dev.ps1` 会额外启动：

```powershell
python -m app.attachment_parse_main
```

它与 FastAPI/RAG 文本向量 worker 分进程运行，避免长文档解析阻塞消息向量化。

## 必须执行的迁移

执行前需单独确认并应用：

```text
db/migrations/008_attachment_document_identity.sql
```

该迁移不新增表。它将旧的广义 `documents` 唯一约束替换为消息和附件各自的部分唯一索引。

## 验证结果

- `python -m compileall app tests`：通过。
- `python -m unittest discover -s tests -v`：通过。
- 微信附件测试：通过。
- `go test ./...`：Core 和飞书 collector 通过。
- `npm run build`：通过。
- `git diff --check`：通过。

尚未执行真实数据库迁移、MinerU 模型下载、真实附件解析、MinIO 写入或 Elasticsearch 写入。
