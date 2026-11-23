package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/hub"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/llm"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/prompt"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/tool"
)

// Store 是执行器需要的持久化能力，接口开在这里，测试可以塞假的。
type Store interface {
	GetRun(ctx context.Context, runID int64) (domain.Run, error)
	LoadConversation(ctx context.Context, taskID, turnID int64) ([]domain.Message, error)
	CASStatus(ctx context.Context, runID int64, from, to string, patch map[string]any) (bool, error)
	AppendStep(ctx context.Context, step domain.Step) error
	SetFirstToken(ctx context.Context, runID int64, at time.Time) error
	NextStepSeq(ctx context.Context, runID int64) (int, error)
	InterruptRunning(ctx context.Context) ([]int64, error)
	MaxEventSeq(ctx context.Context, runID int64) (int64, error)
	InsertEvents(ctx context.Context, events []domain.Event) error
	ClientIDOfTask(ctx context.Context, taskID int64) (string, error)
	UsageTokens(ctx context.Context, scope, scopeKey string) (int64, error)
}

// ConflictError 表示这次操作和 Run 的当前状态冲突（已终态、或同 Task 还有活跃 Run）。
// 实现 Is(domain.ErrConflict)，api 层用 errors.Is 就能映射到 409 并带回当前状态。
type ConflictError struct {
	RunID  int64
	Status string
	Reason string
}

func (e *ConflictError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("conflict: %s（run %d 当前 %s）", e.Reason, e.RunID, e.Status)
	}
	return fmt.Sprintf("conflict: run %d 当前 %s", e.RunID, e.Status)
}

func (e *ConflictError) Is(target error) bool { return target == domain.ErrConflict }

type Options struct {
	PromptStore       *prompt.Store
	DailyTokensGlobal int64
	MaxConcurrentRuns int
	SystemPrompt      string
	MaxOutputTokens   int
	MaxContextBytes   int
	MaxRunTokens      int64
	MaxSteps          int
	StepTimeout       time.Duration
	// ToolTimeout 单次工具调用超时。工具可能卡住（慢速 DNS、大文件），
	// 不能只靠 StepTimeout——那会把整个模型步一起掐掉。
	ToolTimeout time.Duration
	// DailyTokensPerClient 单客户端每日 token 上限，0 表示不限。
	DailyTokensPerClient int64
	// Tools 是模型可用的工具集，nil 表示本次部署不启用工具。
	Tools *tool.Registry
}

// Executor 跑 agent 循环：拼上下文、调模型、跑工具、判终态。
//
// 它不知道有没有客户端在看（D-01），也不 import api：事件往 hub 一丢就走。
// 一个 Run 一个 goroutine，受信号量限制；满了直接拒绝，不排队（D-04）。
type Executor struct {
	store  Store
	hub    *hub.Hub
	models *llm.Registry
	tools  *tool.Registry
	log    *slog.Logger

	root context.Context
	sem  chan struct{}
	opt  Options

	// cancels 存每个在跑的 Run 的取消函数。取消必须能穿透到流式读取，
	// 光改数据库状态是拦不住一个正在读 response body 的 goroutine 的。
	lifeMu   sync.Mutex
	stopping bool
	wg       sync.WaitGroup
	cancels  sync.Map // map[int64]context.CancelFunc
}

func New(root context.Context, st Store, h *hub.Hub, models *llm.Registry, opt Options, log *slog.Logger) *Executor {
	if opt.MaxOutputTokens <= 0 {
		opt.MaxOutputTokens = 2048
	}
	if opt.MaxContextBytes <= 0 {
		opt.MaxContextBytes = 64000
	}
	if opt.MaxRunTokens <= 0 {
		opt.MaxRunTokens = 20000
	}
	if opt.MaxConcurrentRuns <= 0 {
		opt.MaxConcurrentRuns = 4
	}
	if opt.MaxSteps <= 0 {
		opt.MaxSteps = 8
	}
	if opt.StepTimeout <= 0 {
		opt.StepTimeout = 300 * time.Second
	}
	if opt.ToolTimeout <= 0 {
		opt.ToolTimeout = 10 * time.Second
	}
	return &Executor{
		store:  st,
		hub:    h,
		models: models,
		tools:  opt.Tools,
		log:    log,
		root:   root,
		sem:    make(chan struct{}, opt.MaxConcurrentRuns),
		opt:    opt,
	}
}

