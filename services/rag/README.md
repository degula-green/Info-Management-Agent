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
PostgreSQL、Embedding API 和 Elasticsearch 连接信息。消息向量化成功后会同时写入
PostgreSQL 和 Elasticsearch；ES 写入失败时文档不会被标记为 completed，便于重试和对账。

## 混合问答接口

内部接口为 `POST /qa/ask`，由 Core 在 JWT 校验后调用。请求必须包含服务端计算的权限范围：

```json
{"question":"项目为什么延期？","platforms":["feishu"],"top_k":8,"scope":{"user_id":7,"source_account_ids":[11],"conversation_ids":[31]}}
```

响应为 `text/event-stream`，事件顺序至少为 `meta`、一个或多个 `delta`/`citation`、`done`；检索失败时发送 `error` 后仍发送 `done`。无证据时 `delta` 为“当前采集的信息中没有找到足够依据。”且 `done.citations` 为空。

问答生成可选配置 `QA_API_BASE_URL`、`QA_API_KEY`、`QA_MODEL`。未配置或调用失败时使用检索片段回退。所有 BM25/kNN 查询都必须带 `document_status=completed` 和 SearchScope 过滤器。

## 消息价值判断

采集器在写入消息表前调用 `POST /evaluate/message`。该接口使用 OpenAI 兼容的
Chat Completions API，默认模型为 `qwen-plus`，请求体为：

```json
{"source":"feishu","message":{"chat_id":"oc_xxx","msg_type":"text","body":{"content":"..."}}}
```

返回 `valuable=false` 时采集器过滤消息；只有明确的布尔 `false` 才会过滤。接口未配置、超时、返回格式错误或模型服务不可用时默认放行，保证原有采集链路不中断。配置
`MESSAGE_VALUE_API_KEY`；未设置时可复用 `EMBEDDING_API_KEY`。
