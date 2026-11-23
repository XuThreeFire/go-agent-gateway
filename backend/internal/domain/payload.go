package domain

// 事件 payload 的 schema 集中在这里。
// 事件表只有 type + payload 两列，schema 不写下来就没人知道 data 里有什么，
// 客户端也没法独立实现；改结构必须同时改这里和 docs/05。

// ModelSnapshot 存进 run.model_snapshot：配置会变，历史 Run 必须能还原当时的参数。
type ModelSnapshot struct {
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	UpstreamModel    string `json:"upstreamModel"`
	BaseURL          string `json:"baseUrl"`
	SupportsToolCall bool   `json:"supportsToolCall"`
	ContextWindow    int    `json:"contextWindow"`
}

// RunStartedPayload 对应 run.started。
type RunStartedPayload struct {
	TaskID int64  `json:"taskId"`
	TurnID int64  `json:"turnId"`
	Model  string `json:"model"`
}

// StepStartedPayload 对应 step.started。model 与 tool 互斥：
// model_call 只填 model，tool_call 只填 tool。
type StepStartedPayload struct {
	Step  int    `json:"step"`
	Type  string `json:"type"`
	Model string `json:"model,omitempty"`
	Tool  string `json:"tool,omitempty"`
}

// ToolResultPayload 对应 tool.result。成功与失败都发：客户端要能渲染出
// 「调了什么、给回什么、错在哪」，否则工具步骤在界面上就是一段空白。
type ToolResultPayload struct {
	Step       int    `json:"step"`
	Tool       string `json:"tool"`
	CallID     string `json:"callId"`
	Input      string `json:"input,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int    `json:"durationMs"`
}

// TurnDeltaPayload 对应 turn.delta，也是补发时重建回答正文的唯一来源。
type TurnDeltaPayload struct {
	Delta string `json:"delta"`
}

// StepFinishedPayload 对应 step.finished。
type StepFinishedPayload struct {
	Step         int   `json:"step"`
	LatencyMS    int   `json:"latencyMs"`
	HTTPStatus   int   `json:"httpStatus,omitempty"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// RunTerminalPayload 对应 run.terminal。客户端收到它就停止重连。
//
// TTFTMS 是权威首 token 时延（first_token_at - started_at）：终态帧自带，
// 前端不必为了显示 TTFT 再回查一次任务详情。没有首 token 的终态
// （首字前取消、连接直接失败）不填，omitempty 让字段整体不出现——
// 0 与「没测到」在语义上必须能区分。
type RunTerminalPayload struct {
	Status        string `json:"status"`
	ErrorCategory string `json:"errorCategory,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	InputTokens   int64  `json:"inputTokens"`
	OutputTokens  int64  `json:"outputTokens"`
	DurationMS    int64  `json:"durationMs"`
	TTFTMS        int64  `json:"ttftMs,omitempty"`
}
