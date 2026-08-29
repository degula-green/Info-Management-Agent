# RAG service

使用系统 Python，不创建虚拟环境。依赖由 `uv` 管理。

```powershell
uv pip install --python "C:\Program Files\Python311\python.exe" --target .runtime/python --link-mode copy -r requirements.txt
$env:PYTHONPATH = "$PWD\.runtime\python"
python -m uvicorn app.main:app --reload --port 8000
```

通常直接双击 `../../scripts/start-platform.cmd` 即可；依赖缺失或
`requirements.txt` 发生变化时，启动脚本会自动执行上述安装。

## 消息向量化

规范化消息可直接通过 CLI 写入 PostgreSQL/pgvector。命令会按字符切片，调用
OpenAI 兼容的 `/embeddings` 接口，并以稳定 `chunk_id` 幂等写入：

```powershell
python scripts/vectorize.py --input data/collector-feishu/normalized/messages.jsonl
```

默认只处理前 10 条用于联调；使用 `--limit 0` 才处理全部消息。运行前设置
`RAG_DATABASE_URL`，并先执行 `db/migrations/002_vector_store_skipped.sql`。

RAG 服务也提供 `POST /ingest`，请求体为 `{ "messages": [...] }`，便于后续由
Core/outbox 调用。需要配置 `EMBEDDING_API_BASE_URL`、`EMBEDDING_API_KEY` 和
PostgreSQL、Embedding API 和 Elasticsearch 连接信息。Elasticsearch 在本阶段保留，
不参与向量写入。