// Submit 启动执行。注意不能传 HTTP 请求的 ctx——请求结束（202 已返回）就取消，
// 那会把执行一起掐掉，正好踩中 D-01 的反面。
func (e *Executor) Submit(runID int64) {
	e.lifeMu.Lock()
	if e.stopping {
		e.lifeMu.Unlock()
		e.finishWithFailure(runID, domain.ErrProtocol, "服务正在关闭")
		return
	}
	e.wg.Add(1)
	e.lifeMu.Unlock()
	select {
	case e.sem <- struct{}{}:
		go func() { defer e.wg.Done(); e.execute(runID) }()
	default:
		go func() { defer e.wg.Done(); e.reject(runID) }()
	}
}

// Shutdown 停止接收执行并等待所有收尾写入。调用前先取消执行器的 root。
func (e *Executor) Shutdown(ctx context.Context) error {
	e.lifeMu.Lock()
	e.stopping = true
	e.lifeMu.Unlock()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Executor) reject(runID int64) {
	e.log.Warn("并发已满，拒绝启动", "runId", runID, "limit", e.opt.MaxConcurrentRuns)
	e.finishWithFailure(runID, domain.ErrQuotaExceeded, fmt.Sprintf("并发执行数已达上限 %d，请稍后重试", e.opt.MaxConcurrentRuns))
}

// Cancel 请求取消。活跃 goroutine 在模型步骤记账后写终态；未启动的 Run 直接收尾。
// 因此响应是 202 接受取消，而不是同步承诺所有资源已经退出。
func (e *Executor) Cancel(ctx context.Context, runID int64) (domain.Run, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return domain.Run{}, err
	}
	if domain.IsTerminal(run.Status) {
		return run, &ConflictError{RunID: run.ID, Status: run.Status, Reason: "已处于终态，无法取消"}
	}

	if fn, ok := e.cancels.Load(runID); ok {
		fn.(context.CancelFunc)()
		return run, nil
	}
	e.finish(runID, domain.StatusAborted, "", "用户取消")

	fresh, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return run, nil
	}
	return fresh, nil
}

// Recover 是启动扫描：把上个进程残留的 running / pending 置为 interrupted，
// 并给每个 Run 补一条 run.terminal——客户端重连时能从库里读到「中断了」这个事实。
//
// 这条终态事件**不走 hub**，直接写库：hub 的 seq 是进程内计数器，重启后从 1 开始，
// 拿去给老 Run 补事件会撞 uk_run_seq(run_id, seq)。跨进程补事件只能先查 MAX(seq) 再插。
//
// 不自动重跑：agent 的中间点不可幂等重放（可能正处在工具调用中途），
// 自动重跑有副作用风险，交给人决定（继续 = 新建 attempt）。
func (e *Executor) Recover(ctx context.Context) (int, error) {
	ids, err := e.store.InterruptRunning(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		fresh, err := e.store.GetRun(ctx, id)
		if err != nil {
			e.log.Warn("恢复扫描：读取 run 失败", "runId", id, "err", err)
			continue
		}
		payload := domain.RunTerminalPayload{
			Status:       domain.StatusInterrupted,
			ErrorMessage: "进程重启，执行中断",
			InputTokens:  fresh.InputTokens,
			OutputTokens: fresh.OutputTokens,
			// 不报 durationMs：进程死在哪一刻无从得知，用「重启时间 - 开始时间」
			// 会把停机时长算进执行时长，宁可缺这个字段也不给错数。
			DurationMS: 0,
			// 首 token 是进程活着时记下的，可以报；没首 token 自然为 0（omitempty 不出现）。
			TTFTMS: fresh.TTFTMS(),
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			e.log.Error("恢复扫描：序列化终态事件失败", "runId", id, "err", err)
			continue
		}
		seq, err := e.store.MaxEventSeq(ctx, id)
		if err != nil {
			e.log.Error("恢复扫描：取事件序号失败", "runId", id, "err", err)
			continue
		}
		ev := domain.Event{
			RunID:     id,
			Seq:       seq + 1,
			Type:      domain.EventRunTerminal,
			Payload:   raw,
			CreatedAt: time.Now().UTC(),
		}
		if err := e.store.InsertEvents(ctx, []domain.Event{ev}); err != nil {
			e.log.Error("恢复扫描：写终态事件失败", "runId", id, "err", err)
		}
	}
	return len(ids), nil
}

