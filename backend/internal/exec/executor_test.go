package exec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/hub"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/llm"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/tool"
)

// fakeStore 同时满足 exec.Store 与 hub 的存储接口，省得为两个包各写一份假实现。
// 刻意把「事件」和「run 状态」放在一起：取消与恢复都要断言这两者的配合。
type fakeStore struct {
	mu       sync.Mutex
	runs     map[int64]*domain.Run
	events   []domain.Event
	usage    map[string]int64
	clientID string
}

func newFakeStore() *fakeStore {
	return &fakeStore{runs: map[int64]*domain.Run{}, usage: map[string]int64{}, clientID: "c-1"}
}

func (f *fakeStore) GetRun(_ context.Context, runID int64) (domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return domain.Run{}, errors.New("run 不存在")
	}
	return *r, nil
}

func (f *fakeStore) LoadConversation(context.Context, int64, int64) ([]domain.Message, error) {
	return nil, nil
}

func (f *fakeStore) CASStatus(_ context.Context, runID int64, from, to string, patch map[string]any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok || r.Status != from {
		return false, nil
	}
	r.Status = to
	r.Version++
	if v, ok := patch["ended_at"].(time.Time); ok {
		r.EndedAt = &v
	}
	if v, ok := patch["error_message"].(string); ok {
		r.ErrorMessage = &v
	}
	return true, nil
}

func (f *fakeStore) AppendStep(context.Context, domain.Step) error { return nil }

func (f *fakeStore) SetFirstToken(context.Context, int64, time.Time) error { return nil }

func (f *fakeStore) NextStepSeq(context.Context, int64) (int, error) { return 0, nil }

func (f *fakeStore) InterruptRunning(_ context.Context) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int64
	for id, r := range f.runs {
		if r.Status == domain.StatusRunning || r.Status == domain.StatusPending {
			r.Status = domain.StatusInterrupted
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeStore) MaxEventSeq(_ context.Context, runID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var maxSeq int64
	for _, ev := range f.events {
		if ev.RunID == runID && ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	return maxSeq, nil
}

func (f *fakeStore) InsertEvents(_ context.Context, evs []domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
	return nil
}

func (f *fakeStore) ListEvents(_ context.Context, runID, afterSeq int64, limit int) ([]domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Event, 0, limit)
	for _, ev := range f.events {
		if ev.RunID == runID && ev.Seq > afterSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fakeStore) ClientIDOfTask(context.Context, int64) (string, error) { return f.clientID, nil }

func (f *fakeStore) UsageTokens(_ context.Context, scope, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage[scope+"/"+key], nil
}

func (f *fakeStore) putRun(r domain.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := r
	f.runs[r.ID] = &cp
}

func (f *fakeStore) run(runID int64) domain.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.runs[runID]
}

func (f *fakeStore) terminalEvents(runID int64) []domain.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Event
	for _, ev := range f.events {
		if ev.RunID == runID && ev.Type == domain.EventRunTerminal {
			out = append(out, ev)
		}
	}
	return out
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestExecutor(t *testing.T, st *fakeStore, opt Options) *Executor {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := hub.New(st, hub.Options{BatchSize: 1, FlushInterval: 10 * time.Millisecond}, testLogger())
	go h.Run(ctx)
	return New(ctx, st, h, nil, opt, testLogger())
}

// 取消要能写终态，且只写一次：执行器那条路会再试一次 CAS，必须抢输。
func TestCancelWritesSingleAbortedTerminal(t *testing.T) {
	st := newFakeStore()
	st.putRun(domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning})
	e := newTestExecutor(t, st, Options{})

	run, err := e.Cancel(context.Background(), 1)
	if err != nil {
		t.Fatalf("cancel 失败: %v", err)
	}
	if run.Status != domain.StatusAborted {
		t.Fatalf("状态 = %s，期望 aborted", run.Status)
	}

	// 再取消一次：已是终态，必须 409（ConflictError）
	if _, err := e.Cancel(context.Background(), 1); err == nil {
		t.Fatal("取消终态 Run 应该报冲突")
	} else if !isConflict(err) {
		t.Fatalf("错误类型不对: %v", err)
	}

	evs := st.terminalEvents(1)
	if len(evs) != 1 {
		t.Fatalf("run.terminal 应恰好 1 条，实际 %d 条", len(evs))
	}
	var payload domain.RunTerminalPayload
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatalf("解析终态 payload 失败: %v", err)
	}
	if payload.Status != domain.StatusAborted {
		t.Fatalf("终态 payload status = %s，期望 aborted", payload.Status)
	}
}

