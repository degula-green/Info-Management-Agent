# 混合检索方案

## 1. 目标与成功标准

本方案用于阶段 8 的 AI 问答快速模式，目标是在用户已采集信息的权限边界内，通过全文检索和语义检索协同召回相关内容。

验收标准：

- 评测集上的 `Recall@10 >= 90%`。
- 权限误召回率为 0。
- 只使用已绑定平台、正在监听的会话和已完成向量化的 chunk。
- 每个回答都返回可回溯到文件、消息或会话的引用。
- 单路检索或 Rerank 失败时按约定降级，不泄露数据。

召回率采用 Recall@K：人工标注的相关 chunk 或 document 出现在前 K 个结果中的比例。主指标为 `Recall@10`，同时记录 `Recall@5` 和 `Recall@20`。

## 2. 当前项目状态

当前 ES chunk 索引已有：

- `content`：`text` 类型，可用于 BM25。
- `embedding`：1536 维 `dense_vector`，已开启 cosine 向量索引，可用于 kNN。

当前缺口：

- `services/rag/app/services/search_service.py` 仍是占位实现。
- `services/rag/app/routers/search.py` 只提供占位的 `/search` 接口。
- 附件文件名主要位于不可索引的 `metadata` 中。
- ES 缺少明确的 `document_status`，不能稳定排除未完成向量化的文档。

PG 是附件、消息、会话和权限的权威数据源；ES 保存召回所需的 chunk、文本、向量及必要的来源/权限冗余字段；文件二进制和对象存储地址不写入 ES。

## 3. 端到端流程

```text
用户问题
  -> JWT 鉴权
  -> Core 计算 SearchScope
  -> 问题预处理
  -> 查询改写/多查询生成
  -> BM25 召回
  -> kNN 召回
  -> 候选集合合并
  -> 加权 RRF 融合
  -> chunk/document 去重
  -> Rerank
  -> 上下文构建
  -> LLM 回答
  -> 返回答案与引用
```

### 3.1 鉴权和权限范围

前端不得直接决定 `user_id`、`source_account_id` 或任意会话范围。Core 从 JWT 获取当前用户，根据绑定的平台账号和监听白名单生成范围，再传给 RAG。

```python
@dataclass
class SearchScope:
    user_id: int
    source_account_ids: list[int]
    conversation_ids: list[int]
```

### 3.2 问题预处理

保留原始问题，并按需生成关键词查询和语义改写查询。查询改写用于扩大候选集，不能绕过权限过滤，也不能替代最终答案依据；改写失败时退化为原始问题。

### 3.3 双路召回

对原始问题和改写查询分别执行 BM25 与 kNN，所有查询使用完全相同的权限、删除状态和文档状态过滤条件。

BM25 负责关键词、专有名词、编号、文件名、时间和原文表述；kNN 负责同义表达和词汇不同但语义相近的内容。

### 3.4 融合、排序和回答

多路结果按 `chunk_id` 合并后使用加权 RRF，RRF 后保留候选 Top 50，经过 Rerank 后取最终 Top 5 到 Top 10 构建上下文，并返回来源引用。

## 4. 检索策略

### 4.1 BM25

查询字段优先级为：

```text
document_title / file_name
content
conversation_name
```

初始每个查询召回 Top 100，组合使用 `match`、`match_phrase` 和 `multi_match`。正文权重最高，同时保留标题和文件名命中能力。

### 4.2 kNN

对原始问题和每个语义改写生成 embedding；同一请求内相同文本只生成一次向量。每个查询初始召回 Top 100，并使用与 BM25 相同的过滤器。

### 4.3 加权 RRF

```text
RRF(chunk) =
  keyword_weight / (rrf_k + keyword_rank)
  + vector_weight / (rrf_k + vector_rank)
```

初始参数：

```text
rrf_k = 60
keyword_weight = 0.5
vector_weight = 0.5
```