func (e *Executor) execute(runID int64) {
	defer func() { <-e.sem }()
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("执行器 panic，已兜底转失败", "runId", runID)
			e.finishWithFailure(runID, domain.ErrProtocol, "执行器内部错误")
		}
	}()

	// 每个 Run 一条可取消的 ctx：取消时流式读取立刻返回，不用等上游吐完。
	// 但它挂在 root 上，服务关停时同样能一把全掐。
	ctx, cancel := context.WithCancel(e.root)
	defer cancel()
	e.cancels.Store(runID, cancel)
	defer e.cancels.Delete(runID)

	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		e.log.Error("读取 run 失败", "runId", runID, "err", err)
		return
	}

	modelName := snapshotModelName(run.ModelSnapshot)
	client, ok := e.models.Get(modelName)
	if !ok {
		e.log.Error("模型不在配置清单中", "runId", runID, "model", modelName)
		e.finishWithFailure(runID, domain.ErrProtocol, "模型不可用: "+modelName)
		return
	}

	var snapshot domain.ModelSnapshot
	if json.Unmarshal(run.ModelSnapshot, &snapshot) == nil && snapshot.UpstreamModel != "" {
		target := client.Target()
		if target.UpstreamModel != snapshot.UpstreamModel || target.Provider != snapshot.Provider || target.BaseURL != snapshot.BaseURL {
			e.finishWithFailure(runID, domain.ErrProtocol, "模型路由已变更，请新建会话以使用当前模型配置")
			return
		}
	}

	// 配额在「拿信号量之后、迁移到 running 之前」检查：这个位置错了要么烧钱要么漏拦。
	if err := e.checkQuota(ctx, run); err != nil {
		e.fail(ctx, runID, err)
		return
	}

	startedAt := time.Now().UTC()
	moved, err := e.store.CASStatus(ctx, runID, domain.StatusPending, domain.StatusRunning, map[string]any{"started_at": startedAt})
	if err != nil {
		e.log.Error("状态迁移 pending→running 失败", "runId", runID, "err", err)
		return
	}
	if !moved {
		// 已被取消或抢占：不执行，也不覆盖它人写下的终态。
		e.log.Info("Run 已不在 pending，放弃执行", "runId", runID)
		return
	}

	e.publish(ctx, runID, domain.EventRunStarted, domain.RunStartedPayload{
		TaskID: run.TaskID,
		TurnID: run.TurnID,
		Model:  modelName,
	})

	msgs, err := e.store.LoadConversation(ctx, run.TaskID, run.TurnID)
	if err != nil {
		e.fail(ctx, runID, err)
		return
	}

	msgs = e.withSystem(msgs)

	if err := e.loop(ctx, run, client, msgs); err != nil {
		e.fail(ctx, runID, err)
		return
	}
	e.complete(ctx, runID, startedAt)
}

// withSystem 在 Run 开头取一次快照，后续模型步骤不受热重载影响。
func (e *Executor) withSystem(msgs []domain.Message) []domain.Message {
	sys := e.opt.SystemPrompt
	if e.opt.PromptStore != nil && e.opt.PromptStore.Path() != "" {
		sys = e.opt.PromptStore.Get()
	}
	if sys == "" {
		return msgs
	}
	out := make([]domain.Message, 0, len(msgs)+1)
	out = append(out, domain.Message{Role: domain.RoleSystem, Content: sys})
	return append(out, msgs...)
}

