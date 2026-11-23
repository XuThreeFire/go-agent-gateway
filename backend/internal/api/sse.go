package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
)

// handleEvents 是 SSE 订阅入口，也是整个项目最关键的一段：
// 断线重连靠 Last-Event-ID 定位，执行与这条连接完全解耦（D-01）。
//
// 客户端是浏览器原生 EventSource，它有三条硬约束，这里逐条对上：
//   - 不能设自定义 header：身份使用同源 Cookie，续传位置可以走 ?afterSeq=；
//   - 断线重连由浏览器自己做，并自动回传 Last-Event-ID（header 通道仍然优先）；
//   - 收到非 200 就不再重连：所以参数错误回 400/404 是「终止」而不是「稍后重试」。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	runID, err := parseID(r.PathValue("runId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "runId 非法")
		return
	}

	// 先确认 Run 存在：不存在的 Run 直接 404，别让客户端对着空流等 ping。
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run 不存在")
			return
		}
		s.log.Error("查询 run 失败", "runId", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}

	afterSeq := resolveAfterSeq(r, runID)

	// 历史回放走批量读：同一份事件、同一套 seq 语义，只是不用建长连接。
	// 10 轮对话开 10 条 SSE 各自等全量补发，收益只有「代码少写一个分支」，
	// 代价是 10 次串行建连 + 10 次重连状态机。
	if r.URL.Query().Get("format") == "json" {
		s.handleEventList(w, r, runID, afterSeq)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前 ResponseWriter 不支持流式")
		return
	}

	head := w.Header()
	head.Set("Content-Type", "text/event-stream; charset=utf-8")
	head.Set("Cache-Control", "no-cache, no-transform")
	head.Set("Connection", "keep-alive")
	// 少了这行，nginx 会把事件攒到连接结束一次性吐出来，表现为「流式失效」。
	// caddy 不需要，Caddy 的 reverse_proxy 只要看到响应头 Content-Type: text/event-stream，就强制立即刷写，此时 flush_interval 会被直接忽略。
	head.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// 断线后浏览器自动重连的间隔，配合 Last-Event-ID 形成补发闭环。
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	// EventSource 在服务端关流后会自动重连。若这次重连落在一个已终态、
	// 身后又没有待补事件的 Run 上，走正常订阅路径只会挂起一条永不产生事件的
	// 连接：浏览器每 3 秒重连一次，永远等不到收尾信号。这里把终态重放一遍，
	// 客户端收到 run.terminal 就能 close() 收敛。
	if domain.IsTerminal(run.Status) {
		done, err := s.replayTerminal(r.Context(), w, flusher, run, afterSeq)
		if err != nil {
			s.log.Error("重放终态事件失败", "runId", runID, "err", err)
		}
		if done {
			return
		}
	}

	events, cancel, err := s.hub.Subscribe(r.Context(), runID, afterSeq)
	if err != nil {
		s.log.Error("订阅事件流失败", "runId", runID, "err", err)
		return
	}
	defer cancel()

	ping := time.NewTicker(s.opt.PingInterval)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			// SSE 注释帧，只为保活：上游首 token 可能十几秒，中间不能没动静。
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d:%d\nevent: %s\ndata: %s\n\n",
				ev.RunID, ev.Seq, ev.Type, ev.Payload); err != nil {
				return
			}
			flusher.Flush()
			if ev.Type == domain.EventRunTerminal {
				// 终态即结束：让客户端明确收尾，不用等超时或猜。
				return
			}
		}
	}
}

// resolveAfterSeq 决定这次订阅从哪条之后开始补发。三路来源，按可靠性排序：
//  1. header `Last-Event-ID`——浏览器重连时自动带，值就是上一帧的 id；
//  2. query `lastEventId`——同一个值，方便非浏览器客户端与手工调试；
//  3. query `afterSeq`——纯数字，前端「打开历史任务」时算好再拼 URL。
//
// 都没有就从 0 开始，即回放全量历史。这对 EventSource 是必需的：刷新页面后
// 浏览器是「新建连接」，没有上一帧的 id，只能靠从头回放把正文重建出来。
func resolveAfterSeq(r *http.Request, runID int64) int64 {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if seq, ok := parseLastEventID(v, runID); ok {
			return seq
		}
	}
	q := r.URL.Query()
	if v := q.Get("lastEventId"); v != "" {
		if seq, ok := parseLastEventID(v, runID); ok {
			return seq
		}
	}
	if v := q.Get("afterSeq"); v != "" {
		if seq, err := strconv.ParseInt(v, 10, 64); err == nil && seq >= 0 {
			return seq
		}
	}
	return 0
}

