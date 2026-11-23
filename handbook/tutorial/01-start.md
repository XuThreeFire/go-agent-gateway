# 01 从零启动

目标：从新环境启动项目，完成一次能看见工具调用的对话。前置：会运行终端命令；完整容器路线不要求先懂 Go 或 Vue。

## 先运行最小示例

安装 Go 后，在仓库根目录运行：

```bash
go version
go run ./backend/cmd/agent-demo '计算 (12+3)*4'
```

预期输出 `工具调用：calc ...`，最终结果含 `60`。入口是 `backend/cmd/agent-demo/main.go`。它没有 HTTP 和数据库，便于下一章逐行学习 Agent。

## 完整容器环境

确认 `docker version` 能显示 Server，`docker compose version` 为 v2.24 及以上。

```bash
cp .env.example .env
docker compose up -d --build
docker compose ps -a
```

执行顺序是 MySQL 健康 → 版本化迁移成功 → 后端健康 → Web。迁移容器退出码 0 属于正常情况。浏览器打开 `http://127.0.0.1:8080`，输入 README 中的本机示例账号和密码，然后发送 `计算 2+3`。

验收：回答含 `5`，事件面板出现 `tool.result` 和 `run.terminal`；刷新页面后历史仍可查看。

Windows PowerShell 用 `Copy-Item .env.example .env` 代替 `cp`，其余 Compose 命令相同。需要使用 Make 和 Shell 教程时建议进入 WSL。

## 修改源码时的原生开发

推荐 Go 1.26.1、Node 24 LTS（Vite 最低要求 Node 22.12 或 20.19）、Python 3。先用容器单独提供 MySQL：

```bash
docker compose up -d mysql
make migrate
make web-install
make run
```

另开一个终端执行 `make web-dev`，访问 `http://127.0.0.1:5173`。后端默认监听 `127.0.0.1:8080`。不要同时让完整 Compose 的 Web 占用同一端口。

原生后端不会自动读取 `.env`，默认配置连接 `127.0.0.1:13306` 的本地教程库；设置过自己的数据库密码时，要显式导出对应的 `DATABASE_URL`。前端 `/api` 与 `/healthz` 由 Vite 代理到后端。如果原生开发也设置了访问密码，先在浏览器访问 `http://127.0.0.1:5173/api/session` 完成 Basic 认证，再打开首页。

如果使用自己安装的 MySQL，先创建一个独立库和账号，再运行：

```bash
make tools
export DATABASE_URL='mysql://gateway:URL_ENCODED_PASSWORD@127.0.0.1:3306/go_agent_gateway?parseTime=true&loc=UTC'
make migrate-local
make run
```

连接串中的特殊字符需要 URL 编码。以上密码是占位符，必须替换。

## 目录与排错

`backend/` 是 Go 网关，`cli/` 是独立客户端，`web/` 是 Vue 页面，`handbook/` 是公开教程，`deploy/` 是通用部署示例。

- Docker 无法连接：启动 Docker Desktop，然后执行 `make doctor`。
- 端口已占用：先停止另一套本项目进程，或修改 Compose 的宿主端口。
- 数据库拒绝连接：看 `docker compose logs mysql migrate`；修改 `.env` 不会自动修改已有数据库账号。
- 页面 401：使用本机示例账号密码，或使用自己配置的访问密码。
- 模型失败：先选 `demo` 确认工程链路正常，再按第 03 章检查真实模型。

练习：把最小示例输入改成 `现在的时间`，指出输出来自哪个工具。清理容器用 `docker compose down`；加 `-v` 会删除数据库卷，只有确定学习数据可丢弃时才使用。
