// Package api 负责 HTTP 路由、请求校验与 SSE 连接管理。
//
// 不做：业务编排（在 service/exec）、状态机迁移（在 exec）、SQL（在 store）。
// 关键约束：处理函数不得阻塞等待执行结束——创建类接口一律 202 立即返回（D-01）。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/config"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/exec"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/hub"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/llm"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/service"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/tool"
)

type Options struct {
	SessionSecret  string
	AccessPassword string
	SecureCookies  bool
	DefaultModel   string
	Models         []config.Model
	Tools          []tool.Spec
	PingInterval   time.Duration
	MaxInputRunes  int
}

type Store interface {
	service.Store
	Ping(context.Context) error
	GetTask(context.Context, int64) (domain.Task, error)
	GetRun(context.Context, int64) (domain.Run, error)
	GetTaskDetail(context.Context, int64) (store.TaskDetail, error)
	ListTasks(context.Context, string, string, int) (store.TaskPage, error)
	ListEvents(context.Context, int64, int64, int) ([]domain.Event, error)
	MaxEventSeq(context.Context, int64) (int64, error)
}

type Server struct {
	store  Store
	hub    *hub.Hub
	models *llm.Registry
	exec   *exec.Executor
	svc    *service.Service
	log    *slog.Logger
	opt    Options
}

func New(st Store, h *hub.Hub, models *llm.Registry, ex *exec.Executor, opt Options, log *slog.Logger) *Server {
	if opt.MaxInputRunes <= 0 {
		opt.MaxInputRunes = 32000
	}
	if opt.PingInterval <= 0 {
		opt.PingInterval = 15 * time.Second
	}
	if opt.SessionSecret == "" {
		panic("api.New: SessionSecret 必须由启动配置提供")
	}
	return &Server{store: st, hub: h, models: models, exec: ex, svc: service.New(st, ex), log: log, opt: opt}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/tools", s.handleTools)
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("GET /api/tasks/{taskId}", s.owned(s.handleGetTask))
	mux.HandleFunc("POST /api/tasks/{taskId}/turns", s.owned(s.handleAppendTurn))
	mux.HandleFunc("GET /api/runs/{runId}/events", s.owned(s.handleEvents))
	mux.HandleFunc("POST /api/runs/{runId}/cancel", s.owned(s.handleCancelRun))
	mux.HandleFunc("POST /api/runs/{runId}/retry", s.owned(s.handleRetryRun))
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"clientId": clientID(r)})
	})
	return s.withSession(s.withRequestLog(mux))
}

// ---------- 健康检查与模型清单 ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	body := map[string]string{"status": "ok"}
	code := http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		// 数据库不可用时服务本身还活着，但已经不能干活了，用 503 让上游摘流量。
		body["status"] = "degraded"
		body["db"] = "down"
		code = http.StatusServiceUnavailable
	} else {
		body["db"] = "ok"
	}
	writeJSON(w, code, body)
}

type modelItem struct {
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	SupportsToolCall bool   `json:"supportsToolCall"`
	ContextWindow    int    `json:"contextWindow"`
}

// toolItem 是对外的工具声明。把 parameters 原样返回，调试页可以直接渲染成表单。
type toolItem struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	items := make([]modelItem, 0, len(s.opt.Models))
	for _, m := range s.opt.Models {
		if !m.Enabled {
			continue
		}
		items = append(items, modelItem{
			Name:             m.Name,
			Provider:         m.Provider,
			SupportsToolCall: m.SupportsToolCall,
			ContextWindow:    m.ContextWindow,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "default": s.opt.DefaultModel})
}