专有名词、编号或文件名召回不足时提高 keyword 权重；同义问题召回不足时提高 vector 权重。参数修改必须通过离线评测验证。

### 4.4 Rerank

```text
RRF Top 50
  -> Rerank
  -> 最终 Top 8
```

Reranker 输入问题和候选内容，可附带文件名、标题和来源位置。Rerank 只改变排序，不改变权限范围和候选集合。默认启用；超时或不可用时回退到 RRF 排序。

### 4.5 去重

先按 `chunk_id` 去重，再按 `document_id` 或 `attachment_id` 限制同一文档的候选数量。相邻 chunk 可以合并，但必须保留原始位置和引用字段。

## 5. 权限和数据过滤

所有受保护检索必须满足：

```text
user_id = 当前用户
source_account_id ∈ 已绑定平台账号
conversation_id ∈ 正在监听的会话
is_deleted = false
document_status = completed
```

`document_status = completed` 用于问答和向量检索；处理失败、处理中或被删除的内容不能进入上下文。

过滤条件必须同时应用于 BM25 和 kNN，不能先搜索全库再在应用层删除无权结果。ES 召回后仍需由 Core 批量回查 PG 做最终权限校验和实体详情补全。

## 6. 上下文、来源归因和引用

统一内部结果结构：

```python
@dataclass
class SearchResult:
    chunk_id: str
    document_id: int | None
    attachment_id: int | None
    message_id: str | None
    conversation_id: int | None
    content: str
    score: float
    rank: int
    rerank_score: float | None
    file_name: str | None
    source_position: str | None
```

ES 负责召回，Core/PG 负责批量补全来源身份。最终结果必须能够回答“来自哪个平台、哪个用户、哪个群聊或私聊、什么时间发送”。补全字段包括：

```text
platform
sender_id / sender_name
conversation_id / conversation_name
conversation_type: group | direct
occurred_at
```

消息结果需要展示平台、发送者、会话类型和名称、发送时间以及命中消息片段。文档结果除上述消息来源外，还需要展示：

```text
file_name
extension
document_summary
source_position
chunk_position
```

附件的来源通过 `attachment -> message -> conversation` 关联得到。`document_summary` 应来自已解析的文档内容，不能根据文件名猜测；可以保存于 PG 文档 metadata，并在检索结果回填时返回。

上下文按 Rerank 相关性排序，限制总 token 数，合并相邻 chunk，限制单文档占比，并为每个片段保留 `document_id`、`attachment_id`、`message_id`、`conversation_id` 和来源位置。来源身份信息由后端结构化注入，LLM 不得自行编造。

最终 `POST /api/qa/ask` 返回答案和 citations，引用必须能定位到 PG 中的文件、消息或会话。没有证据的回答必须使用固定提示：

```text
当前采集的信息中没有找到足够依据。
```

此时 `citations` 必须为空。

### 6.1 AI 问答接口契约

```http
POST /api/qa/ask
Authorization: Bearer <jwt>
Content-Type: application/json
Accept: text/event-stream
```

请求体：

```json
{
  "question": "项目为什么延期？",
  "platforms": ["feishu"],
  "conversation_ids": [45, 46],
  "top_k": 8
}
```

固定行为：

- `question` 必填；`platforms` 和 `conversation_ids` 只能缩小 JWT 对应的权限范围。
- 最终上下文数量为 8；BM25 每路 Top 100，kNN 每路 Top 100。
- RRF 使用 `k=60`，关键词/向量权重为 `0.5/0.5`。
- 查询改写最多 3 个问题。
- Rerank 启用，处理 RRF Top 50，输出最终 Top 8；失败时回退到 RRF 顺序。
- 不持久化问答历史。

响应使用 SSE，事件至少包括：

