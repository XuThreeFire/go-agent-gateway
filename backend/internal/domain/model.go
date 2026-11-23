// Package domain 只放结构体、常量与状态迁移判定，不依赖任何其他内部包。
// 概念见 handbook/reference/architecture.md；DDL 以 backend/migrations/ 的版本化 SQL 为准。
//
// ID 由数据库自增生成（`BIGINT UNSIGNED AUTO_INCREMENT`），这里统一用 int64 承接：
// 自增值远小于 MaxInt64，store 层负责 uint64 → int64 的显式转换，其它层不感知。
// 因此新建实体不能预先填 ID，必须 INSERT 后取 LastInsertId 回填。
package domain

import (
	"errors"
	"time"
)

// Run 状态。pending/running 是过程态，其余四个都是终态。
const (
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusAborted     = "aborted"
	StatusInterrupted = "interrupted"
)

// IsTerminal 判断是否为终态。终态之后不允许任何迁移——
// 用户「继续」或「重试」都是新建 Run，不是把旧状态改回去。
func IsTerminal(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusAborted, StatusInterrupted:
		return true
	}
	return false
}

// ErrorCategory 只用来决定「能不能重试」和「给用户看什么」，不做技术分类展示。
type ErrorCategory string

const (
	ErrRateLimit     ErrorCategory = "rate_limit"
	ErrTimeout       ErrorCategory = "timeout"
	ErrProtocol      ErrorCategory = "protocol"
	ErrToolError     ErrorCategory = "tool_error"
	ErrQuotaExceeded ErrorCategory = "quota_exceeded"
	// ErrMaxSteps 是网关自己的判断，不是上游给的：模型在 max_steps 内一直在调工具
	// 没有收敛。单列一类是因为处置动作不同——它不是故障，是「参数调小/提示词要改」，
	// 混进 protocol 会让上游错误率统计失真。
	ErrMaxSteps ErrorCategory = "max_steps"
)

// EventType 是 SSE 的 data.type 取值，客户端按它分流。
const (
	EventRunStarted  = "run.started"
	EventTurnDelta   = "turn.delta"
	EventStepStarted = "step.started"
	EventStepFinish  = "step.finished"
	EventToolResult  = "tool.result"
	EventRunTerminal = "run.terminal"
	EventHeartbeat   = "heartbeat"
)

// Step 类型。
const (
	StepModelCall = "model_call"
	StepToolCall  = "tool_call"
)

// ErrConflict 用于 CAS 失败、取消已终态等场景，API 层映射为 409。
var ErrConflict = errors.New("conflict: state changed")

type Task struct {
	ID        int64
	ClientID  string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Turn 是 Task 内的一轮对话，追加后不再修改。
type Turn struct {
	ID        int64
	TaskID    int64
	Seq       int
	UserInput string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Run 是某个 Turn 的一次执行尝试。重试产生新的 Run（attempt_no + 1），旧 Run 不改写。
// Version 供 CAS 使用：UPDATE ... WHERE id = ? AND status = ? AND version = ?
type Run struct {
	ID             int64
	TaskID         int64
	TurnID         int64
	AttemptNo      int
	IdempotencyKey *string
	ModelSnapshot  []byte // JSON 原样存，避免模型配置变更后历史无法还原
	Status         string
	ErrorCategory  *string
	ErrorMessage   *string
	InputTokens    int64
	OutputTokens   int64
	StartedAt      *time.Time
	FirstTokenAt   *time.Time // 只记第一次，TTFT 由它减 started_at
	EndedAt        *time.Time
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TTFTMS 是权威首 token 时延：first_token_at - started_at，单位毫秒。
// 任一端缺失或算出负数都返回 0，调用方配 omitempty 让字段整体不出现。
// 放在 domain 而不是 exec：终态事件由 executor 写、由 api 的补发路径重放，
// 两处必须给出同一个数，否则重连前后 TTFT 会不一致。
func (r Run) TTFTMS() int64 {
	if r.StartedAt == nil || r.FirstTokenAt == nil {
		return 0
	}
	ms := r.FirstTokenAt.Sub(*r.StartedAt).Milliseconds()
	if ms <= 0 {
		return 0
	}
	return ms
}

// Step 是一次模型调用或工具调用，用于归因：耗时、状态码、token、错误。
type Step struct {
	ID            int64
	RunID         int64
	Seq           int
	Type          string
	Provider      string
	Model         string
	HTTPStatus    *int
	LatencyMS     *int
	InputTokens   int64
	OutputTokens  int64
	ToolName      *string
	ToolInput     []byte
	ToolResult    []byte
	ErrorCategory *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Event 是对外输出流，append-only。
// Seq 在 Run 内单调递增但允许空洞（批量落库 + 崩溃会产生未落库的 seq）。
type Event struct {
	ID        int64
	RunID     int64
	Seq       int64
	Type      string
	Payload   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ModelConfig 驱动 /api/models，并在能力不匹配时用于拒绝请求。
type ModelConfig struct {
	ID               int64
	Provider         string
	BaseURL          string
	ModelName        string
	SupportsToolCall bool
	ContextWindow    int
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// UsageCounter 是配额计数的原子累加载体（UPDATE ... SET tokens = tokens + ?）。
type UsageCounter struct {
	ID        int64
	Scope     string // run / client_day
	ScopeKey  string
	Tokens    int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
