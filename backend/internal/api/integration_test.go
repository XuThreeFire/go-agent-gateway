package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fmt"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/config"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/exec"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/hub"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/llm"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/testutil"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/tool"
)

func TestMySQLHTTPToolReplayAndIsolation(t *testing.T) {
	st := testutil.Database(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root, stop := context.WithCancel(context.Background())
	defer stop()
	h := hub.New(st, hub.Options{BatchSize: 4}, logger)
	hubCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()
	go h.Run(hubCtx)
	models := llm.NewRegistry([]llm.Target{{Name: "demo", Provider: "mock", UpstreamModel: "demo", SupportsToolCall: true}}, llm.ClientOptions{})
	tools := tool.New(tool.Options{}, tool.Builtins()...)
	ex := exec.New(root, st, h, models, exec.Options{Tools: tools}, logger)
	defer func() {
		stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ex.Shutdown(ctx)
		h.FlushAll(ctx)
	}()
	s := New(st, h, models, ex, Options{SessionSecret: strings.Repeat("s", 32), DefaultModel: "demo", Models: []config.Model{{Name: "demo", Provider: "mock", UpstreamModel: "demo", Enabled: true, SupportsToolCall: true}}}, logger)
	server := httptest.NewServer(s.Routes())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	response, err := client.Post(server.URL+"/api/tasks", "application/json", strings.NewReader(`{"input":"计算 2+3","idempotencyKey":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 202 {
		t.Fatalf("创建 HTTP %d", response.StatusCode)
	}
	var created struct {
		TaskID int64 `json:"taskId"`
		RunID  int64 `json:"runId"`
	}
	json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	response, err = client.Get(fmt.Sprintf("%s/api/runs/%d/events", server.URL, created.RunID))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "event: tool.result") || !strings.Contains(text, "event: run.terminal") || !strings.Contains(text, "completed") {
		t.Fatalf("不完整执行事件: %s", text)
	}
	run, _ := st.GetRun(context.Background(), created.RunID)
	if run.Status != domain.StatusCompleted || run.InputTokens == 0 {
		t.Fatal("执行状态与用量未落库")
	}
	// 终态从头回放，不挂起、不重复执行。
	response, err = client.Get(fmt.Sprintf("%s/api/runs/%d/events", server.URL, created.RunID))
	if err != nil {
		t.Fatal(err)
	}
	replayed, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(replayed), "run.terminal") {
		t.Fatal("终态未回放")
	}
	other := &http.Client{Timeout: time.Second}
	response, err = other.Get(fmt.Sprintf("%s/api/tasks/%d", server.URL, created.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("会话隔离失败")
	}
}