func isConflict(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}

// scriptedClient 按顺序吐出预先写好的结果，用来驱动 loop 而不碰网络。
type scriptedClient struct {
	target llm.Target
	steps  []llm.Result
	reqs   []llm.Request
}

func (c *scriptedClient) Target() llm.Target { return c.target }

func (c *scriptedClient) Stream(_ context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Result, error) {
	c.reqs = append(c.reqs, req)
	if len(c.reqs) > len(c.steps) {
		return llm.Result{}, errors.New("脚本用完了：loop 比预期多打了一次模型")
	}
	res := c.steps[len(c.reqs)-1]
	if res.Content != "" {
		if err := onChunk(llm.Chunk{Delta: res.Content}); err != nil {
			return llm.Result{}, err
		}
	}
	return res, nil
}

type panicTool struct{}

func (panicTool) Spec() tool.Spec { return tool.Spec{Name: "boom", Description: "总是崩"} }

func (panicTool) Invoke(context.Context, json.RawMessage) (string, error) {
	panic("工具内部炸了")
}

// 一轮工具往返：模型要工具 → 执行器跑工具 → 结果回灌 → 模型给最终回答。
// 重点是第二次请求的上下文结构：没有 assistant 的 tool_calls 和配套的 tool 消息，
// 真实上游会直接 400，所以这里必须断言消息序列。
func TestLoopExecutesToolAndContinues(t *testing.T) {
	st := newFakeStore()
	run := domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning}
	st.putRun(run)
	reg := tool.New(tool.Options{}, tool.CalcTool{})
	e := newTestExecutor(t, st, Options{MaxSteps: 4, Tools: reg})

	client := &scriptedClient{
		target: llm.Target{Name: "m", SupportsToolCall: true},
		steps: []llm.Result{
			{ToolCalls: []domain.ToolCall{{ID: "call_1", Name: "calc", Arguments: `{"expression":"2*21"}`}}},
			{Content: "等于 42"},
		},
	}

	err := e.loop(context.Background(), run, client, []domain.Message{{Role: domain.RoleUser, Content: "2*21 等于多少"}})
	if err != nil {
		t.Fatalf("loop 失败: %v", err)
	}
	if len(client.reqs) != 2 {
		t.Fatalf("应该打两次模型，实际 %d 次", len(client.reqs))
	}
	if len(client.reqs[0].Tools) != 1 || client.reqs[0].Tools[0].Name != "calc" {
		t.Fatalf("第一次请求没带工具声明: %+v", client.reqs[0].Tools)
	}

	second := client.reqs[1].Messages
	if len(second) != 3 {
		t.Fatalf("第二次请求消息数 = %d，期望 3（user + assistant(tool_calls) + tool）", len(second))
	}
	if len(second[1].ToolCalls) != 1 || second[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant 消息没带回 tool_calls: %+v", second[1])
	}
	if second[2].Role != domain.RoleTool || second[2].ToolCallID != "call_1" {
		t.Fatalf("tool 消息结构不对: %+v", second[2])
	}
	if !strings.Contains(second[2].Content, "42") {
		t.Fatalf("工具结果没回灌给模型: %q", second[2].Content)
	}
}