// eventItem 是批量读的事件形态。payload 直接给 JSON 对象，不要字符串——
// 让前端拿到就能用，而不是先 JSON.parse 一次自己写的转义串。
type eventItem struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// eventListResponse 故意不是裸数组：截断必须能被发现。
// hasMore=true 时按 nextAfterSeq 再拉一页，语义与 SSE 的 Last-Event-ID 完全一致。
type eventListResponse struct {
	Items        []eventItem `json:"items"`
	HasMore      bool        `json:"hasMore"`
	NextAfterSeq int64       `json:"nextAfterSeq"`
}

// handleEventList 是 SSE 的批量读法（`?format=json`），给历史回放用。
// 读的是同一张 event 表、同一套 seq，不经过 hub，所以不会有「实时事件混进来」。
func (s *Server) handleEventList(w http.ResponseWriter, r *http.Request, runID, afterSeq int64) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	// 多取一条判断是否还有下一页，省一次 COUNT。
	events, err := s.store.ListEvents(r.Context(), runID, afterSeq, limit+1)
	if err != nil {
		s.log.Error("读取事件失败", "runId", runID, "afterSeq", afterSeq, "err", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}

	resp := eventListResponse{Items: make([]eventItem, 0, len(events)), HasMore: hasMore, NextAfterSeq: afterSeq}
	for _, ev := range events {
		payload := json.RawMessage(ev.Payload)
		if !json.Valid(payload) {
			// 库里理论上只有合法 JSON；真出现脏行也不能把整页序列化搞崩，
			// 降级成字符串交回，前端至少能看到原文。
			payload = json.RawMessage(strconv.Quote(string(ev.Payload)))
		}
		resp.Items = append(resp.Items, eventItem{Seq: ev.Seq, Type: ev.Type, Payload: payload})
		resp.NextAfterSeq = ev.Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

// replayTerminal 处理「连上一个已经结束、身后没有待补事件」的 Run。
// done=true 表示这条连接已经写完收尾，不必再进订阅循环。
func (s *Server) replayTerminal(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, run domain.Run, afterSeq int64) (bool, error) {
	// Run 行可能先于终态事件提交，先刷新内存尾批；失败时关闭，交给客户端重连。
	if err := s.hub.Flush(ctx, run.ID); err != nil {
		return true, err
	}
	cursor := afterSeq
	for {
		events, err := s.store.ListEvents(ctx, run.ID, cursor, 500)
		if err != nil {
			return true, err
		}
		for _, ev := range events {
			if _, err := fmt.Fprintf(w, "id: %d:%d\nevent: %s\ndata: %s\n\n", run.ID, ev.Seq, ev.Type, ev.Payload); err != nil {
				return true, err
			}
			flusher.Flush()
			cursor = ev.Seq
			if ev.Type == domain.EventRunTerminal {
				return true, nil
			}
		}
		if len(events) < 500 {
			break
		}
	}
	maxSeq, err := s.store.MaxEventSeq(ctx, run.ID)
	if err != nil {
		return true, err
	}
	payload, err := json.Marshal(terminalPayloadOf(run))
	if err != nil {
		return true, err
	}
	// 显式给新序号：EventSource 会继承缺省 id，前端可能把缺省 id 的终态去重掉。
	if cursor > maxSeq {
		maxSeq = cursor
	}
	_, err = fmt.Fprintf(w, "id: %d:%d\nevent: %s\ndata: %s\n\n", run.ID, maxSeq+1, domain.EventRunTerminal, payload)
	flusher.Flush()
	return true, err
}

// terminalPayloadOf 用 run 行还原终态事件，字段与 exec 写入时保持一致。
func terminalPayloadOf(run domain.Run) domain.RunTerminalPayload {
	payload := domain.RunTerminalPayload{
		Status:       run.Status,
		InputTokens:  run.InputTokens,
		OutputTokens: run.OutputTokens,
		TTFTMS:       run.TTFTMS(),
	}
	if run.ErrorCategory != nil {
		payload.ErrorCategory = *run.ErrorCategory
	}
	if run.ErrorMessage != nil {
		payload.ErrorMessage = *run.ErrorMessage
	}
	if run.StartedAt != nil && run.EndedAt != nil {
		payload.DurationMS = run.EndedAt.Sub(*run.StartedAt).Milliseconds()
	}
	return payload
}

// parseLastEventID 解析 "{runId}:{seq}"。
//
// 必须带 runId：只带 seq 的话，客户端换了个 Run 还带着旧 seq，会补到别的事件上。
// runId 对不上时返回 false，调用方回退到「从头补」，宁可多补也不漏。
func parseLastEventID(v string, expectRunID int64) (int64, bool) {
	parts := strings.SplitN(strings.TrimSpace(v), ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	runID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || runID != expectRunID {
		return 0, false
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || seq < 0 {
		return 0, false
	}
	return seq, true
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush 必须转发：否则 SSE handler 里的类型断言拿不到 http.Flusher，流会卡住。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"cost", time.Since(start).String(),
			slog.String("clientId", clientID(r)),
		)
	})
}