// loop 是 agent 本体：模型 → 工具 → 模型，直到模型不再要工具、或步数用尽。
//
// 四个刻意的约束：
//
//   - MaxSteps 数的是**模型调用次数**，不是工具次数。一次模型调用可以带多个工具，
//     真正让成本失控的是反复对话，所以按模型调用来计。
//   - 每轮都查配额。一个 Run 可以打十几次模型，只在开跑前查一次等于没查。
//   - 工具**串行**执行。并行的收益只在「多个工具互不依赖且都很慢」时才明显，
//     而内置工具都是毫秒级；串行换来的是确定的事件顺序和可读的日志。
//   - 步数用尽时**不再执行**本轮工具：跑完也没机会把结果回给模型，纯浪费。
func (e *Executor) loop(ctx context.Context, run domain.Run, client llm.Client, msgs []domain.Message) error {
	tools := e.toolSpecs(client)

	for step := 0; step < e.opt.MaxSteps; step++ {
		if err := e.checkQuota(ctx, run); err != nil {
			return err
		}

		raw, _ := json.Marshal(msgs)
		if len(raw) > e.opt.MaxContextBytes {
			return fmt.Errorf("上下文超过 %d 字节，请新建会话或缩短工具结果", e.opt.MaxContextBytes)
		}
		fresh, err := e.store.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
		if fresh.InputTokens+fresh.OutputTokens >= e.opt.MaxRunTokens {
			return &quotaError{scope: "单次执行", used: fresh.InputTokens + fresh.OutputTokens, limit: e.opt.MaxRunTokens}
		}
		res, err := e.runModelStep(ctx, run, client, msgs, tools)
		if err != nil {
			return err
		}
		if len(res.ToolCalls) == 0 {
			return nil
		}
		if step == e.opt.MaxSteps-1 {
			return &maxStepsError{limit: e.opt.MaxSteps, pending: len(res.ToolCalls)}
		}

		// 发起调用的这条 assistant 消息必须原样回灌：下一轮上游要按 tool_call_id
		// 把 tool 消息和调用对上，缺了它直接 400。
		msgs = append(msgs, domain.Message{
			Role:      domain.RoleAssistant,
			Content:   res.Content,
			ToolCalls: res.ToolCalls,
		})

		for _, call := range res.ToolCalls {
			out, err := e.runToolStep(ctx, run, call)
			if err != nil {
				return err
			}
			msgs = append(msgs, domain.Message{
				Role:       domain.RoleTool,
				Content:    out,
				ToolCallID: call.ID,
				Name:       call.Name,
			})
		}
	}
	return &maxStepsError{limit: e.opt.MaxSteps}
}

// toolSpecs 决定这次要不要把工具声明发给上游。
// 模型自己声明不支持函数调用时一律不带：有的供应商收到 tools 会直接 400，
// 有的会安静地忽略，两种都表现为「模型莫名其妙不干活」，不如不发。
func (e *Executor) toolSpecs(client llm.Client) []llm.ToolSpec {
	if e.tools == nil || e.tools.Len() == 0 {
		return nil
	}
	if !client.Target().SupportsToolCall {
		return nil
	}
	specs := e.tools.Specs()
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, llm.ToolSpec{Name: s.Name, Description: s.Description, Parameters: s.Parameters})
	}
	return out
}

