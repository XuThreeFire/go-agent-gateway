# 修改提示词、上下文和执行预算

默认编辑 `backend/configs/prompts/system.md` 调整提示词，Compose 环境运行 `make reload-prompt` 生效，无需重启。若要使用内联提示词，在本地配置中清空 `prompt.system_file`：

```yaml
prompt:
  system_file: ""
executor:
  system_prompt: "你是课程学习助手。引用工具结果，无法确认时明确说明。"
  max_steps: 6
  max_output_tokens: 1024
  max_context_bytes: 32000
  max_run_tokens: 10000
```

修改 YAML 预算或内联提示词后需要重启；文件提示词通过 SIGHUP 重载。每个 Run 的全部模型步骤使用同一份提示词快照。`max_steps` 是模型调用次数，一个模型调用可以请求多个工具。工具结果长度由 `tools.max_result_bytes` 控制，工具执行时间由 `tools.timeout` 控制。

不要只靠提示词限制危险能力：工具权限由注册与白名单决定。工具返回的文本也可能包含恶意指令，系统提示词应将工具结果视作数据；新增外部内容源时仍需考虑提示注入和业务权限。

若要裁剪历史，在 `store/conversation.go` 的上下文组装处实现策略，并测试完整轮次的保留、失败 Run 排除、当前轮用户输入保留。若要总结历史，明确何时触发总结、如何持久化以及总结错误如何恢复。

单 Run token 与日配额都是基于已知 usage 的软限制；修改预算时同步[可靠性边界](../tutorial/08-reliability.md)，不要写出“精确限制全部费用”的承诺。

练习验收：上下文过长能明确失败；工具循环在指定模型调用次数结束；更换提示词后仍能通过取消、重试和补发回归测试。

提示词优先级、缓存统计与出站代理的完整说明见[模型运行时参考](../reference/model-runtime.md)。
