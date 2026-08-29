# info-agent

信息管理 Agent 项目。当前先以 Web 形态开发，后续可复用 Vue 前端接入 Electron。

```text
info-agent/
├── apps/
│   └── web/                 # Vue 3 + TypeScript 前端
├── services/
│   ├── core/                # Go + Gin 核心业务服务
│   └── rag/                 # Python + FastAPI RAG 服务
├── gateway/
│   └── nginx/               # Nginx 网关配置
├── db/
│   ├── migrations/          # 数据库迁移文件
│   └── seeds/               # 初始化数据
├── docker/                  # 服务器部署用 Docker 文件
├── scripts/                 # 开发、构建、启动脚本
└── docs/                    # 项目文档
```

## 技术架构

```text
Vue Web
  ↓
Nginx 网关 :80
  ├── /api/core/* → core-service :8080
  └── /api/rag/*  → rag-service  :8000
```

### 前端

- Vue 3：页面和组件开发。
- TypeScript：类型约束。
- Vite：开发服务器和构建工具。
- 后续使用 Electron 作为桌面端壳，复用 `apps/web`。

### 后端

- `services/core`：Go + Gin，负责用户、文件、联系人、项目、权限和普通业务接口。
- `services/rag`：Python + FastAPI，负责信息导入、文本处理、Embedding、Elasticsearch 检索和 AI 问答。
- 两个服务可以独立部署，通过 Nginx 按路径转发。

### 基础设施

```text
PostgreSQL       业务数据和结构化信息
Redis            缓存、任务状态和队列
MinIO            原始文件、附件和图片
OpenFGA          用户与资源之间的权限关系
Elasticsearch    全文检索、向量检索和混合检索
```

## 本地启动

双击：`scripts/start-platform.cmd`

或在命令行执行：

```powershell
./scripts/start-dev.ps1
```

首次启动会自动恢复前端与 RAG Python 依赖。统一检索入口为
`GET /api/rag/search?q=关键词`，Nginx 会将其转发到 RAG 服务的 `/search`。
