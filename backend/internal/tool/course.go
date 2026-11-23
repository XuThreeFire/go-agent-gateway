package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// CourseTool 是二次开发教程的完整参考实现，只读固定的虚构课程数据。
// 默认不加入 Builtins；按 handbook/development/tools.md 完成注册与启用练习。
type CourseTool struct{}

func (CourseTool) Spec() Spec {
	return Spec{Name: "course_lookup", Description: "按主题查询学习课程，仅含虚构示例数据", Parameters: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string"}},"required":["topic"],"additionalProperties":false}`)}
}
func (CourseTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var input struct {
		Topic string `json:"topic"`
	}
	if err := decodeStrict(args, &input); err != nil {
		return "", err
	}
	courses := map[string]string{"agent": "示例课程：Agent 入门；内容：模型、工具和结果回灌。", "sse": "示例课程：SSE 实践；内容：事件序号、重连和补发。"}
	if output, ok := courses[input.Topic]; ok {
		return output, nil
	}
	return "", fmt.Errorf("未知课程主题，请使用 agent 或 sse")
}
