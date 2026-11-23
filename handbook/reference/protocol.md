# HTTP 与 SSE 契约

除健康检查外，所有接口经过会话中间件。浏览器自动使用 HttpOnly Cookie；CLI 保存自己的 Cookie。个人公网环境还使用 HTTP Basic，用户名固定 `gateway`，密码来自 `GATEWAY_ACCESS_PASSWORD`。

| 方法 | 路径 | 含义 |
| --- | --- | --- |
| GET | `/healthz` | 检查数据库连通性，成功 200、不可用 503 |
| GET | `/api/session` | 建立/读取本机会话身份，前端先调用再并发加载 |
| GET | `/api/models` | 返回启用模型与默认模型，不返回密钥 |
| GET | `/api/tools` | 返回实际启用工具声明 |
| POST | `/api/tasks` | `{input, model?, idempotencyKey?}`，202 返回 taskId/runId |
| GET | `/api/tasks` | 当前身份的会话列表；支持 limit/cursor |
| GET | `/api/tasks/{taskId}` | 会话、轮次、执行尝试与用量 |
| POST | `/api/tasks/{taskId}/turns` | `{input, model?, idempotencyKey?}`，202 返回 turnId/runId |
| GET | `/api/runs/{runId}/events` | SSE 实时订阅与补发 |
| POST | `/api/runs/{runId}/cancel` | 请求取消；运行中的执行器异步完成收尾 |
| POST | `/api/runs/{runId}/retry` | 新建执行尝试；仅 failed/aborted/interrupted 可重试 |

未知资源与他人资源均返回 404。错误体为 `{error, code}`；冲突 409 额外携带当前 runId/status。code 包括 invalid_argument、unauthenticated、forbidden、not_found、conflict、rate_limited 和 internal。

## 事件

`run.started`、`step.started`、`turn.delta`、`step.finished`、`tool.result`、`run.terminal`。各事件的权威 JSON tag 在 `backend/internal/domain/payload.go`，前端对应解析在 `web/src/api/payload.ts`。

SSE id 使用 `{runId}:{seq}`。补发位置优先级：Last-Event-ID 请求头 → lastEventId 查询参数 → afterSeq 查询参数 → 0。心跳是 `: ping` 注释，不写入事件表。订阅正常终态后客户端应立即关闭连接。

同一路径加 `?format=json` 可以分页读取持久化历史事件，返回 `{items, hasMore, nextAfterSeq}`。实时与历史使用同一套 payload。JSON 回放只包含已经落库的事件。

## 幂等与预算

新建和追加支持调用方提供的幂等键，最多 64 字符；重放时返回原有执行并设置 `Idempotent-Replay: true`。同一操作重试时复用相同 key；不同输入应使用不同 key。

输入默认上限 32000 个 Unicode 字符，请求体上限 1MB。上下文、模型输出、步数和 token 软预算由后端配置控制；客户端不能通过请求参数提高这些预算。
