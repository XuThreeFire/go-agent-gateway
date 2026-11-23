package llm

import (
	"context"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"testing"
	"time"
)

func TestMockSelectsToolWithoutNetworkOrKey(t *testing.T) {
	c := &mockClient{target: Target{Provider: "mock"}, delay: 0}
	res, err := c.Stream(context.Background(), Request{Messages: []domain.Message{{Role: domain.RoleUser, Content: "计算 2+3"}}, Tools: []ToolSpec{{Name: "calc"}}}, func(Chunk) error { return nil })
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "calc" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
func TestMockHonorsCancellation(t *testing.T) {
	c := &mockClient{delay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Stream(ctx, Request{}, func(Chunk) error { return nil }); err == nil {
		t.Fatal("应传播取消")
	}
}
