package store

import (
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// row 结构体与迁移脚本一一对应。与 domain 分开的理由：
// 表结构是持久化细节（JSON 存 string、可空列用指针），domain 是对外语义，
// 混在一起会让上层被迫知道列类型。转换只发生在这一层。
//
// 已落地 task / turn / run / step / event / usage_counter 六张；
// model_config 暂时不用：模型清单以 config.yaml 为唯一事实来源（运维改一次配置重启即可，
// 表里再存一份就会出现「配置和库不一致该信谁」的问题）。要接运营后台时再启用。

type taskRow struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ClientID       string    `gorm:"column:client_id"`
	Title          string    `gorm:"column:title"`
	IdempotencyKey *string   `gorm:"column:idempotency_key"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (taskRow) TableName() string { return "task" }

func (r taskRow) toDomain() domain.Task {
	return domain.Task{
		ID:        r.ID,
		ClientID:  r.ClientID,
		Title:     r.Title,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

type turnRow struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	TaskID    int64     `gorm:"column:task_id"`
	Seq       int       `gorm:"column:seq"`
	UserInput string    `gorm:"column:user_input"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (turnRow) TableName() string { return "turn" }

func (r turnRow) toDomain() domain.Turn {
	return domain.Turn{
		ID:        r.ID,
		TaskID:    r.TaskID,
		Seq:       r.Seq,
		UserInput: r.UserInput,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

type runRow struct {
	ID             int64      `gorm:"column:id;primaryKey;autoIncrement"`
	TaskID         int64      `gorm:"column:task_id"`
	TurnID         int64      `gorm:"column:turn_id"`
	AttemptNo      int        `gorm:"column:attempt_no"`
	IdempotencyKey *string    `gorm:"column:idempotency_key"`
	ModelSnapshot  string     `gorm:"column:model_snapshot"`
	Status         string     `gorm:"column:status"`
	ErrorCategory  *string    `gorm:"column:error_category"`
	ErrorMessage   *string    `gorm:"column:error_message"`
	InputTokens    int64      `gorm:"column:input_tokens"`
	OutputTokens   int64      `gorm:"column:output_tokens"`
	StartedAt      *time.Time `gorm:"column:started_at"`
	FirstTokenAt   *time.Time `gorm:"column:first_token_at"`
	EndedAt        *time.Time `gorm:"column:ended_at"`
	Version        int        `gorm:"column:version"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

func (runRow) TableName() string { return "run" }

func (r runRow) toDomain() domain.Run {
	run := domain.Run{
		ID:             r.ID,
		TaskID:         r.TaskID,
		TurnID:         r.TurnID,
		AttemptNo:      r.AttemptNo,
		IdempotencyKey: r.IdempotencyKey,
		Status:         r.Status,
		ErrorCategory:  r.ErrorCategory,
		ErrorMessage:   r.ErrorMessage,
		InputTokens:    r.InputTokens,
		OutputTokens:   r.OutputTokens,
		StartedAt:      r.StartedAt,
		FirstTokenAt:   r.FirstTokenAt,
		EndedAt:        r.EndedAt,
		Version:        r.Version,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
	if r.ModelSnapshot != "" {
		run.ModelSnapshot = []byte(r.ModelSnapshot)
	}
	return run
}

type stepRow struct {
	ID            int64     `gorm:"column:id;primaryKey;autoIncrement"`
	RunID         int64     `gorm:"column:run_id"`
	Seq           int       `gorm:"column:seq"`
	Type          string    `gorm:"column:type"`
	Provider      string    `gorm:"column:provider"`
	Model         string    `gorm:"column:model"`
	HTTPStatus    *int      `gorm:"column:http_status"`
	LatencyMS     *int      `gorm:"column:latency_ms"`
	InputTokens   int64     `gorm:"column:input_tokens"`
	OutputTokens  int64     `gorm:"column:output_tokens"`
	ToolName      *string   `gorm:"column:tool_name"`
	ToolInput     *string   `gorm:"column:tool_input"`
	ToolResult    *string   `gorm:"column:tool_result"`
	ErrorCategory *string   `gorm:"column:error_category"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (stepRow) TableName() string { return "step" }

func newStepRow(s domain.Step) stepRow {
	row := stepRow{
		RunID:         s.RunID,
		Seq:           s.Seq,
		Type:          s.Type,
		Provider:      s.Provider,
		Model:         s.Model,
		HTTPStatus:    s.HTTPStatus,
		LatencyMS:     s.LatencyMS,
		InputTokens:   s.InputTokens,
		OutputTokens:  s.OutputTokens,
		ToolName:      s.ToolName,
		ErrorCategory: s.ErrorCategory,
	}
	if len(s.ToolInput) > 0 {
		v := string(s.ToolInput)
		row.ToolInput = &v
	}
	if len(s.ToolResult) > 0 {
		v := string(s.ToolResult)
		row.ToolResult = &v
	}
	return row
}

type eventRow struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	RunID     int64     `gorm:"column:run_id"`
	Seq       int64     `gorm:"column:seq"`
	Type      string    `gorm:"column:type"`
	Payload   string    `gorm:"column:payload"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (eventRow) TableName() string { return "event" }

func (r eventRow) toDomain() domain.Event {
	return domain.Event{
		ID:        r.ID,
		RunID:     r.RunID,
		Seq:       r.Seq,
		Type:      r.Type,
		Payload:   []byte(r.Payload),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

// usage_counter 没有对应 domain 结构体：它纯粹是配额用的累计器，
// 上层只关心「读一个数、加一个数」，多一层领域模型没有意义。
type usageRow struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Scope     string    `gorm:"column:scope"`
	ScopeKey  string    `gorm:"column:scope_key"`
	Tokens    int64     `gorm:"column:tokens"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (usageRow) TableName() string { return "usage_counter" }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(v int) *int { return &v }
