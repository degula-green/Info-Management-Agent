# 混合检索开发进度（2026-08-31）

## 已完成

- RAG 检索内核：BM25、kNN、加权 RRF（k=60）、chunk 去重、单文档最多 3 个 chunk、Rerank 失败回退。
- 权限过滤：`user_id`、`source_account_id`、`conversation_id`、`is_deleted=false`、`document_status=completed` 同时注入 BM25 和 kNN。
- ES mapping 增加 `document_status`、`attachment_id`、`file_name`、`document_title`、`conversation_name`；索引写入脚本和 worker 会显式写入完成状态。
- 切片一致性：消息/附件切片将 `file_name`、`document_title`、`conversation_name`、`attachment_id` 提升为 ES 顶层字段，确保 BM25 字段权重生效。
- ES 前置：`index_service.ensure_index` 会幂等创建缺失索引，并为已有索引补充 v2 mapping 字段。
- RAG `POST /qa/ask`：SSE `meta/delta/citation/done`，无证据返回固定提示和空引用，检索异常返回 `error` 后正常 `done`。
- Core `POST /api/qa/ask`：JWT 保护；从已绑定且启用的账号读取监听白名单，并将外部会话 ID 映射为 PG 内部 `conversation_id` 后转发至 RAG。
- 前端 `InfoQuickQAPage.vue`：真实调用 `/api/qa/ask`，解析 SSE，显示回答和引用卡片，引用可跳转消息/附件详情。
- PG 引用回填：新增权威查询，补充平台、发送者、会话类型/名称、时间、文件名、文档摘要和来源位置；回填异常不会中断 SSE。
- 修正消息引用回填分支：只有带 `attachment_id` 的结果走附件文档查询，消息 chunk 按 `message_id` 回查。
- RAG SSE 接口回归：覆盖正常引用、无答案固定提示、检索异常后 `done` 收尾。
- 问答生成层：支持 OpenAI-compatible Chat Completions（`QA_API_BASE_URL/QA_API_KEY/QA_MODEL`），未配置或调用失败时回退到检索片段。
- Rerank：增加 OpenAI-compatible `/rerank` 客户端；配置 `RERANK_API_BASE_URL/RERANK_API_KEY/RERANK_MODEL` 后默认启用，异常自动回退 RRF。
- 查询改写：增加可选 QueryRewriter，调用失败/未配置时退化原问题，最多保留 3 个保序去重改写。
- 离线评测：新增 `scripts/evaluate_retrieval.py` 和 20 条 JSONL 评测模板，自动输出 Recall@5/10/20，并判断 90%/85% 门槛。
- 安全评测：评测脚本同时统计 `permission_errors`、`permission_error_rate`，并输出 `pass_permissions`。
- 稳定性：Core 到 RAG 的 SSE 代理增加响应头超时，RAG 不可用时返回明确错误而不会无限挂起。
- 网关：Nginx 增加 `/api/qa/` SSE 代理并关闭响应缓冲，避免生产入口落入 SPA fallback。
- 观测：`done.retrieval` 增加 `latency_ms`，记录检索内核耗时。
- SSE 稳定性：事件编码支持 PG 返回的 `datetime`，避免 citation 回填后 JSON 序列化中断。
- 联调工具：新增 `scripts/smoke-hybrid-qa.ps1`，从 `INFO_AGENT_JWT` 读取短期 JWT，验证 SSE 事件顺序和 `done` 收尾。
- ES 诊断：新增 `scripts/check_es_index.py`，只输出索引/mapping/status 统计，不输出文档正文。
- 环境示例：Core/RAG `.env.example` 已补充 `RAG_SERVICE_URL`、QA 和 Rerank 配置项。

## 验证结果

- Python：`python -m unittest discover -s services/rag/tests -p 'test_*.py'` —— 25 项通过。
- Go：`go test ./...` —— 全部通过。
- 前端：`npm run type-check` —— 通过。

### 真实环境验证（2026-08-31）

- 远程 PostgreSQL：连接和查询通过；当前可见 7 个附件、134 个文档、171 个 PG chunk。
- 远程 MinIO：`/minio/health/live` 返回 200。
- 本机 Elasticsearch：RAG 配置的 HTTPS 端点可达；`info-agent-chunks-v1` 已通过 `ensure_index` 补齐 v2 字段。
- 三个验收文件均已解析并完成向量化；6 个附件文档已重新写入本机 ES。索引当前 123 个 chunk，`document_status=completed` 为 123。
- 20 条真实评测题（用户 1、启用账号范围）结果：Recall@5=100%、Recall@10=100%、Recall@20=100%，达到 90% 门槛。
- 评测脚本权限字段检查为 0 误召回；由于原始 fixture 未包含逐题权限断言，仍需通过 Core JWT 端到端用例补充权限验收。
- RAG 本机联调：`GET /health` 返回 200；`POST /qa/ask` 返回完整 `meta -> delta -> citation -> done` SSE 流，引用已回填 PG 的平台、发送者、会话和时间字段。
- Core 入口联调：使用内部测试 JWT，`POST /api/qa/ask` 返回完整 SSE；未认证请求返回 401；显式传入无权限会话范围返回正常 SSE 且无 citation（已修复空交集被误当作“全会话”的问题）。
- Core 参数边界：已将省略的 `platforms`/`conversation_ids` 规范化为空数组，避免 Go `nil` slice 序列化为 `null` 导致 RAG 422；省略字段和无权限会话现在均返回正常 SSE，后者无 citation。
- 前端开发服务：Vite 在 `http://127.0.0.1:5173/` 返回 200，入口资源正常；`npm run type-check` 通过。
- 前端登录/问答联调已完成：使用 `lyc5020980@163.com` 成功登录，真实页面发送“系统如何记录消息来源？”后显示非空 AI 回答和 8 条引用。
- 已修复两个实际阻塞：Core 运行时的 `RAG_SERVICE_URL` 指向实际 RAG 端口 `8001`；问答页助手消息改为 Vue 响应式对象，避免 SSE 已收到内容但 DOM 仍为空。
- SSE 解析改为按事件流实时读取，并在 `done` 事件结束，避免代理连接未关闭导致页面长期等待。

## 当前阻塞（已纠正部署拓扑）

项目不要求通过 Docker 启动全部基础设施：PostgreSQL 和 MinIO 使用远端服务，只有 Elasticsearch 使用本机实例。Docker 未安装本身不是阻塞条件。

前置连接条件已确认满足：远端 PostgreSQL/MinIO 地址可达，本机 Elasticsearch 已启动并使用 RAG 配置中的 `ELASTICSEARCH_URL`。

剩余联调风险：外部 Rerank/问答生成服务响应较慢，真实 SSE 请求可能等待其超时；RAG 已实现 RRF/抽取式回答降级，基础检索不依赖该服务。

测试账号备注：环境数据库中实际账号为 `lyc5020980@163.com`；已使用该账号完成真实浏览器登录和问答验证。密码未写入代码、日志或文档。

## 恢复后的验收顺序

1. 启动基础设施并确认 `GET http://localhost:9200`、PG、MinIO 可用。
2. 执行文档解析和向量化，确认三个文件 `document_status=completed` 且 ES 有 chunk/embedding。
3. 登录后从前端问答区发送问题，检查 SSE `meta/delta/citation/done` 顺序和引用跳转。
4. 执行 20 条评测及权限边界用例；目标 Recall@10 >= 90%，如未达到则记录实际值并以 85% 作为退而求其次门槛。
5. 检查未绑定账号、未监听会话、删除/processing/failed 文档不会出现在结果中。