```text
event: meta
data: {"request_id":"...","mode":"hybrid"}

event: delta
data: {"text":"项目延期主要是因为供应商交付延迟。"}

event: citation
data: {"citation_id":"cite-1","type":"message","platform":"feishu","sender_name":"张三","conversation_name":"后端学习群","conversation_type":"group","sent_at":"2026-08-31T15:20:00+08:00","snippet":"供应商交付延迟，项目需要顺延一周。","message_id":"msg_xxx"}

event: citation
data: {"citation_id":"cite-2","type":"document","platform":"feishu","sender_name":"李四","conversation_name":"后端学习群","conversation_type":"group","sent_at":"2026-08-30T18:00:00+08:00","file_name":"项目方案.docx","document_summary":"项目计划和交付风险说明。","snippet":"供应商交付延迟导致项目延期。","document_id":456,"attachment_id":123}

event: done
data: {"citations":["cite-1","cite-2"],"retrieval":{"keyword_hits":100,"vector_hits":100,"fused_hits":50,"reranked_hits":8}}
```

来源中的 `sender_name`、`conversation_name`、`conversation_type` 和 `sent_at` 必须由 PG 回填；LLM 只组织回答文本，不生成来源事实。前端收到 `citation` 后应支持打开消息详情、会话上下文或文件预览。

## 7. 评测和持续改进

建立人工标注评测集，覆盖关键词明确、同义表达、文件名/编号、跨 chunk、多文档综合、无答案和权限边界问题。每条问题标注相关 `document_id` 或 `chunk_id` 及可接受引用范围。

持续记录：

```text
Recall@5
Recall@10
Recall@20
MRR
nDCG
权限误召回率
Rerank 降级率
端到端延迟
```

召回不足时按固定顺序调优：

1. 检查数据完整性和 chunk 切分。
2. 调整 BM25 字段、分词器和 `minimum_should_match`。
3. 调整 kNN 候选数量和相似度参数。
4. 增加或优化查询改写。
5. 调整 RRF 权重。
6. 调整 Rerank 候选数量和模型。
7. 最后调整上下文截取策略。

每次修改 chunk、mapping、召回参数、RRF 权重、查询改写或 reranker 后，必须重新运行离线回归。若 `Recall@20` 仍不足 90%，优先扩大候选池和查询覆盖，不通过提高 LLM 温度解决召回问题。

### 7.1 三个真实文档的首批测试数据

测试素材来自以下三个用户上传文件：

```text
总模块分析.docx
信息管理Agent第一版需求说明.docx
2026暑假青云前端第一组模板.pptx
```

当前入库状态（以实际查询结果为准）：

```text
MinIO：三个文件均已上传
PostgreSQL attachments：download_status=completed，parse_status=completed
PostgreSQL documents：document_id=142/143/144，status=completed
Elasticsearch：三个文件对应 62 个 chunk（14/40/8）
```

因此以下数据可以直接用于检索评测；若运行时状态与此记录不一致，应以 PostgreSQL 和 Elasticsearch 的实时查询结果为准。

评测数据格式：

```json
{
  "id": "doc-001",
  "question": "信息管理 Agent 第一版的核心目标是什么？",
  "expected_files": ["信息管理Agent第一版需求说明.docx"],
  "expected_topics": ["小范围内部试用", "统一检索", "来源、发送者、时间", "AI 问答引用"],
  "type": "keyword"
}
```

首批问题：

