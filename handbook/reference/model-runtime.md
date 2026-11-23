# 提示词、请求稳定性与出站调试

## 提示词来源与热重载

默认配置使用 `backend/configs/prompts/system.md`，通过 `prompt.system_file` 指定。相对路径按 YAML 配置文件所在目录解析，不按命令启动目录解析。

```yaml
prompt:
  system_file: "../prompts/system.md"
```

提示词文件优先于 `executor.system_prompt`。若要沿用旧版内联配置，将 `prompt.system_file` 留空。配置了文件但文件缺失或内容为空时启动失败；运行中重载失败保留上一次成功内容。

完整 Compose 环境挂载整个提示词目录，编辑文件后运行：

```bash
make reload-prompt
```

该命令只向当前 Compose 后端发 SIGHUP。服务不会重启，正在执行的 Run 使用开始时取得的提示词快照，新 Run 使用新内容。原生运行时向后端可执行进程发送 SIGHUP；不要把信号发给 `go run` 的包装进程。systemd 部署可用：

```bash
sudo systemctl kill -s HUP go-agent-gateway
```

原生部署还需复制提示词目录；默认生产配置将路径解析到 `/opt/go-agent-gateway/backend/configs/prompts/system.md`。

## 稳定请求与缓存观测

LLM 请求走 `buildRequestV3`：工具按名称排序，JSON Schema 对象键规范化，空工具参数统一为 `{}`，消息正文和消息顺序保持不变。JSON 数字使用 `json.Number`，保留精度和原始数字写法；不能把它理解为对所有语义相同数字做统一改写。

旧的 `buildRequest` 仅留作协议回归测试对照，不承担实际发送入口。规范化只能减少本地非必要变化，是否命中缓存仍取决于供应商的模型、前缀长度、保留时间与服务策略。

模型步骤日志包含 `cachedTokens`、`cacheHitRate` 和 `prefixFp`。缓存 token 同时识别 DeepSeek 的 `prompt_cache_hit_tokens` 与 OpenAI 的 `prompt_tokens_details.cached_tokens`。供应商未上报时是 0，不能据此认定缓存未命中。

指纹只覆盖模型名称、规范化工具声明和开头的 system 消息，不包含用户对话，也不代表供应商真实的 token 序列。缓存命中 token 仍包含在输入 token 总量中，不会从配额中再次扣减。缓存观测仅进入内部日志，不改变现有 HTTP/SSE 字段。

## 只影响 LLM 的代理

原生开发可执行：

```bash
make run ARGS='-llm-proxy off'
make run-proxy
LLM_PROXY=http://127.0.0.1:9090 make run-proxy
```

三态含义：未配置按 `HTTPS_PROXY`/`NO_PROXY` 等环境变量决定；`off`、`none`、`direct` 强制直连；显式 URL 只用于 LLM 出站，支持 HTTP、HTTPS 和 SOCKS5。代理日志只显示协议、主机与端口，不显示认证信息、路径或查询参数。

默认仍验证 TLS 证书。优先给自己的抓包工具配置受信任证书；确需临时跳过证书验证时，仅本地使用 `make run-proxy INSECURE=1`，`INSECURE=0` 不会开启此选项。生产模式禁止显式调试代理和跳过 TLS，即使只传 `-llm-proxy-insecure` 也拒绝启动；显式直连不受此限制。

模拟模型不访问网络。若要观察代理流量，需要自行启用真实模型并提供自己的密钥；抓包文件、环境文件和原始对话均不应提交。

## 输出上限字段

服务端继续保留每次模型调用默认 2048 token 的输出上限。`provider: openai` 发送 `max_completion_tokens`，其他兼容接口发送 `max_tokens`，不会同时发送两种参数。接入第三方兼容网关时按其实际支持的字段选择 provider 标签并验证，不把标签变化当成新协议实现。
