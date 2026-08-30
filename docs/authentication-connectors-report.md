# 用户认证与连接器管理完成报告

## 修改文件

- `services/core/internal/config/config.go`：增加 JWT secret 配置。
- `services/core/internal/database/postgres.go`：启动时为现有 `users` 表补充认证及飞书资料字段。
- `services/core/internal/httpapi/router.go`：注册、登录、当前用户 API；JWT HMAC 签发与中间件；OAuth 状态绑定当前用户；飞书回调重定向及用户资料保存。
- `services/collectors/feishu/main.go`：每轮从 Redis 重新读取凭据，并保留临近过期刷新与 401 重试一次逻辑。
- Feishu collector 启动时不再依赖 `.env` 的旧账号值；会从 PostgreSQL 激活连接器中选择 Redis 有效凭据的最新外部账号。
- `services/collectors/wechat/service.py`：对损坏会话做进程级隔离，避免单个 malformed 数据库反复阻塞健康会话。
- 飞书 OAuth 回调会幂等创建/激活对应的 `ingestion.source_accounts` 记录，确保 collector 可直接按授权账号运行。
- `apps/web/src/App.vue`、`apps/web/src/style.css`：登录、注册、个人主页、飞书/微信绑定和流水线状态展示。

## 数据库变更

不新增表。Core 启动时执行幂等 `ALTER TABLE identity.users ADD COLUMN IF NOT EXISTS`，字段为 `username`（唯一）、`nickname`、`password_hash`、`feishu_open_id`、`feishu_name`、`feishu_avatar`。密码使用 bcrypt 哈希保存。

## 启动方式

保持原有 `scripts/start-dev.ps1` 或 Docker Compose 启动方式。生产环境应设置 `CORE_JWT_SECRET`，并配置 PostgreSQL、Redis 与飞书应用凭据。

## 测试结果

- `go test ./...`：通过。
- `python -m compileall -q services/collectors scripts`：通过。
- `npm run build`：通过（`vue-tsc` 与 Vite build）。
- Core 连接现有 PostgreSQL 后 `/health`：返回 `database: ok`。
- 使用真实 PostgreSQL 临时 Core 实例验证：注册返回 201、登录返回 JWT、`/api/auth/me` 返回当前用户、未带 JWT 访问 `/api/messages` 返回 401。
- Feishu collector `go test ./...` 和 WeChat checkpoint 测试通过。
- 当前 PostgreSQL 状态核验：73 条 ingestion 消息、73 条 worker_tasks、73 条 pgvector documents、305 个 checkpoints；`feishu-collector`、`wechat-collector`、`rag-vectorization` heartbeat 均为 `running`。
- 使用提供的微信目录完成真实绑定：建立绑定时间和 checkpoints，collector 已处理 6 条新消息；浏览器确认本地 Web 登录页可呈现。
- 该微信目录仍有部分会话数据库损坏；修复后的 worker 会跳过已识别损坏会话并继续处理其他会话。
- 重启微信 collector 加载隔离修复后，`/status` 返回 `running` 且 `last_error` 为空；Core heartbeat 仍为 `running`，失败计数未继续增长。
- 飞书 OAuth 成功后，`identity.users` 已确认绑定当前账号，Redis 凭据存在，collector heartbeat 更新并处理消息；collector 启动时会从激活的 `ingestion.source_accounts` 中选择带有效 Redis 凭据的最新账号，不再以 `.env` 中旧账号作为启动前置条件。
- 连接器状态按登录用户读取现有 `ingestion.source_accounts`：账户 1 的微信/飞书绑定保持不迁移；微信重复绑定同一用户、同一路径和 wxid 时直接复用，其他用户不能覆盖该绑定。
- 监听范围配置使用现有 `ingestion.collector_bindings` 扩展字段（白名单 JSONB、历史起始时间），提供按用户的会话分页/搜索/类型筛选和配置接口；空白名单暂停新增采集，两个 collector 每轮热加载配置。
- 白名单为空时 Feishu collector 仍刷新会话元数据目录，但不写消息、worker task 或 checkpoint，用户可先选择会话再开启采集。
- Web 开发端口已切换为 `5174`（避免占用 `5173`）；Core 的飞书 OAuth 成功/失败重定向同步指向 `http://localhost:5174`。
- 当前 8080 被工作区外 Java 服务占用，开发启动脚本会将 info-agent Core 临时运行在 `8082`，Web 代理同步指向 8082；8080 callback 约定保留，端口释放后可恢复。
- OAuth 失败回调实测返回 HTTP 302，Location 为 `http://localhost/login?error=feishu_oauth_failed`；未授权访问授权入口返回 HTTP 401。

## 已验证链路

代码已覆盖注册 -> bcrypt 密码校验 -> JWT 签发 -> Bearer JWT 身份注入 -> `/api/auth/me`；登录后的个人主页可读取消息、向量任务、worker 心跳并发起飞书/微信绑定。飞书 OAuth state 绑定用户，成功回调保存 Redis 凭据并更新 users 后重定向 `/profile`，失败重定向 `/login?error=feishu_oauth_failed`。现有 collector、checkpoint、worker_tasks、RAG 与 pgvector 链路未改动。

## 剩余问题

微信目录已可读取，但部分会话数据库报告 `database disk image is malformed`，worker 已隔离错误并继续处理新消息；该错误需要修复或移除损坏的本地会话文件后才能宣称微信状态完全无错误。开发环境未安装 nginx，因此使用 `http://localhost:5174` 访问 Web，OAuth callback 仍使用 Core 的 `http://localhost:8080` 地址。