| ID | 测试问题 | 预期文件 | 类型 |
|---|---|---|---|
| doc-001 | 信息管理 Agent 第一版的核心目标是什么？ | 信息管理Agent第一版需求说明.docx | keyword |
| doc-002 | 第一版计划支持多少用户、多少个真实项目？ | 信息管理Agent第一版需求说明.docx | exact-number |
| doc-003 | 系统如何记录消息来源，回答中需要展示哪些来源信息？ | 信息管理Agent第一版需求说明.docx | semantic |
| doc-004 | 文件上传后需要经过哪些处理步骤才能用于问答？ | 信息管理Agent第一版需求说明.docx | workflow |
| doc-005 | 哪些文件类型在第一版范围内，哪些类型只保留元数据？ | 信息管理Agent第一版需求说明.docx | keyword |
| doc-006 | 普通用户、项目成员、管理员和超级管理员的权限有什么区别？ | 信息管理Agent第一版需求说明.docx | permission |
| doc-007 | 全局搜索需要支持哪些筛选条件和结果信息？ | 信息管理Agent第一版需求说明.docx | semantic |
| doc-008 | 系统有哪些主要业务模块？ | 总模块分析.docx | semantic |
| doc-009 | 投递信息模块需要展示和筛选哪些字段？ | 总模块分析.docx | keyword |
| doc-010 | 面试通知模块如何通知候选人，候选人可以进行哪些操作？ | 总模块分析.docx | workflow |
| doc-011 | 问答模块中管理员和普通用户分别可以做什么？ | 总模块分析.docx | permission |
| doc-012 | 前端小组汇报 PPT 的目录包含哪些部分？ | 2026暑假青云前端第一组模板.pptx | exact |
| doc-013 | PPT 中关于登录安全的展示方案是什么？ | 2026暑假青云前端第一组模板.pptx | semantic |
| doc-014 | PPT 中学习笔记使用了哪些地址或链接？ | 2026暑假青云前端第一组模板.pptx | keyword |
| doc-015 | PPT 中算法分享部分提到了什么内容？ | 2026暑假青云前端第一组模板.pptx | semantic |
| doc-016 | PPT 中下一阶段的学习计划是什么？ | 2026暑假青云前端第一组模板.pptx | workflow |
| doc-017 | 哪份文档描述了文件解析、Embedding 和 AI 问答引用要求？ | 信息管理Agent第一版需求说明.docx | cross-document |
| doc-018 | 哪份材料同时包含 Git、MySQL、Redis 学习总结和前端小组汇报？ | 2026暑假青云前端第一组模板.pptx | cross-section |
| doc-019 | 请比较需求说明中的权限设计和总模块分析中的角色权限设计。 | 信息管理Agent第一版需求说明.docx、总模块分析.docx | multi-document |
| doc-020 | 这些材料中是否说明了如何处理没有足够证据的问题？ | 信息管理Agent第一版需求说明.docx | no-answer-boundary |

每条问题执行时记录：

```text
query_rewrites
bm25_hits
knn_hits
rrf_hits
reranked_hits
returned_citations
latency_ms
```

相关性标注建议至少包含目标文件和章节；文档向量化完成后，再补充实际 `document_id`、`attachment_id` 和 `chunk_id`。其中 `doc-019` 用于验证多文档融合，`doc-020` 用于验证无答案提示和空 citations。

## 8. 测试数据和测试计划

### 8.1 已采集文本测试数据

以下文本来自当前已采集并完成向量化的飞书、微信消息，可作为阶段 8 问答和混合检索的真实联调样本。时间统一记录为 UTC；前端展示时按用户时区转换。

数据存储边界：检索测试必须查询 Elasticsearch 索引 `info-agent-chunks-v1`。该索引的 `embedding` 字段是 1536 维、已建立 cosine kNN 索引的 `dense_vector`，同时保存用于 BM25 的 `content`。表格中的“文档 ID”只是 PG `vector_store.documents.id` 与 ES `document_id` 的关联键：PG 保存权威结构化数据、权限和来源详情，不作为向量召回数据源。验证向量召回时应以 ES 中对应 `document_id` 的 chunk 数和 `embedding` 字段为准，再回 PG 查询发送者、会话、文件等详情。

