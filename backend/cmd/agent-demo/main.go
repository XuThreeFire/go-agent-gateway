// 最小 Agent：没有 HTTP、SSE 和数据库，只有模型→工具→模型。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/llm"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/tool"
)

func main() {
	input := "计算 (12+3)*4"
	if len(os.Args) > 1 {
		input = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := llm.New(llm.Target{Name: "demo", Provider: "mock", SupportsToolCall: true}, llm.ClientOptions{})
	tools := tool.New(tool.Options{}, tool.Builtins()...)
	var specs []llm.ToolSpec
	for _, s := range tools.Specs() {
		specs = append(specs, llm.ToolSpec{Name: s.Name, Description: s.Description, Parameters: s.Parameters})
	}
	messages := []domain.Message{{Role: domain.RoleUser, Content: input}}
	for step := 0; step < 4; step++ {
		result, err := client.Stream(ctx, llm.Request{Messages: messages, Tools: specs, MaxOutputTokens: 512}, func(c llm.Chunk) error { fmt.Print(c.Delta); return nil })
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(result.ToolCalls) == 0 {
			fmt.Println()
			return
		}
		messages = append(messages, domain.Message{Role: domain.RoleAssistant, Content: result.Content, ToolCalls: result.ToolCalls})
		for _, call := range result.ToolCalls {
			fmt.Printf("工具调用：%s %s\n", call.Name, call.Arguments)
			output, err := tools.Run(ctx, call.Name, call.Arguments)
			if err != nil {
				raw, _ := json.Marshal(map[string]string{"error": err.Error()})
				output = string(raw)
			}
			messages = append(messages, domain.Message{Role: domain.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: output})
		}
	}
	fmt.Fprintln(os.Stderr, "超过最大模型调用次数")
	os.Exit(1)
}
