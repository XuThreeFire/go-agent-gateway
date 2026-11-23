# go-agent-gateway

通过一个可运行的 Go + Vue 项目，学习 **Agent 工具调用、多轮上下文、SSE 流式输出，以及执行取消、重试和事件补发**。

项目起于个人学习Agent过程中的想法，由作者与 AI 辅助设计，初版代码由 AI 编写。开源版面向有基本编程经验、尚未开发过 Agent 的读者；所有演示数据均为虚构数据。采用 [MIT 许可证](LICENSE)。

## 先体验：两条路线

### 路线 A：只装 Go，理解最小 Agent

建议 Go 1.26.1；仓库声明的语言版本为 Go 1.22。下面示例无需数据库、容器或模型密钥：

```bash
git clone git@github.com:XuThreeFire/go-agent-gateway.git
cd go-agent-gateway
make agent-demo
```

预期看到一次 `calc` 工具调用，然后输出包含 `60` 的模拟回答。Windows 建议使用 WSL；没有 Make 时直接执行 `go run ./backend/cmd/agent-demo '计算 (12+3)*4'`。

### 路线 B：完整 Web 网关

准备 Docker Desktop（包含 Compose v2.24+）并启动引擎：

```bash
cp .env.example .env
docker compose up -d --build
```

首次会下载镜像和依赖。打开 <http://127.0.0.1:8080>，本机教程访问账号为 **gateway**，密码为 **gateway-local-password**。这些公开的示例值仅适用于回环监听的本机环境。

输入 `计算 (12+3)*4` 或 `现在的时间`，打开右上角“事件流”，观察模型、工具与终态事件。默认 `demo` 是确定性的模拟模型，不访问供应商、不产生模型费用。真实模型的选择工具行为可能不同。

```bash
docker compose logs -f backend  # 查看日志
docker compose down            # 停止，保留学习数据
```

不要将整个本地目录压缩上传；安全源码包通过 `make release` 生成。不要将示例密码直接用于公网。

## 学习路线

| 顺序 | 教程 | 你会完成什么 |
| --- | --- | --- |
| 1 | [从零启动](handbook/tutorial/01-start.md) | 独立运行，理解目录与两种开发方式 |
| 2 | [最小 Agent 闭环](handbook/tutorial/02-agent-loop.md) | 理解模型、工具声明和结果回灌 |
| 3 | [接入真实模型](handbook/tutorial/03-models.md) | 配置自己的供应商与模型 |
| 4 | [流式输出与 SSE](handbook/tutorial/04-streaming.md) | 阅读事件，理解两个独立的流 |
| 5 | [工具调用](handbook/tutorial/05-tools.md) | 看懂参数校验与错误反馈 |
| 6 | [多轮对话](handbook/tutorial/06-memory.md) | 理解历史选择与上下文预算 |
| 7 | [取消、重试和恢复](handbook/tutorial/07-lifecycle.md) | 分离连接与执行生命周期 |
| 8 | [可靠性与成本](handbook/tutorial/08-reliability.md) | 执行测试，理解系统保障的边界 |

继续阅读[二次开发指南](handbook/development/README.md)、[架构与数据模型](handbook/reference/architecture.md)、[HTTP/SSE 契约](handbook/reference/protocol.md)和[个人部署指南](handbook/deployment.md)。

## 当前范围

- 单进程、单实例 Go 网关；MySQL 保存状态、步骤、用量与事件。
- Vue 浏览器客户端，以及独立 HTTP/SSE CLI。
- 默认启用 `now`、`calc` 两个只读工具；提供课程查询扩展参考实现。
- 模拟模型与 Chat Completions 兼容接口；其他协议需要单独适配。
- 签名 Cookie 隔离浏览器身份；个人公网部署还需访问密码和 HTTPS。

当前的日配额和单 Run token 预算是按已收到的供应商用量检查的软限制，不能保证精确账单上限。供应商不返回 usage 时不会伪造用量；强制杀进程可能丢失尚未持久化的事件尾批。详细边界见可靠性教程。

## 开发与验证

原生开发需要 Go、Node 24 LTS、Python 3，以及独立 MySQL 8.4。完整步骤见[启动教程](handbook/tutorial/01-start.md)。

```bash
make doctor
make tools                # 将固定版本工具安装到项目 bin/
make web-install
make test vet web-test web-build
make privacy-check
```

运行 `make test-container` 可自动创建、迁移、验证并清理独立的 MySQL 测试环境；需要 Docker 引擎。原生测试库必须以 `_test` 结尾，详见[贡献指南](CONTRIBUTING.md)。CI 会运行迁移、MySQL 集成测试与容器启动检查。

公开文档均在 `handbook/`。本机 `docs/`、`.local-docs/`、`data/`、`.env`、`dist/` 和编辑器状态不属于开源交付物。