| 数据 ID | 平台 | 消息标识 | 测试文本 | 文档 ID | 状态 |
|---|---|---|---|---:|---|
| msg-001 | 飞书 | `om_x100b667e1f2c70a0c4cc2798f0037c8` | 青云飞鹏官网的首屏还需要调整，整体需要再高级一些 | 134 | completed |
| msg-002 | 飞书 | `om_x100b667e1dc138a0c286eaae2c084a5` | 数据库配置采用内网连接 | 135 | completed |
| msg-003 | 飞书 | `om_x100b667e1122fca0c2b5cbc76204f35` | 李老板说明天下午 3 点开会 | 136 | completed |
| msg-004 | 飞书 | `om_x100b667e2e11c4a4b1a2e0de0bc60b4` | 青云项目的首页还需要整改，并且智能问答出现 bug 了 | 137 | completed |
| msg-005 | 飞书 | `om_x100b667e2fd580a0dfa4f3ee48f9be9` | 明天我们汇报一下官网的效果 | 138 | completed |
| msg-006 | 飞书 | `om_x100b667e2df730a4b3bbcbd02bbb1c6` | 完成 agent 开发，DDL 是今天十二点 | 139 | completed |
| msg-007 | 微信群聊 | `56115118262@chatroom:2754` | 前后端都拉一下代码 | 87 | completed |
| msg-008 | 微信 | `wxid_v0o8tmxy91ea22:8281` | JWT → Core 校验用户、平台绑定、监听白名单 → BM25/kNN → RRF → 返回消息和附件详情 | 113 | completed |

上述消息均已写入 PostgreSQL 文档表并同步写入 Elasticsearch；向量检索使用 ES，PG 仅用于结构化回填，每条短文本通常对应 1 个 chunk。配置文件类消息曾被采集，但包含数据库密码、API Key 和服务凭据，不纳入测试数据表；如需验证敏感信息过滤，应使用脱敏后的固定占位符。

推荐联调问题：

```text
李老板什么时候开会？
青云飞鹏官网的首页需要怎么调整？
智能问答出现了什么问题？
明天要汇报什么内容？
谁说过“前后端都拉一下代码”？
agent 开发的截止时间是什么？
JWT、BM25、kNN 和 RRF 的处理流程是什么？
```

- BM25 单路召回。
- kNN 单路召回。
- BM25 + kNN 的 RRF 融合。
- 多查询结果去重。
- Rerank 成功重排及失败回退。
- embedding 服务失败时的降级行为。
- 未监听会话、跨用户数据不可召回。
- 已删除、`processing`、`failed` 文档不可进入上下文。
- 同一文档多个 chunk 不重复占满结果。
- 引用能够回到 PG 中的文件、消息和会话。
- 固定评测集达到 `Recall@10 >= 90%`。

## 9. 后续实施顺序

```text
1. 冻结 SearchScope、SearchResult 和权限过滤契约
2. 补充 ES mapping 和文档状态/来源字段
3. 创建或迁移 chunk 索引，并验证向量和字段写入
4. 实现 BM25、kNN、RRF、去重和统一检索内核
5. 实现查询改写和可选 Rerank，增加失败回退
6. 接入 POST /api/qa/ask
7. 建立离线评测集和 Recall@K 基线
8. 按评测结果持续调优，直至 Recall@10 达到 90%
```

阶段 7 的顶部全局搜索可以复用权限过滤、BM25 和结果回填能力，但文件名和群聊名属于对象搜索，不能依赖问答用的向量召回替代。

## 10. 前后端联调验收

最终目标是前端与后端联调成功，而不仅是 RAG 单元测试通过。联调必须验证完整链路：

```text
前端输入问题
  -> Core 校验 JWT 和监听范围
  -> Core 调用 RAG 内部问答接口
  -> RAG 执行 BM25 + kNN + RRF + Rerank
  -> Core/PG 回填来源身份
  -> Core 转发 SSE
  -> 前端逐段显示回答和 citations
  -> 点击 citation 打开消息/会话/文件详情
```

联调验收标准：

