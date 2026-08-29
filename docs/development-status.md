# 信息采集与 Core 开发状态

> 本文用于把当前实现状态和后续开发边界交接给新的开发任务。范围只覆盖飞书/微信信息采集、统一消息模型、本地暂存和 Go Core 接入；不把向量化、RAG 检索和正式用户系统误认为已完成。

## 1. 当前项目结构

```text
apps/web/                       Vue 前端（当前不是采集链路的主入口）
services/collectors/            Python 统一模型、本地存储
services/collectors/feishu/     Go 飞书采集器（独立 Go module）
scripts/collect.py              微信/fixture 采集入口
scripts/normalize_raw.py        原始 JSONL -> 统一消息 JSONL
services/core/                  Go + Gin Core 服务
services/core/internal/database PostgreSQL 连接池
services/rag/                   Python RAG 服务（后续处理/向量化）
data/                           当前开发环境的本地采集结果，不作为生产存储
```

## 2. 已完成内容

### 2.1 统一消息模型

文件：`services/collectors/model.py`

已定义：

- `CollectionEvent`：保存来源平台、采集账号、原始消息 ID、采集时间和原始 payload。
- `CanonicalMessage`：统一消息 ID、平台、采集账号、会话、发送人、时间、类型、正文、附件引用、状态、元数据和原始数据引用。
- `normalize_event()`：已支持飞书和微信样例的基本规范化。
- 飞书文本会解析 `body.content` 中的 JSON 文本。
- 微信消息会处理部分 `wxid:正文` 前缀。
- 内部消息 ID 基于 `source_account_id + source_message_id` 稳定生成，便于去重。

当前统一模型只做采集后的结构规范化，尚未做 OCR、附件解析、脱敏、切片或 Embedding。

### 2.2 本地采集存储

文件：`services/collectors/storage.py`

`LocalStore` 会创建并使用：

```text
raw/                         原始采集事件 JSONL
normalized/messages.jsonl   统一消息 JSONL
checkpoints/                 会话断点 JSON
outbox/pending-events.jsonl  后续发送到 Core 的待发送事件
attachments/                 预留附件目录
logs/                        预留日志目录
seen.json                    source_account_id + source_message_id 去重集合
```

重复执行时，相同来源账号和来源消息 ID 不会重复写入。原始 payload 会保留，统一消息通过 `raw_payload_ref` 指向原始 JSONL 行。

### 2.3 微信采集

入口：`scripts/collect.py --source wechat`

已支持：

- `--once` 单次执行。
- `--watch --interval N` 持续轮询。
- `--since` 历史起始时间。
- `--account`、`--db-dir` 指定采集账号和微信数据库目录。
- 首次运行默认采集最近 24 小时；有 checkpoint 时继续增量采集。
- 每轮重新读取会话列表，因此可发现当前授权账号可访问的新群聊。
- 使用现有 `wechatauto` 读取本机已登录微信数据，不使用 OAuth，也不要求填写授权 URL。

已用 fixture 和本机微信数据验证过，能够生成 raw、normalized、checkpoint 和 outbox 文件。运行微信采集前必须确保 Python 环境中可导入 `wechatauto`。

### 2.4 飞书采集

入口：`services/collectors/feishu/main.go`

已支持：

- 使用 OAuth 取得的 user access token，支持 `--credential-file` 读取凭据。
- `--once`、`--watch`、`--interval`、`--page-size`、`--account`。
- 获取可访问群聊并分页获取群消息。
- 使用 `seen-feishu.json` 去重。
- 原始事件写入 `raw/feishu/<source_account_id>/<date>.jsonl`。
- 凭据文件中的 access token/refresh token 不写入消息事件。

实际 OAuth 凭据已成功采集过 53 条飞书原始消息，重复执行不会追加重复消息。

注意：飞书采集器当前主要负责抓取和原始落盘；规范化需要再运行：

```powershell
python scripts/normalize_raw.py --data-dir data/collector-feishu --source feishu
```

下一步应把这两个步骤编排成稳定流水线，并处理飞书群列表分页、token 刷新和更细的 checkpoint。

### 2.5 Go Core 与 PostgreSQL 连接池

文件：

- `services/core/cmd/server/main.go`
- `services/core/internal/database/postgres.go`
- `services/core/internal/httpapi/router.go`

已完成：

- 从 `CORE_DATABASE_URL` 初始化 `pgxpool.Pool`。
- 连接池参数：最大连接 10、最小连接 1、最大生命周期 1 小时、最大空闲时间 15 分钟。
- 启动时 ping 数据库，失败则退出。
- `/health` 会检查数据库连接，返回 `database: ok` 或降级状态。
- `/api/info` 保持可用。

已验证：Core 在正确加载 `services/core/.env` 后启动成功，`/health` 返回数据库正常，`/api/info` 返回 ready。

直接执行 `go run ./cmd/server` 不会自动加载 `.env`；开发时应从仓库根目录运行 `scripts/start-dev.ps1`，或在当前 PowerShell 会话手动设置 `CORE_DATABASE_URL`。不要把密码、access token 写入代码或提交到 Git。

## 3. 当前尚未完成的关键链路

当前链路是：

```text
飞书/微信
  -> 采集器
  -> raw JSONL
  -> normalize_raw.py
  -> normalized/messages.jsonl
  -> outbox JSONL
```

