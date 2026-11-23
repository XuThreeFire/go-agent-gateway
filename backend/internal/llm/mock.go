package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// mockClient 以确定规则代替模型决策，但仍经过真实执行器、工具与事件链路。
// 它不访问网络；不用于评估真实模型的推理能力。
type mockClient struct {
	target Target
	delay  time.Duration
}

func (m *mockClient) Target() Target { return m.target }
func (m *mockClient) Stream(ctx context.Context, req Request, onChunk func(Chunk) error) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(req.Messages) == 0 {
		return Result{}, fmt.Errorf("模拟模型需要消息")
	}
	last := req.Messages[len(req.Messages)-1]
	result := Result{HTTPStatus: 200, Usage: Usage{InputTokens: 20, OutputTokens: 10}}
	if last.Role != domain.RoleTool {
		name, args := "", ""
		input := strings.TrimSpace(last.Content)
		switch {
		case strings.HasPrefix(input, "计算 "):
			name = "calc"
			b, _ := json.Marshal(map[string]string{"expression": strings.TrimSpace(strings.TrimPrefix(input, "计算 "))})
			args = string(b)
		case strings.Contains(input, "时间"):
			name = "now"
			args = `{"timezone":"Asia/Shanghai"}`
		case strings.HasPrefix(input, "课程 "):
			name = "course_lookup"
			b, _ := json.Marshal(map[string]string{"topic": strings.TrimSpace(strings.TrimPrefix(input, "课程 "))})
			args = string(b)
		}
		for _, spec := range req.Tools {
			if spec.Name == name {
				result.ToolCalls = []domain.ToolCall{{ID: "demo-call", Name: name, Arguments: args}}
				return result, onChunk(Chunk{FinishReason: "tool_calls"})
			}
		}
		result.Content = "【模拟模式】已收到：" + input + "。试试输入「计算 (12+3)*4」或「现在的时间」。真实模型接入方法见教程。"
	} else {
		result.Content = "【模拟模式】工具执行结果：" + last.Content
	}
	if req.MaxOutputTokens > 0 && len([]rune(result.Content)) > req.MaxOutputTokens {
		result.Content = string([]rune(result.Content)[:req.MaxOutputTokens])
	}
	for _, r := range result.Content {
		timer := time.NewTimer(m.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, ctx.Err()
		case <-timer.C:
		}
		if err := onChunk(Chunk{Delta: string(r)}); err != nil {
			return Result{}, err
		}
	}
	return result, onChunk(Chunk{FinishReason: "stop", Usage: &result.Usage})
}