- 前端能够发送 `POST /api/qa/ask` 并正确处理 `meta`、`delta`、`citation`、`done` 事件。
- 回答中的消息来源显示平台、发送者、群聊/私聊类型、会话名称和发送时间。
- 回答中的文档来源显示文件名、文档摘要、命中文档片段及其聊天来源。
- 点击引用能够进入对应的消息详情、会话上下文或文件预览/下载页面。
- 未登录、未绑定平台、未监听会话、已删除内容和未完成文档均不能出现在回答或引用中。
- 无答案时前端显示固定提示，并展示空引用状态。
- Rerank 或 embedding 服务异常时，前端仍能收到明确的降级或错误事件，连接不会无提示挂起。
- 同一请求的 SSE 流以 `done` 事件结束，前端能够解除 loading 状态并允许发起下一轮问题。

## 11. 假设和边界

- 主要指标为 `Recall@10 >= 90%`，同时记录 Recall@5 和 Recall@20。
- 以召回率优先，允许查询改写和 Rerank 带来的额外延迟与模型成本。
- 初始继续使用单个 ES chunk 索引，暂不拆分独立资源索引。
- Rerank 默认启用且可降级为 RRF 排序，不改变权限边界。
- 用户已采集的信息、已绑定的平台账号和正在监听的会话是唯一权限边界。
- 不将文件二进制、永久 MinIO 地址或未经授权的全库数据写入或返回给检索服务。
- 问答接口使用 SSE 流式响应，不持久化问答历史。
- 来源身份信息由 PG 回填，前端不负责推断或拼接权限字段。
- 前后端联调成功是最终交付标准，需使用真实 API、真实采集数据和真实引用完成验收。

## 12. 已确认的实施决策

为避免后续实现因接口或基础设施选择停滞，以下决策固定作为第一版实施边界：

- 对外接口由 Core 提供 `POST /api/qa/ask`；RAG 提供仅供 Core 调用的内部问答接口，前端不得直接访问 RAG。
- Core 从 JWT、已绑定平台账号和正在监听的会话计算 `SearchScope`，通过内部鉴权传给 RAG；前端传入的平台或会话只能缩小范围。
- 问答模型使用阿里云 DashScope OpenAI-compatible 接口，默认模型为 `qwen-plus`。
- Rerank 使用 DashScope 原生 Rerank 接口，模型固定为 `gte-rerank-v2`；Rerank 失败回退到 RRF，不阻断回答。
- 问答采用 SSE 流式响应，事件顺序为 `meta`、一个或多个 `delta`/`citation`、最后 `done`；不持久化问答历史。
- 第一版继续使用单个 chunk 索引；新增字段时创建 `info-agent-chunks-v2` 并通过 alias 切换，不直接破坏 v1 数据。
- 混合检索默认参数固定为：BM25 Top 100、kNN Top 100、RRF `k=60`、关键词/向量权重 `0.5/0.5`、Rerank 候选 Top 50、最终上下文 Top 8、查询改写最多 3 个。
- 没有足够证据时只返回固定提示“当前采集的信息中没有找到足够依据。”并返回空 `citations`，禁止模型使用权限范围外或未召回的知识补答。
- 配置只放在 RAG 服务环境变量中：`QA_*`、`RERANK_*`、`RAG_HYBRID_*`；真实密钥只写入本地 `services/rag/.env`，不进入前端或 Git。
- 以离线标注集的 `Recall@10 >= 90%` 作为召回验收门槛；在评测集建立前只报告基线，不宣称达到目标。

## 13. 目标模式自动推进任务

目标模式必须按以下顺序自动推进，前一项完成并通过验证后再进入下一项。实现过程中不因已确定的参数再次请求确认；只有真实密钥缺失、数据库/ES 不可连接或权限范围无法从后端计算时，才报告阻塞。

### 13.1 第一项：RAG 检索核心

在 `services/rag` 中实现可独立测试的检索内核：

- 统一 `SearchScope` 和 `SearchResult` 类型。
- 实现 BM25、kNN、加权 RRF、chunk/document 去重和统一过滤器。
- BM25 与 kNN 均使用 `user_id`、绑定账号、监听会话、`is_deleted=false` 和完成状态过滤。
- 默认 BM25/kNN 每路 Top 100，RRF `k=60`，权重 `0.5/0.5`，融合后保留 Top 50。
- 为 Elasticsearch 客户端提供可替换的测试实现，先通过单元测试，再使用当前真实索引验证。