以下部分尚未完成：

1. Core 没有接收统一消息事件的 HTTP API。
2. Core 没有把 `source_accounts`、`conversations`、`raw_messages`、`collector_checkpoints` 写入 PostgreSQL 的 repository/service。
3. outbox 还没有可靠发送器、重试、失败转移和确认机制。
4. 采集脚本与 Core 尚未形成统一的账号注册和凭据引用流程。
5. 附件尚未下载、哈希去重或写入对象存储；当前只有字段和目录预留。
6. 飞书 OAuth token 自动刷新、失效后的统一 401 处理尚未接入采集调度。
7. RAG 服务尚未接入采集数据处理、切片和 Embedding 任务。
8. 数据库迁移脚本、初始化数据和自动化集成测试仍需补齐。

## 4. 推荐后续开发顺序

### 阶段 A：把数据库 schema 固化

在 `db/migrations/` 保存第一版 PostgreSQL migration，目标 schema 为 `ingestion`，至少包含：

- `source_accounts`：平台账号、外部账号标识、凭据引用、状态和时间。
- `conversations`：来源账号下的会话/群聊。
- `raw_messages`：原始 payload、来源消息 ID、采集时间和去重约束。
- `collector_checkpoints`：账号 + 会话维度的 cursor/page token、最后消息时间和消息 ID。

迁移必须包含 schema、表、字段注释、表注释、唯一约束和查询索引。不要把真实 token 或数据库密码写入 migration/seed。

### 阶段 B：Core 接入统一事件

新增建议接口：

```text
POST /api/ingestion/events
```

请求体使用 `CanonicalMessage` 或带原始引用的采集事件。服务端需要：

- 校验必填字段和来源账号。
- 以 `source_account_id + source_message_id` 幂等写入。
- 事务内 upsert 会话、保存 raw message、更新 checkpoint。
- 返回新建/已存在结果，不能返回任何 token。
- 统一返回 400（参数错误）、401（账号/凭据失效）、502（上游或发送失败）、500（内部数据库错误）。

先实现 repository 单元测试，再实现 HTTP handler 集成测试。

### 阶段 C：采集器 sender 与 outbox

为 Python 采集器定义 `Sender` 接口：

```text
send(event) -> accepted / retryable error / permanent error
```

本地模式继续写 outbox；Core 模式增加 HTTP sender。发送成功后记录确认，临时错误指数退避重试，永久错误写入 dead-letter 文件。采集和发送解耦，避免 Core 短暂不可用导致消息丢失。

### 阶段 D：增量和账号管理完善

- 飞书群列表和消息分页都使用独立 checkpoint。
- 处理 OAuth access token 过期和 refresh token 更新。
- 新群聊在账号可访问范围内自动创建 conversation/checkpoint。
- 微信继续以本机已登录账号和数据库状态为准，明确账号变更检测。
- 统一日志、指标和错误分类。

### 阶段 E：附件处理

附件不要直接把二进制塞进消息表。建议流程：

```text
采集附件元数据
  -> 下载到临时目录
  -> SHA-256 哈希和大小校验
  -> 保存对象存储/本地 attachments
  -> raw_messages.attachments 保存引用、类型、名称、哈希
```

安装包等二进制文件只保留元数据和文件引用；文本文件后续再提取文本；图片后续交给 OCR；下载失败必须保留失败状态，不伪造附件内容。

### 阶段 F：Python 信息处理与向量化

Core 数据稳定后再接 RAG/处理服务：

```text
CanonicalMessage
  -> 清洗正文和附件文本
  -> 按语义/长度切片
  -> 提取元数据
  -> 生成 Embedding
  -> 写入向量索引
```

本阶段不改变采集原文，处理结果应通过 message ID 和版本号关联，方便重跑和模型升级。

## 5. 当前验证命令

在 `D:\agentworkspace\info-agent\info-agent` 执行：

```powershell
# Python 语法检查
python -m compileall services scripts

# fixture 采集与幂等验证
$env:PYTHONPATH = (Get-Location).Path
python scripts/collect.py --source fixture --fixture scripts/fixtures/messages.json --account fixture_001 --once --data-dir data/collector-test
python scripts/collect.py --source fixture --fixture scripts/fixtures/messages.json --account fixture_001 --once --data-dir data/collector-test

# 飞书原始消息规范化
python scripts/normalize_raw.py --data-dir data/collector-feishu --source feishu

# Core（推荐从仓库根目录加载 .env）
.\scripts\start-dev.ps1

# 服务启动后
Invoke-WebRequest -UseBasicParsing http://localhost:8080/health
Invoke-WebRequest -UseBasicParsing http://localhost:8080/api/info
```

预期：fixture 第一次有新增，第二次 `saved: 0`；Core `/health` 中 `database` 为 `ok`。

## 6. 交接时的开发原则

- 原始数据永久保留，规范化数据可重建，处理结果可重跑。
- 所有平台消息必须带 `source`、`source_account_id` 和 `source_message_id`。
- 以来源账号 + 来源消息 ID 做幂等边界，不能只用正文或时间去重。
- Access Token、Refresh Token 只能放凭据存储/受限配置，不能进入 raw、normalized、outbox 或 API 响应。
- 缺失字段统一为空或 `-`，不根据猜测补值。
- 先稳定采集、落库和断点，再做附件解析和向量化。