// runModelStep 打一次模型调用，落 step 记录并推送增量。
// 返回的 Result 里带模型这轮的完整文本与工具调用，由 loop 决定下一步走向。
func (e *Executor) runModelStep(ctx context.Context, run domain.Run, client llm.Client, msgs []domain.Message, tools []llm.ToolSpec) (llm.Result, error) {
	seq, err := e.store.NextStepSeq(ctx, run.ID)
	if err != nil {
		return llm.Result{}, fmt.Errorf("取步骤序号失败: %w", err)
	}
	target := client.Target()

	e.publish(ctx, run.ID, domain.EventStepStarted, domain.StepStartedPayload{
		Step:  seq,
		Type:  domain.StepModelCall,
		Model: target.Name,
	})

	stepCtx, cancel := context.WithTimeout(ctx, e.opt.StepTimeout)
	defer cancel()

	start := time.Now()
	firstToken := true
	res, streamErr := client.Stream(stepCtx, llm.Request{Messages: msgs, Tools: tools, MaxOutputTokens: e.opt.MaxOutputTokens}, func(c llm.Chunk) error {
		if err := ctx.Err(); err != nil {
			// 用户取消或服务关停：立刻停读，不等上游把话说完。
			return err
		}
		if c.Delta == "" {
			// 工具片段与 usage 帧不用往下推：工具调用由 step/tool.result 事件承载，
			// 客户端不需要看参数是怎么一段段拼起来的。
			return nil
		}
		if firstToken {
			firstToken = false
			// TTFT 只记第一次，后续 chunk 不再覆盖（表结构里它是单值列）。
			if err := e.store.SetFirstToken(ctx, run.ID, time.Now().UTC()); err != nil {
				e.log.Warn("记录首 token 时间失败", "runId", run.ID, "err", err)
			}
		}
		_, err := e.publishErr(ctx, run.ID, domain.EventTurnDelta, domain.TurnDeltaPayload{Delta: c.Delta})
		return err
	})
	latency := int(time.Since(start).Milliseconds())

	// 无论成功失败都落 step：失败但没有调用记录，是最难查的那种问题。
	record := domain.Step{
		RunID:        run.ID,
		Seq:          seq,
		Type:         domain.StepModelCall,
		Provider:     target.Provider,
		Model:        target.UpstreamModel,
		LatencyMS:    &latency,
		InputTokens:  res.Usage.InputTokens,
		OutputTokens: res.Usage.OutputTokens,
	}
	if res.HTTPStatus != 0 {
		status := res.HTTPStatus
		record.HTTPStatus = &status
	}
	if streamErr != nil {
		category := string(llm.Classify(streamErr))
		record.ErrorCategory = &category
	}
	wctx, cancelWrite := e.writeCtx()
	if err := e.store.AppendStep(wctx, record); err != nil {
		cancelWrite()
		return res, fmt.Errorf("记录模型步骤与用量失败: %w", err)
	}
	cancelWrite()
	e.log.Info("模型调用完成", "runId", run.ID, "step", seq, "model", target.Name, "latencyMs", latency, "inputTokens", res.Usage.InputTokens, "cachedTokens", res.Usage.CachedTokens, "cacheHitRate", cacheHitRate(res.Usage), "prefixFp", res.PrefixFingerprint)

	if streamErr != nil {
		return res, streamErr
	}
	if err := ctx.Err(); err != nil {
		// 已取消：先返回，让执行循环统一写终态，不再发布普通步骤事件。
		return res, err
	}

	e.publish(ctx, run.ID, domain.EventStepFinish, domain.StepFinishedPayload{
		Step:         seq,
		LatencyMS:    latency,
		HTTPStatus:   res.HTTPStatus,
		InputTokens:  res.Usage.InputTokens,
		OutputTokens: res.Usage.OutputTokens,
	})
	return res, nil
}

// runToolStep 执行一次工具调用：落 step、推 tool.result，返回要回灌给模型的内容。
//
// 错误处理分两层，这是这块最需要说清楚的地方：
//   - 参数写错、工具不存在、除零这类**业务错误**：回灌给模型，它下一轮会自己改。
//     这是 agent 的正常恢复路径，当成失败直接掐掉反而更差。
//   - panic：说明工具实现自己崩了，再试一次只会再崩，直接判 Run 失败（tool_error）。
func (e *Executor) runToolStep(ctx context.Context, run domain.Run, call domain.ToolCall) (string, error) {
	seq, err := e.store.NextStepSeq(ctx, run.ID)
	if err != nil {
		return "", fmt.Errorf("取步骤序号失败: %w", err)
	}
	e.publish(ctx, run.ID, domain.EventStepStarted, domain.StepStartedPayload{
		Step: seq,
		Type: domain.StepToolCall,
		Tool: call.Name,
	})

	if e.tools == nil {
		// 上游硬塞了一个工具调用（模型不支持时不该发生），告诉它没有工具可用，
		// 让它用已有上下文直接回答，比整条 Run 失败划算。
		return `{"error":"本服务未启用工具，请直接回答"}`, nil
	}

	start := time.Now()
	toolCtx, cancel := context.WithTimeout(ctx, e.opt.ToolTimeout)
	out, toolErr := e.tools.Run(toolCtx, call.Name, call.Arguments)
	cancel()
	latency := int(time.Since(start).Milliseconds())

	record := domain.Step{
		RunID:     run.ID,
		Seq:       seq,
		Type:      domain.StepToolCall,
		ToolName:  &call.Name,
		LatencyMS: &latency,
		ToolInput: clampJSON(call.Arguments, 4<<10),
	}
	payload := domain.ToolResultPayload{
		Step:       seq,
		Tool:       call.Name,
		CallID:     call.ID,
		Input:      truncateRunes(call.Arguments, 1024),
		DurationMS: latency,
	}
	if toolErr != nil {
		category := string(domain.ErrToolError)
		record.ErrorCategory = &category
		payload.Error = "工具执行失败，请检查参数"
		if errors.Is(toolErr, tool.ErrPanic) {
			payload.Error = "工具内部错误"
		}
	} else {
		// 工具结果一律以 JSON 字符串存：tool_result 是 JSON 列，直接塞纯文本
		// MySQL 会报 Invalid JSON text，整条 step 就丢了。
		record.ToolResult = jsonValue(out)
		payload.Result = truncateRunes(out, 1024)
	}

	wctx, cancelWrite := e.writeCtx()
	if err := e.store.AppendStep(wctx, record); err != nil {
		cancelWrite()
		return "", fmt.Errorf("记录工具步骤失败")
	}
	cancelWrite()

	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.publish(ctx, run.ID, domain.EventToolResult, payload)

	if toolErr != nil {
		if errors.Is(toolErr, tool.ErrPanic) {
			return "", toolErr
		}
		return toolFeedback(toolErr), nil
	}
	return out, nil
}