完成判据：BM25、kNN、RRF、去重和越权过滤测试全部通过，且当前 ES 中的真实 chunk 能返回非空结果。

### 13.2 第二项：ES v2 mapping 和数据一致性

在不破坏 v1 的前提下创建或迁移 `info-agent-chunks-v2`，并通过 alias 切换。至少补齐：

```text
document_status
attachment_id
file_name
document_title
source_position
```

同时修改消息和附件向量化写入，使 PG 文档状态、ES 文档状态和来源字段保持一致；对已有 completed 文档执行重建，并核对 PG/ES chunk 数量。

完成判据：v2 mapping 可创建，历史 completed 文档可重建，查询可以过滤 processing/failed/deleted 数据，三个真实测试文档均可按 `attachment_id` 找到 ES chunk。

### 13.3 第三项：Core 权限 scope

由 Core 根据 JWT 计算真实权限范围：

```text
JWT user_id
  -> 已绑定且启用的平台账号
  -> 正在监听的会话白名单
  -> SearchScope
```

前端传入的 `platforms` 和 `conversation_ids` 只能缩小范围；Core 不信任前端 `user_id`。Core 到 RAG 使用内部鉴权，RAG 拒绝缺少或伪造 scope 的请求。

完成判据：同一检索请求无法访问其他用户、未绑定平台、未监听会话、已删除内容和未完成文档；权限过滤在 BM25 和 kNN 两路均生效。

### 13.4 第四项：RAG 内部问答接口

实现供 Core 调用的内部问答接口，并为 Core 对外的 `POST /api/qa/ask` 提供稳定的请求/响应契约。内部接口负责检索结果、引用和检索统计，不直接暴露给前端。

接口必须支持：

- `question`、缩小后的 `platforms` 和 `conversation_ids`、`top_k=8`。
- 查询改写最多 3 个问题。
- RRF Top 50 进入 Rerank，最终 Top 8 进入上下文。
- 引用包含 `document_id`、`attachment_id`、`message_id`、`conversation_id`、来源位置和 PG 回填的来源身份。
- 没有足够依据时返回固定提示和空 `citations`。
- RAG 异常以结构化错误或降级结果返回，不能静默挂起。

完成判据：Core 能携带 JWT 调用内部接口，RAG 返回真实检索结果和完整引用，非法 scope 被拒绝，接口错误和无答案行为可测试。

### 13.5 已确定配置和评测资产

第五项模型配置不再等待确认，直接从本地 RAG 环境读取：

```text
services/rag/.env
```

使用其中的 `QA_*`、`RERANK_*`、`EMBEDDING_*` 和检索参数；真实密钥不得复制到代码、前端、日志或 Git。Rerank 默认使用 DashScope `gte-rerank-v2`，问答默认使用 DashScope `qwen-plus`。

评测集直接使用本文件第 7.1、8.1 节已经定义的三个真实测试文档和问题，不另造一套数据。文档解析和向量化已完成后，目标模式应自动生成实际 `document_id`、`attachment_id`、`chunk_id` 对照表并运行 Recall@5/10/20、MRR、nDCG 和权限误召回率评测。

### 13.6 自动推进停止条件

目标模式只有在以下情况才停止并报告阻塞：

- `QA_API_KEY` 或 `RERANK_API_KEY` 缺失，且无法执行真实模型调用。
- PostgreSQL、Elasticsearch 或内部服务连续不可用。
- 无法从 JWT/PG 得到当前用户的绑定账号和监听会话范围。
- 真实评测集缺少相关性标注，无法宣称 `Recall@10 >= 90%`。

代码缺陷、测试失败、mapping 不一致、端口冲突或服务未启动不属于最终阻塞；目标模式必须先自行检查配置、启动服务、修复代码并重试。
