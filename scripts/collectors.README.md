# 本地采集脚本

本轮采集脚本只保存原始事件和统一消息，不连接 PostgreSQL、Redis 或向量数据库。

## 微信

需要在本机 Python 环境安装 `wechatauto-replica` 包并在本机登录微信：

```powershell
$env:PYTHONPATH = "$PWD"
python scripts/collect.py --source wechat --once --data-dir data/collector
python scripts/collect.py --source wechat --watch --interval 60 --data-dir data/collector
```

可通过 `--account`、`--db-dir`、`--since` 覆盖默认配置。采集器按会话保存 checkpoint，并在每次轮询重新读取会话列表，因此新群聊会被发现。

## Fixture 验证

```powershell
$env:PYTHONPATH = "$PWD"
python scripts/collect.py --source fixture --fixture path/to/messages.json --account fixture_001 --once
```

输出目录包含 `raw/`、`normalized/messages.jsonl`、`checkpoints/`、`outbox/` 和 `seen.json`。重复运行不会重复写入同一 `source_account_id + source_message_id`。

## 飞书

飞书命令使用已通过 OAuth 获取的 user access token；token 不写入事件：

```powershell
$env:FEISHU_ACCESS_TOKEN = "..."
$env:FEISHU_SOURCE_ACCOUNT_ID = "conn_001"
go run ./services/collectors/feishu --watch --data-dir data/collector
```

OAuth 页面仍复用 `go-test`，正式 Core 接入前只需把 token/账号配置改为 Core 提供的连接配置。

飞书采集完成后，将原始 JSONL 规范化为统一消息模型：

```powershell
python scripts/normalize_raw.py --data-dir data/collector-feishu --source feishu
```

该命令可重复执行，只会追加尚未规范化的消息，结果写入 `normalized/messages.jsonl`。

## 并列采集入口

微信采集入口已迁移到 `services/collectors/wechat/main.py`，第一版使用本机已登录微信，
手动提供数据库目录和预先注册的 wxid：

```powershell
python services/collectors/wechat/main.py --db-dir C:\path\to\wechat-db --wxid wxid_xxx --once
```

飞书 OAuth 由 Core 提供：`GET /api/ingestion/feishu/authorize` 返回授权地址，回调固定为
`http://localhost:8080/api/ingestion/feishu/callback`。`FEISHU_APP_ID` 和
`FEISHU_APP_SECRET` 仅放在 `services/core/.env`。