// complete 写终态并补一条 run.terminal。
// 顺序很重要：先 CAS 成功，再发事件，最后强制 flush——保证客户端重连时库里已有终态。
func (e *Executor) complete(ctx context.Context, runID int64, startedAt time.Time) {
	writeCtx, cancelWrite := e.writeCtx()
	defer cancelWrite()
	endedAt := time.Now().UTC()
	if err := e.hub.Flush(writeCtx, runID); err != nil {
		e.finishWithFailure(runID, domain.ErrProtocol, "事件持久化失败，请检查数据库")
		return
	}

	moved, err := e.store.CASStatus(writeCtx, runID, domain.StatusRunning, domain.StatusCompleted, map[string]any{"ended_at": endedAt})
	if err != nil {
		e.log.Error("状态迁移 running→completed 失败", "runId", runID, "err", err)
		return
	}
	if !moved {
		e.log.Info("Run 已不在 running，跳过完成", "runId", runID)
		return
	}

	fresh, err := e.store.GetRun(writeCtx, runID)
	if err != nil {
		e.log.Error("读取 run 汇总失败", "runId", runID, "err", err)
		fresh = domain.Run{ID: runID}
	}

	e.publish(writeCtx, runID, domain.EventRunTerminal, domain.RunTerminalPayload{
		Status:       domain.StatusCompleted,
		InputTokens:  fresh.InputTokens,
		OutputTokens: fresh.OutputTokens,
		DurationMS:   endedAt.Sub(startedAt).Milliseconds(),
		TTFTMS:       fresh.TTFTMS(),
	})
	e.flushAndDrop(writeCtx, runID)
}

func (e *Executor) fail(ctx context.Context, runID int64, cause error) {
	// 关闭流程中的取消不是失败，按 aborted 落库：语义上「用户/运维打断了它」。
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) && e.root.Err() != nil {
		e.finish(runID, domain.StatusAborted, "", "执行被中断")
		return
	}
	category := classifyRunError(cause)
	e.log.Warn("Run 失败", "runId", runID, "category", category)
	e.finish(runID, domain.StatusFailed, string(category), sanitizeMessage(cause))
}

// classifyRunError 先认网关自己造的错误类型，再交给供应商适配层。
// 顺序不能反：llm.Classify 什么都归 protocol，会把「步数用尽」「配额触顶」
// 一起吞成协议错误，指标就失真了。
func classifyRunError(cause error) domain.ErrorCategory {
	var quota *quotaError
	if errors.As(cause, &quota) {
		return domain.ErrQuotaExceeded
	}
	var maxSteps *maxStepsError
	if errors.As(cause, &maxSteps) {
		return domain.ErrMaxSteps
	}
	if errors.Is(cause, tool.ErrPanic) {
		return domain.ErrToolError
	}
	return llm.Classify(cause)
}

// maxStepsError 表示模型在 max_steps 内没收敛（一直在要工具）。
type maxStepsError struct {
	limit   int
	pending int
}

func (e *maxStepsError) Error() string {
	if e.pending > 0 {
		return fmt.Sprintf("已达到最大步数 %d，模型仍在请求工具调用（本轮 %d 个），判定为工具循环", e.limit, e.pending)
	}
	return fmt.Sprintf("已达到最大步数 %d，模型未给出最终回答", e.limit)
}