// handleTools 暴露网关实际启用的工具，用来回答「这个服务让模型能做什么」。
// 只读、无密钥，可以放心给前端调试页用。
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	items := make([]toolItem, 0, len(s.opt.Tools))
	for _, t := range s.opt.Tools {
		items = append(items, toolItem{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---------- 会话 ----------

type createTaskRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type createTaskResponse struct {
	TaskID int64 `json:"taskId"`
	RunID  int64 `json:"runId"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if len(req.IdempotencyKey) > 64 {
		writeError(w, 400, "idempotencyKey 最多 64 字节")
		return
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		writeError(w, http.StatusBadRequest, "input 不能为空")
		return
	}
	if n := utf8.RuneCountInString(input); n > s.opt.MaxInputRunes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("input 过长：%d 字，上限 %d 字", n, s.opt.MaxInputRunes))
		return
	}

	snapshot, ok := s.snapshotFor(req.Model)
	if !ok {
		writeError(w, http.StatusBadRequest, "模型不存在或未启用: "+req.Model)
		return
	}

	out, err := s.svc.CreateTask(r.Context(), service.CreateTaskInput{
		ClientID:       clientID(r),
		Title:          truncateRunes(input, 200),
		Input:          input,
		ModelSnapshot:  snapshot,
		IdempotencyKey: optString(req.IdempotencyKey),
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	// 幂等重放：告诉客户端「这是重放，不是新会话」，它可据此跳过重复建连。
	if !out.Created {
		w.Header().Set("Idempotent-Replay", "true")
	}
	writeJSON(w, http.StatusAccepted, createTaskResponse{TaskID: out.Task.ID, RunID: out.Run.ID})
}

type appendTurnRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type appendTurnResponse struct {
	TurnID int64 `json:"turnId"`
	RunID  int64 `json:"runId"`
}

func (s *Server) handleAppendTurn(w http.ResponseWriter, r *http.Request) {
	taskID, err := parseID(r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "taskId 非法")
		return
	}
	var req appendTurnRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if len(req.IdempotencyKey) > 64 {
		writeError(w, 400, "idempotencyKey 最多 64 字节")
		return
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		writeError(w, http.StatusBadRequest, "input 不能为空")
		return
	}
	if n := utf8.RuneCountInString(input); n > s.opt.MaxInputRunes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("input 过长：%d 字，上限 %d 字", n, s.opt.MaxInputRunes))
		return
	}

	snapshot, ok := s.snapshotFor(req.Model)
	if !ok {
		writeError(w, http.StatusBadRequest, "模型不存在或未启用: "+req.Model)
		return
	}

	out, err := s.svc.AppendTurn(r.Context(), service.AppendTurnInput{
		TaskID:         taskID,
		Input:          input,
		ModelSnapshot:  snapshot,
		IdempotencyKey: optString(req.IdempotencyKey),
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	// 与建会话同一套语义：重放只回 id，不再提交执行。
	if !out.Created {
		w.Header().Set("Idempotent-Replay", "true")
	}
	writeJSON(w, http.StatusAccepted, appendTurnResponse{TurnID: out.Turn.ID, RunID: out.Run.ID})
}

type cancelRunResponse struct {
	RunID  int64  `json:"runId"`
	Status string `json:"status"`
}

// handleCancelRun 取消一次执行。已终态返回 409 并带上当前状态，让客户端自己收敛。
// 幂等：重复取消走到 409，客户端据此认为“已经停了”，不需要特殊处理。
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	runID, err := parseID(r.PathValue("runId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "runId 非法")
		return
	}

	run, err := s.svc.Cancel(r.Context(), runID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, cancelRunResponse{RunID: run.ID, Status: run.Status})
}

type retryRunResponse struct {
	RunID       int64 `json:"runId"`
	SourceRunID int64 `json:"sourceRunId"`
	AttemptNo   int   `json:"attemptNo"`
}

// handleRetryRun 基于一个终态的 Run 新建一次尝试。
// 不重放原 Run、不改它的状态：终态只能写一次，重跑就是新行（attempt_no + 1）。
func (s *Server) handleRetryRun(w http.ResponseWriter, r *http.Request) {
	runID, err := parseID(r.PathValue("runId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "runId 非法")
		return
	}

	run, err := s.svc.Retry(r.Context(), runID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, retryRunResponse{RunID: run.ID, SourceRunID: runID, AttemptNo: run.AttemptNo})
}

type taskItem struct {
	ID        int64     `json:"id"`
	ClientID  string    `json:"clientId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// 侧栏角标：本会话最近一次 Run（重试多次时取 id 最大的那次）。
	// 没有 Run 的会话不出现这两个字段，前端据此显示「无记录」而不是「运行中」。
	// 只加字段、不改老字段，旧客户端不升级也不会坏。
	LastRunID     int64  `json:"lastRunId,omitempty"`
	LastRunStatus string `json:"lastRunStatus,omitempty"`
}

func toTaskItem(t domain.Task) taskItem {
	return taskItem{ID: t.ID, ClientID: t.ClientID, Title: t.Title, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

type listTasksResponse struct {
	Items      []taskItem `json:"items"`
	HasMore    bool       `json:"hasMore"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := s.store.ListTasks(r.Context(), clientID(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	resp := listTasksResponse{Items: make([]taskItem, 0, len(page.Items)), HasMore: page.HasMore, NextCursor: page.NextCursor}
	for _, t := range page.Items {
		item := toTaskItem(t)
		// LastRuns 随分页一次取回，这里只是 map 查表，不再回库。
		if run, ok := page.LastRuns[t.ID]; ok {
			item.LastRunID = run.ID
			item.LastRunStatus = run.Status
		}
		resp.Items = append(resp.Items, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

type turnItem struct {
	ID        int64     `json:"id"`
	Seq       int       `json:"seq"`
	UserInput string    `json:"userInput"`
	CreatedAt time.Time `json:"createdAt"`
}

type runItem struct {
	ID            int64      `json:"id"`
	TurnID        int64      `json:"turnId"`
	AttemptNo     int        `json:"attemptNo"`
	Status        string     `json:"status"`
	ErrorCategory string     `json:"errorCategory,omitempty"`
	ErrorMessage  string     `json:"errorMessage,omitempty"`
	InputTokens   int64      `json:"inputTokens"`
	OutputTokens  int64      `json:"outputTokens"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FirstTokenAt  *time.Time `json:"firstTokenAt,omitempty"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

type taskDetailResponse struct {
	Task  taskItem   `json:"task"`
	Turns []turnItem `json:"turns"`
	Runs  []runItem  `json:"runs"`
	Usage struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := parseID(r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "taskId 非法")
		return
	}
	detail, err := s.store.GetTaskDetail(r.Context(), taskID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	resp := taskDetailResponse{Task: toTaskItem(detail.Task)}
	resp.Turns = make([]turnItem, 0, len(detail.Turns))
	for _, t := range detail.Turns {
		resp.Turns = append(resp.Turns, turnItem{ID: t.ID, Seq: t.Seq, UserInput: t.UserInput, CreatedAt: t.CreatedAt})
	}
	resp.Runs = make([]runItem, 0, len(detail.Runs))
	for _, run := range detail.Runs {
		item := runItem{
			ID:           run.ID,
			TurnID:       run.TurnID,
			AttemptNo:    run.AttemptNo,
			Status:       run.Status,
			InputTokens:  run.InputTokens,
			OutputTokens: run.OutputTokens,
			StartedAt:    run.StartedAt,
			FirstTokenAt: run.FirstTokenAt,
			EndedAt:      run.EndedAt,
			CreatedAt:    run.CreatedAt,
		}
		if run.ErrorCategory != nil {
			item.ErrorCategory = *run.ErrorCategory
		}
		if run.ErrorMessage != nil {
			item.ErrorMessage = *run.ErrorMessage
		}
		resp.Runs = append(resp.Runs, item)
		resp.Usage.InputTokens += run.InputTokens
		resp.Usage.OutputTokens += run.OutputTokens
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------- 工具函数 ----------

// snapshotFor 把配置里的模型冻结成快照，随 run 一起入库。
// 之后改配置也不影响历史 Run 的归因（model_snapshot 的意义就在这）。
func (s *Server) snapshotFor(name string) ([]byte, bool) {
	if name == "" {
		name = s.opt.DefaultModel
	}
	for _, m := range s.opt.Models {
		if !m.Enabled || m.Name != name {
			continue
		}
		if _, ok := s.models.Get(name); !ok {
			return nil, false
		}
		snapshot := domain.ModelSnapshot{
			Name:             m.Name,
			Provider:         m.Provider,
			UpstreamModel:    m.UpstreamModel,
			BaseURL:          m.BaseURL,
			SupportsToolCall: m.SupportsToolCall,
			ContextWindow:    m.ContextWindow,
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return nil, false
		}
		return raw, true
	}
	return nil, false
}

// writeStoreError 把持久化错误翻译成 HTTP 语义。
// 409 一定带当前状态，让客户端自己决定是轮询还是放弃（05 接口约定）。
func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	// 两类 409 都带回 runId + 当前状态，客户端据此决定是重连订阅还是重新提交：
	//   - store.ConflictError：这个 Task 还有活跃 Run（规则 1）；
	//   - exec.ConflictError：这个 Run 已是终态，取消/重试无从下手。
	var ce *store.ConflictError
	if errors.As(err, &ce) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  ce.Error(),
			"code":   errorCode(http.StatusConflict),
			"runId":  ce.ActiveRunID,
			"status": ce.Status,
		})
		return
	}
	var xe *exec.ConflictError
	if errors.As(err, &xe) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  xe.Error(),
			"code":   errorCode(http.StatusConflict),
			"runId":  xe.RunID,
			"status": xe.Status,
		})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	s.log.Error("存储错误", "err", err)
	writeError(w, http.StatusInternalServerError, "内部错误")
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	// 1MB 上限：单条 prompt 的正常量级在几十 KB，超过这个数基本是误用。
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("请求体超过 1MB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("请求体只能包含一个 JSON 对象，且不能超过 1MB")
	}
	return nil
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("非法 id: %q", raw)
	}
	return id, nil
}

// clientID 只能来自已验证签名的 Cookie；不信任客户端提供的 header/query 身份。
func clientID(r *http.Request) string {
	id, _ := r.Context().Value(identityKey{}).(string)
	return id
}

func optString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 统一错误体：{"error": 中文摘要, "code": 稳定枚举}。
// code 是给程序判断的，error 是给人看的——文案会改，code 不改。
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg, "code": errorCode(code)})
}

// errorCode 把 HTTP 状态翻译成固定枚举，前端不必靠状态码猜原因。
func errorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusBadRequest:
		return "invalid_argument"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "internal"
	}
}
