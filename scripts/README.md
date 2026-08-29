# Scripts

## 启动开发环境

可以直接双击：

```text
scripts/start-platform.cmd
```

也可以在项目根目录执行：

```powershell
.\scripts\start-dev.ps1
```

脚本会启动四个进程：Vue、Gin Core、FastAPI RAG 和 Nginx。首次运行会自动执行
`npm ci`，并用 `uv` 将 Python 依赖安装到 `services/rag/.runtime/python`；该目录不是虚拟环境。

需要先安装：Node.js、Go、系统 Python、uv。Nginx 未安装时，其他三个服务仍会启动。
