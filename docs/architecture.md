# 架构说明

当前项目采用清晰的四层结构：

```text
apps/web       Vue 3 + TypeScript 前端
gateway/nginx  统一入口与路径转发
services/core  Go + Gin 核心业务服务
services/rag   Python + FastAPI RAG 服务
```

请求路径：

```text
/api/core/* -> core-service:8080
/api/rag/*  -> rag-service:8000
```

本地基础设施：PostgreSQL、Redis、MinIO、Elasticsearch、OpenFGA。

数据职责：PostgreSQL 保存业务数据，MinIO 保存原始文件，Elasticsearch 保存全文和向量索引，Redis 保存缓存与任务状态，OpenFGA 保存资源权限关系。