// 工具的业务错误（这里是除零）要回灌给模型，让它自己改，而不是把 Run 判死。
func TestLoopFeedsToolErrorBackToModel(t *testing.T) {
	st := newFakeStore()
	run := domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning}
	st.putRun(run)
	reg := tool.New(tool.Options{}, tool.CalcTool{})
	e := newTestExecutor(t, st, Options{MaxSteps: 4, Tools: reg})

	client := &scriptedClient{
		target: llm.Target{Name: "m", SupportsToolCall: true},
		steps: []llm.Result{
			{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "calc", Arguments: `{"expression":"1/0"}`}}},
			{Content: "除数不能为零"},
		},
	}
	if err := e.loop(context.Background(), run, client, nil); err != nil {
		t.Fatalf("业务错误不应该让 Run 失败: %v", err)
	}
	toolMsg := client.reqs[1].Messages[len(client.reqs[1].Messages)-1]
	if !strings.Contains(toolMsg.Content, "除数不能为 0") {
		t.Fatalf("错误没回灌给模型: %q", toolMsg.Content)
	}
	if toolMsg.Role != domain.RoleTool {
		t.Fatalf("回灌消息角色 = %s，期望 tool", toolMsg.Role)
	}
}

// 工具 panic 是代码 bug：不能再给模型试第二次，必须让 Run 失败并归成 tool_error。
func TestLoopFailsOnToolPanic(t *testing.T) {
	st := newFakeStore()
	run := domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning}
	st.putRun(run)
	reg := tool.New(tool.Options{}, panicTool{})
	e := newTestExecutor(t, st, Options{MaxSteps: 4, Tools: reg})

	client := &scriptedClient{
		target: llm.Target{Name: "m", SupportsToolCall: true},
		steps: []llm.Result{
			{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "boom", Arguments: "{}"}}},
		},
	}
	err := e.loop(context.Background(), run, client, nil)
	if err == nil {
		t.Fatal("工具 panic 应该让 loop 返回错误")
	}
	if !errors.Is(err, tool.ErrPanic) {
		t.Fatalf("错误应能被识别为 panic: %v", err)
	}
	if got := classifyRunError(err); got != domain.ErrToolError {
		t.Fatalf("分类 = %s，期望 tool_error", got)
	}
}

// 模型一直要工具不收敛：步数用尽要立刻停，且不能再执行最后一次的工具。
func TestLoopStopsAtMaxSteps(t *testing.T) {
	st := newFakeStore()
	run := domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning}
	st.putRun(run)
	reg := tool.New(tool.Options{}, tool.CalcTool{})
	e := newTestExecutor(t, st, Options{MaxSteps: 2, Tools: reg})

	call := llm.Result{ToolCalls: []domain.ToolCall{{ID: "c", Name: "calc", Arguments: `{"expression":"1+1"}`}}}
	client := &scriptedClient{
		target: llm.Target{Name: "m", SupportsToolCall: true},
		steps:  []llm.Result{call, call, call},
	}

	err := e.loop(context.Background(), run, client, nil)
	if err == nil {
		t.Fatal("步数用尽应该报错")
	}
	if got := classifyRunError(err); got != domain.ErrMaxSteps {
		t.Fatalf("分类 = %s，期望 max_steps", got)
	}
	if len(client.reqs) != 2 {
		t.Fatalf("模型调用次数 = %d，应恰好等于 MaxSteps", len(client.reqs))
	}
}

// 模型声明不支持函数调用时不能发 tools：有的供应商收到就直接 400。
func TestLoopSkipsToolsWhenModelDoesNotSupport(t *testing.T) {
	st := newFakeStore()
	run := domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning}
	st.putRun(run)
	reg := tool.New(tool.Options{}, tool.CalcTool{})
	e := newTestExecutor(t, st, Options{MaxSteps: 2, Tools: reg})

	client := &scriptedClient{
		target: llm.Target{Name: "m", SupportsToolCall: false},
		steps:  []llm.Result{{Content: "直接回答"}},
	}
	if err := e.loop(context.Background(), run, client, nil); err != nil {
		t.Fatalf("loop 失败: %v", err)
	}
	if len(client.reqs[0].Tools) != 0 {
		t.Fatalf("不该给不支持工具的模型发 tools: %+v", client.reqs[0].Tools)
	}
}