// finishWithFailure 用于「还没开始执行就失败」的场景：模型不存在、并发已满、panic 兜底。
func (e *Executor) finishWithFailure(runID int64, category domain.ErrorCategory, msg string) {
	e.finish(runID, domain.StatusFailed, string(category), msg)
}

// finish 从 pending 和 running 里挑一个当前状态做 CAS，谁成功算谁。
// 终态只写一次：两次 CAS 不会都成功，因为状态已经变了。
func (e *Executor) finish(runID int64, status, category, message string) {
	writeCtx, cancelWrite := e.writeCtx()
	defer cancelWrite()
	endedAt := time.Now().UTC()

	patch := map[string]any{"ended_at": endedAt}
	if category != "" {
		patch["error_category"] = category
	}
	if message != "" {
		patch["error_message"] = message
	}

	moved, err := e.store.CASStatus(writeCtx, runID, domain.StatusRunning, status, patch)
	if err != nil {
		e.log.Error("写失败终态出错", "runId", runID, "err", err)
		return
	}
	if !moved {
		moved, err = e.store.CASStatus(writeCtx, runID, domain.StatusPending, status, patch)
		if err != nil {
			e.log.Error("写失败终态出错", "runId", runID, "err", err)
			return
		}
	}
	if !moved {
		// 已经是终态了，说明有人先写了，保持先到先得。
		return
	}

	fresh, err := e.store.GetRun(writeCtx, runID)
	if err != nil {
		fresh = domain.Run{ID: runID}
	}
	var durationMS int64
	if fresh.StartedAt != nil {
		durationMS = endedAt.Sub(*fresh.StartedAt).Milliseconds()
	}
	e.publish(writeCtx, runID, domain.EventRunTerminal, domain.RunTerminalPayload{
		Status:        status,
		ErrorCategory: category,
		ErrorMessage:  message,
		InputTokens:   fresh.InputTokens,
		OutputTokens:  fresh.OutputTokens,
		DurationMS:    durationMS,
		TTFTMS:        fresh.TTFTMS(),
	})
	e.flushAndDrop(writeCtx, runID)
}

// ---------- 配额 ----------

// quotaScope 必须与 store.ScopeClientDay 一致。这里不 import store，
// 是为了让 exec 只依赖自己声明的接口（上层不该知道持久化包的常量）。
const quotaScope = "client_day"
const globalQuotaScope = "global_day"

func globalQuotaKey(at time.Time) string { return "all:" + at.UTC().Format("20060102") }

func quotaKey(clientID string, at time.Time) string {
	return clientID + ":" + at.UTC().Format("20060102")
}

// quotaError 单独成型，是为了让 fail() 能把「配额触顶」和「上游出错」区分开。
// 用 fmt.Errorf 拼字符串的话，分类只能靠字符串匹配。
type quotaError struct {
	scope string
	used  int64
	limit int64
}

func (e *quotaError) Error() string {
	scope := e.scope
	if scope == "" {
		scope = "客户端"
	}
	if scope == "单次执行" {
		return fmt.Sprintf("本次执行 token 已用 %d，上限 %d", e.used, e.limit)
	}
	return fmt.Sprintf("%s今日 token 已用 %d，上限 %d，明日 UTC 零点重置", scope, e.used, e.limit)
}

// checkQuota 判断这个客户端今天还有没有额度。
// 启用配额后读取失败即拒绝，避免故障期间无限消耗。
func (e *Executor) checkQuota(ctx context.Context, run domain.Run) error {
	if limit := e.opt.DailyTokensGlobal; limit > 0 {
		used, err := e.store.UsageTokens(ctx, globalQuotaScope, globalQuotaKey(time.Now()))
		if err != nil {
			return fmt.Errorf("读取全局日配额失败: %w", err)
		}
		if used >= limit {
			return &quotaError{scope: "全局", used: used, limit: limit}
		}
	}

	limit := e.opt.DailyTokensPerClient
	if limit <= 0 {
		return nil
	}
	clientID, err := e.store.ClientIDOfTask(ctx, run.TaskID)
	if err != nil {
		e.log.Warn("配额检查跳过：查不到客户端", "runId", run.ID, "err", err)
		return fmt.Errorf("无法确认客户端配额")
	}
	used, err := e.store.UsageTokens(ctx, quotaScope, quotaKey(clientID, time.Now()))
	if err != nil {
		e.log.Warn("配额读取失败，已拒绝执行", "runId", run.ID)
		return fmt.Errorf("无法读取客户端配额")
	}
	if used >= limit {
		return &quotaError{used: used, limit: limit}
	}
	return nil
}

