# Info-Agent 后端改造任务拆分

## 目标与原则

在保留现有 Go Core、采集器、RAG worker、PostgreSQL、Redis 和 Elasticsearch 链路的前提下，配合前端实现类似 WeKnora 的工作台体验。前端只借鉴信息架构和交互，不复制 WeKnora 私有源码或组件。

固定一级知识库只有：飞书、企业微信、个人微信。一级知识库由平台账号和现有 `conversations` 虚拟映射得到，用户不能创建新的一级知识库。

现有表继续承担以下职责：`identity.users` 保存用户和认证字段；`source_accounts` 保存平台账号归属；`conversations` 保存群聊/私聊；`messages` 保存规范化消息；`raw_messages` 保存原始 payload；`vector_store.documents/chunks` 保存解析后的正文和 chunk；ES `info-agent-chunks-v1` 保存新向量索引。Redis 保存短期状态，MinIO 保存附件二进制。

所有受保护接口都从 JWT 获取当前用户，不能信任前端传入的 `user_id`。`source_account_id`、`conversation_id`、`sender_id` 始终使用 PG 内部主键，平台 ID 使用对应 `external_*` 字段。

## 数据模型调整

### 统一消息分类

微信和飞书统一输出：`text`、`file`、`image`、`audio`、`video`、`link`、`system`、`mixed`、`unknown`，同时保留平台原始类型 `source_message_type`。文本进入 RAG，非文本至少保留类型和原始 payload。

### 新增 `ingestion.attachments` 表

用途：保存一条消息下多个附件的元数据、下载/解析生命周期、预览能力和对象存储引用，不保存二进制文件。该表是本方案明确同意的唯一新增表。

字段：

```text
id, message_id, raw_message_id, source_account_id, user_id, platform
external_attachment_id, file_name, extension, mime_type, file_category
file_size, content_hash, storage_provider, storage_bucket, storage_key
download_status, parse_status, preview_capability, is_deleted, last_error
created_at, updated_at
```

`file_category` 为 document/archive/installer/image/audio/video/unknown；`download_status` 为 not_downloaded/pending/completed/failed；`parse_status` 为 not_required/pending/processing/completed/failed；`preview_capability` 为 inline/rendered/download_only。安装包不执行，压缩包不自动解压。

`vector_store.documents` 只表示已提取文本、可以分块和向量化的文档，不与原始附件记录混用。

## 最小可执行改造阶段

### 阶段 0：冻结契约

固定内部/外部 ID、平台枚举、消息类型枚举和错误格式 `{"error":"..."}`。验收：形成接口字段表，不新增数据库对象。

### 阶段 1：邮箱认证

注册接收 `username`、`email`、`password`、`confirm_password`；邮箱唯一，密码使用 bcrypt/argon2id，不发送验证码。登录改为邮箱+密码，JWT 保护后续接口。

```text
POST /api/auth/register
POST /api/auth/login
GET  /api/auth/me
```

验收：重复邮箱拒绝；错误凭据不泄露账号是否存在；JWT 能访问受保护接口。

### 阶段 2：固定知识库

```text
GET /api/knowledge-bases
```

每个平台返回 `platform`、`display_name`、`bound`、`enabled`、`selected_conversation_count`、`last_sync_at`。未绑定平台不可读取会话，不创建知识库表。

### 阶段 3：会话层级

复用 `conversations` 和现有白名单配置：

```text
GET /api/knowledge-bases/{platform}/conversations
GET /api/knowledge-bases/{platform}/conversations/{id}
GET /api/knowledge-bases/{platform}/conversations/{id}/messages
```

支持群聊/私聊筛选、搜索、分页、监听开关和历史时间；关闭监听不删除历史数据。验收：只选一个会话时仅该会话产生新增消息、任务、chunk 和 ES 文档。

### 阶段 4：附件采集

先执行并确认 `ingestion.attachments` migration；微信和飞书解析器统一 `message_type`，从 `raw_payload` 提取附件元数据，原始 payload 继续保留。文本进入 RAG，其他类型只记录。

验收：一条含多个附件的消息产生多条附件记录；安装包/压缩包标记为 `download_only`。

### 阶段 5：消息与文件详情

```text
GET /api/messages/{id}
GET /api/messages/{id}/attachments
GET /api/attachments/{id}
GET /api/attachments/{id}/preview
GET /api/attachments/{id}/download
```

API 先查 PG 校验权限，再从 MinIO 流式返回或生成短时效 URL；不返回永久 MinIO 地址。验收：跨用户访问返回 403/404，下载失败保留状态。

### 阶段 6：文档处理预留

预留 `pdf/docx/xlsx/pptx/txt/md/csv`。后续流程为 MinIO 原文件→解析文本/Markdown→`vector_store.documents`→`chunks`→ES embedding。本阶段不实现图片 OCR、语音转写、多模态 embedding、压缩包解压或安装包解析。

### 阶段 7：全局搜索

```text
GET /api/search?q=...&platform=...&conversation_id=...
```

结果类型为 `conversation`、`message`、`attachment`。JWT→权限/监听白名单过滤→ES BM25+kNN→应用层合并→批量回 PG。命中会话进入消息页，命中消息或文件打开详情。

验收：BM25 和 kNN 均不能跨用户返回，搜索参数不能绕过绑定和监听范围。

### 阶段 8：AI 问答快速模式

```text
POST /api/qa/ask
```

请求包含 `question`、`platforms`、`conversation_ids`。只允许已绑定且已监听的会话，只使用已完成向量化的 chunk。本轮不持久化问答历史，不新增问答表。

### 阶段 9：个人中心和连接器

返回头像、昵称、邮箱、用户名、三个连接器状态、监听会话数和最近同步时间。绑定按钮只负责首次绑定；有效绑定显示配置入口，只有显式重新绑定才替换账号。

## 前端协作提示词

请基于现有 Vue+Vite 项目，保留后端认证和连接器接口，参考 WeKnora 的知识库、文档列表、搜索、问答和个人中心交互，但不要复制其源码。实现邮箱注册/登录、工作台、三个固定知识库、平台会话配置、消息详情、文件列表/预览/下载、全局消息/会话/文件搜索、限定监听会话的快速问答和个人连接器中心。使用真实后端 API，不伪造消息、文件或连接器状态；通过状态标签区分待处理、处理中、解析完成、解析失败和仅可下载。

## 测试与验收

依次验证认证、知识库权限、白名单采集、附件多记录、MinIO 下载、文档状态、ES 向量写入、BM25/kNN 用户过滤和问答范围。最后运行 Core Go tests、RAG compileall、前端 build、migration 测试和 `git diff --check`。

## 明确不做

不整体重写后端；不允许自定义一级知识库；不迁移旧 pgvector；不执行安装包；不自动解压压缩包；不做图片 OCR、语音转写和多模态向量；不持久化问答历史；不复制 WeKnora 私有前端源码；除 `ingestion.attachments` 外不新增数据库表，其他新增数据库对象必须先说明用途并确认。
