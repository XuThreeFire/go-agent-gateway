package domain

// Role 是对话角色。system 用于人设与规则，user/assistant 是真实轮次，
// tool 是工具执行结果——它只在一次 Run 内部存在，不跨轮次进上下文。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型发起的一次工具调用。
//
// Arguments 保留上游给的 JSON 原文，不在这里解析成结构体：参数非法也要能原样
// 回给模型看，让它自己改；提前反序列化会把「模型写错了什么」这个信息丢掉。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message 是拼上下文的最小单位。
//
// 为什么不做跨供应商的完整抽象（D-05）：reasoning_content、image_url、
// cache_control 这些字段各家互不相通，全量保留等于被第一家绑死。
// 这里只保留 OpenAI 兼容协议的公共子集：角色 + 文本 + 工具调用三件套。
//
// ToolCalls 必须带上：模型发起调用的那一轮 assistant 消息要原样回灌，
// 否则下一轮上游看到「有 tool 消息但没有对应的 tool_call」会直接 400。
type Message struct {
	Role    Role
	Content string

	// ToolCalls 仅 assistant 消息可能有：本步模型发起了哪些调用。
	ToolCalls []ToolCall
	// ToolCallID 仅 tool 消息有：这条结果是回应哪一次调用。
	ToolCallID string
	// Name 仅 tool 消息有：工具名，便于排查（部分供应商不校验）。
	Name string
}
