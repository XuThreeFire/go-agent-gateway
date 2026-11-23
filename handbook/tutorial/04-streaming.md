# 04 流式输出与两个独立的流

目标：区分供应商流与网关事件流，并理解断线补发。前置：完成一次完整对话。

供应商流由 `llm.Client.Stream` 读取，内容是文本片段、工具参数片段和 usage。网关流由 `GET /api/runs/{id}/events` 提供，内容是项目自己的业务事件。两者协议格式相似，但事件身份、持久化和生命周期不同。

## 观察一次执行

打开页面的“事件流”，发送 `计算 (12+3)*4`。应依次看见模型步骤、工具结果、第二个模型步骤、正文增量和终态。`turn.delta` 的一帧示例：

```text
id: 12:8
event: turn.delta
data: {"delta":"结果"}

```

`12:8` 是 `runId:seq`，不包含用户身份。断线后浏览器原生 `EventSource` 会回传 `Last-Event-ID`。服务端先建立实时订阅，再补发历史和未落库批次，按序号去重。

相关代码：后端 `api/sse.go`、`hub/hub.go`；前端 `api/events.ts`。原生 EventSource 使用 Cookie，所以无需将会话凭据拼进 URL。

## 使用 CLI 验证

```bash
make build-cli
# 完整 Compose 环境需要此访问密码；原生未配置访问密码时不用设置。
export GATEWAY_ACCESS_PASSWORD='gateway-local-password'
bin/cli new '计算 2+3'
bin/cli list
```

记录命令显示的 runId，用 `bin/cli tail <runId>` 回放。`bin/cli -after 5 tail <runId>` 只接收第 5 条之后的事件。CLI 身份文件默认在 `data/cli-session`，与浏览器身份独立，不能用 CLI 读取浏览器创建的会话。

## 断线实验

浏览器发送较长问题后刷新，重新打开同一会话，验证正文来自历史回放。停止订阅不会取消服务端执行；要停止执行需使用取消接口。

常见问题：反向代理缓冲会使文字最后一次性出现；网关与 Nginx 配置已经关闭 SSE 缓冲。终态必须主动关闭 EventSource，否则浏览器会重连。练习：找出心跳帧与业务事件的差别，说明为什么心跳不需要落库。