func (e *Executor) flushAndDrop(ctx context.Context, runID int64) {
	if err := e.hub.Flush(ctx, runID); err != nil {
		e.log.Error("终态事件落库失败", "runId", runID, "err", err)
	}
	e.hub.Drop(runID)
}

// writeCtx 给「收尾写库」用：执行可能已被取消，但状态与事件还是要落下去。
func (e *Executor) writeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (e *Executor) publish(ctx context.Context, runID int64, typ string, payload any) {
	if _, err := e.publishErr(ctx, runID, typ, payload); err != nil {
		e.log.Error("发布事件失败", "runId", runID, "type", typ, "err", err)
	}
}

func (e *Executor) publishErr(ctx context.Context, runID int64, typ string, payload any) (domain.Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, fmt.Errorf("序列化事件 %s 失败: %w", typ, err)
	}
	return e.hub.Publish(ctx, runID, typ, raw)
}

func snapshotModelName(snapshot []byte) string {
	var s domain.ModelSnapshot
	if err := json.Unmarshal(snapshot, &s); err != nil {
		return ""
	}
	return s.Name
}

// sanitizeMessage 给用户看的错误摘要：截断 + 去掉可能的空白噪声。
func sanitizeMessage(err error) string {
	var cfg *llm.ConfigError
	if errors.As(err, &cfg) {
		return cfg.Error()
	}
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("模型供应商返回 HTTP %d，请检查模型配置、余额或稍后重试", apiErr.Status)
	}
	var maxSteps *maxStepsError
	if errors.As(err, &maxSteps) {
		return maxSteps.Error()
	}
	var quota *quotaError
	if errors.As(err, &quota) {
		return quota.Error()
	}
	switch llm.Classify(err) {
	case domain.ErrTimeout:
		return "模型请求超时或流提前结束，请稍后重试"
	}
	return "执行未完成，请检查服务端配置与日志；内部诊断信息不会返回客户端"
}

// toolFeedback 把业务错误包成 JSON 回灌给模型。
// 用 {"error": ...} 而不是纯文本：模型对结构化错误的利用率明显更高，
// 它会照着字段改参数，而不是把错误信息当成回答的一部分复述出去。
func toolFeedback(err error) string {
	raw, mErr := json.Marshal(map[string]string{"error": err.Error()})
	if mErr != nil {
		return `{"error":"工具执行失败"}`
	}
	return string(raw)
}

// clampJSON 保证写进 JSON 列的永远是合法 JSON。
//
// tool_input 是 JSON 列：模型给的参数可能大到上百 KB，也可能根本不是合法 JSON
// （上游截断、模型幻觉）。两种情况直接写都会让 MySQL 报 Invalid JSON text，
// 整条 step 跟着丢——而 step 恰好是排查工具问题唯一的数据源。
func clampJSON(raw string, max int) []byte {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		out, err := json.Marshal(map[string]any{"invalid": truncateRunes(trimmed, max)})
		if err != nil {
			return nil
		}
		return out
	}
	if len(trimmed) > max {
		out, err := json.Marshal(map[string]any{"truncated": true, "bytes": len(trimmed)})
		if err != nil {
			return nil
		}
		return out
	}
	return []byte(trimmed)
}

// jsonValue 把普通字符串包成 JSON 字符串字面量（tool_result 也是 JSON 列）。
func jsonValue(s string) []byte {
	out, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return out
}

// truncateRunes 按字符截断，不会切坏 UTF-8。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// 缓存数是供应商可选观测值，缺失时为 0；不用于费用精确结算。
func cacheHitRate(u llm.Usage) float64 {
	if u.InputTokens <= 0 {
		return 0
	}
	r := float64(u.CachedTokens) / float64(u.InputTokens)
	if r < 0 {
		return 0
	}
	if r > 1 {
		r = 1
	}
	return float64(int(r*1000)) / 1000
}
