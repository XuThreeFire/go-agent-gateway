# 06 多轮对话与上下文

目标：理解“保存历史”和“给模型上下文”是两件事。前置：至少完成两轮对话。

数据库保存 Task、Turn、Run、Step 和 Event；模型不会主动读取数据库。`backend/internal/store/conversation.go` 按当前轮次加载用户输入，并从历史成功 Run 的 `turn.delta` 事件重建助手文本，再由执行器传给模型。

## 实验

使用真实模型创建会话：第一轮输入“我正在学习 Go，请记住”，第二轮问“我在学习什么”。查看请求链路，第二轮应包含历史用户消息和成功助手回答。模拟模型只回显，不具备真实语言记忆能力。

历史失败或中断的 Run 不作为成功助手回答加入上下文。工具声明与工具结果在当前 Run 内参与消息回灌，跨轮次目前只保留用户与助手正文。这是简化设计，开发长期任务时需要重新评估。

## 上下文保护

默认通过 `prompt.system_file` 指定系统提示词文件；未指定文件时回落到 `executor.system_prompt`。每次 Run 开始读取一次提示词；`max_context_bytes` 限制序列化消息大小；`max_output_tokens` 限制每次模型输出；`max_steps` 限制模型调用次数。

字节数不是 token 数，模型配置的 `context_window` 当前用于能力展示，不能当成精确 tokenizer 校验。超长会话会明确失败，读者可以新建会话；当前没有静默截断或自动摘要，避免在不知情时改变对话含义。

```bash
go test ./backend/internal/exec -v
```

练习：把本地配置的 `max_context_bytes` 调小后重启，在长会话中观察失败。再设计一个“按完整轮次裁剪历史”的方案，说明应保留哪些 system/assistant/tool 消息。不要直接从消息数组中随意删除单条工具消息，否则会破坏调用配对。

练习：在一个多步骤 Run 执行期间重载提示词，确认它仍使用旧快照，而下一个 Run 使用新内容。操作见[模型运行时参考](../reference/model-runtime.md)。