// 配额：0 表示不限；达上限拒绝；未达上限放行。
func TestCheckQuota(t *testing.T) {
	st := newFakeStore()
	run := domain.Run{ID: 1, TaskID: 7}
	key := quotaKey(st.clientID, time.Now())

	if e := newTestExecutor(t, st, Options{DailyTokensPerClient: 0}); e.checkQuota(context.Background(), run) != nil {
		t.Fatal("上限为 0 时不应该拦")
	}

	st.usage[quotaScope+"/"+key] = 5
	if e := newTestExecutor(t, st, Options{DailyTokensPerClient: 10}); e.checkQuota(context.Background(), run) != nil {
		t.Fatal("未达上限不应该拦")
	}

	st.usage[quotaScope+"/"+key] = 10
	e := newTestExecutor(t, st, Options{DailyTokensPerClient: 10})
	if err := e.checkQuota(context.Background(), run); err == nil {
		t.Fatal("达到上限应该拒绝")
	}
}

// 用量记录只记一次，且按客户端 + 日期聚合。
// 启动扫描要把残留 running / pending 都收掉，并给每个 Run 补一条终态事件。
// 关键是 seq 必须接着库里的 MAX(seq) 走——hub 的进程内计数器重启后从 1 开始，会撞唯一键。
func TestRecoverInterruptsResidueAndAppendsTerminal(t *testing.T) {
	st := newFakeStore()
	st.putRun(domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning})
	st.putRun(domain.Run{ID: 2, TaskID: 2, Status: domain.StatusPending})
	st.putRun(domain.Run{ID: 3, TaskID: 3, Status: domain.StatusCompleted})
	// run 1 崩之前已经有 3 条事件
	st.events = append(st.events, domain.Event{RunID: 1, Seq: 1, Type: domain.EventRunStarted})
	st.events = append(st.events, domain.Event{RunID: 1, Seq: 3, Type: domain.EventTurnDelta})

	e := newTestExecutor(t, st, Options{})
	n, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("恢复扫描失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应扫到 2 条残留，实际 %d", n)
	}
	if got := st.run(1).Status; got != domain.StatusInterrupted {
		t.Fatalf("run 1 状态 = %s，期望 interrupted", got)
	}
	if got := st.run(2).Status; got != domain.StatusInterrupted {
		t.Fatalf("run 2 状态 = %s，期望 interrupted", got)
	}
	if got := st.run(3).Status; got != domain.StatusCompleted {
		t.Fatalf("已终态的 run 3 不该被改，实际 %s", got)
	}
	if evs := st.terminalEvents(1); len(evs) != 1 || evs[0].Seq != 4 {
		t.Fatalf("run 1 终态事件应为 1 条且 seq=4，实际 %d 条", len(evs))
	}
	if evs := st.terminalEvents(2); len(evs) != 1 || evs[0].Seq != 1 {
		t.Fatalf("run 2 终态事件应为 1 条且 seq=1，实际 %d 条", len(evs))
	}
}

func TestLoopRejectsContextAndTokenBudgetBeforeCallingModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opt    Options
		tokens int64
	}{{"context", Options{MaxContextBytes: 4}, 0}, {"tokens", Options{MaxRunTokens: 5}, 6}} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			run := domain.Run{ID: 1, TaskID: 1, Status: domain.StatusRunning, InputTokens: tc.tokens}
			st.putRun(run)
			e := newTestExecutor(t, st, tc.opt)
			client := &scriptedClient{}
			err := e.loop(context.Background(), run, client, []domain.Message{{Role: domain.RoleUser, Content: "hello"}})
			if err == nil || len(client.reqs) != 0 {
				t.Fatal("超预算时不得发送模型请求")
			}
		})
	}
}
func TestPublicErrorDoesNotEchoPrivateDiagnostics(t *testing.T) {
	const sensitive = "private-internal-diagnostic"
	for _, err := range []error{errors.New(sensitive), &llm.APIError{Status: 401, Body: sensitive}} {
		if strings.Contains(sanitizeMessage(err), sensitive) {
			t.Fatal("内部诊断不得进入终态公开错误")
		}
	}
}
