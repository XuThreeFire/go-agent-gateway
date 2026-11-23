// Package llm 是供应商适配层：把各家流式响应统一成「增量回调 + 用量」。
//
// 只抽适配层，不抽跨供应商的消息中间模型（D-05）：tool_call / reasoning 的格式
// 各家差别太大，强行统一会丢信息。当前提供模拟实现与 Chat Completions 兼容实现；
// 新协议新增适配器，不改 exec。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// Target 是一个可用模型的静态信息。密钥只在这里出现，不进入响应与日志。
type Target struct {
	Name     string
	Provider string
	BaseURL  string
	APIKey   string
	// KeyEnv 只用于报错时告诉人该设哪个环境变量，不参与鉴权。
	KeyEnv           string
	UpstreamModel    string
	SupportsToolCall bool
	ContextWindow    int
}

// Usage 是本次调用的 token 用量。供应商没返回时保持 0，不估算。
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
}

// ToolSpec 是发给模型的工具声明，参数结构用 JSON Schema 描述。
// 存在的意义只有一个：让模型知道能调什么、怎么填参数。真正的白名单在 tool 包，
// 这里写什么都不会让模型获得额外能力。
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request 是一次模型调用的入参。
// Tools 为空表示本次不启用工具（模型不支持函数调用，或网关没注册任何工具）。
type Request struct {
	MaxOutputTokens int
	Messages        []domain.Message
	Tools           []ToolSpec
}

// ToolCallDelta 是流式工具调用的一个片段。
// index 标识这是本轮第几个调用；name 通常只在第一帧出现，
// arguments 会被拆成若干段，必须按 index 归并后拼接。
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// Chunk 是一个增量。Delta 为空不代表没内容：可能只带 usage、finish_reason 或工具片段。
type Chunk struct {
	Delta        string
	FinishReason string
	Usage        *Usage
	ToolCall     *ToolCallDelta
}

// Result 是调用结束后的汇总。
// Content 是拼好的完整文本，ToolCalls 是按 index 归并好的调用列表——两者都由适配层产出，
// 上层不需要再关心 SSE 的分片方式。
type Result struct {
	Usage             Usage
	HTTPStatus        int
	Content           string
	ToolCalls         []domain.ToolCall
	PrefixFingerprint string
}

type Client interface {
	Target() Target
	Stream(ctx context.Context, req Request, onChunk func(Chunk) error) (Result, error)
}

type ClientOptions struct {
	// nil 使用代理环境变量；空 URL 强制直连；其他 URL 仅影响 LLM 出站。
	Proxy             *url.URL
	InsecureSkipTLS   bool
	RequestTimeout    time.Duration // 单次调用总超时，兜住「连上了但一直不吐 token」
	FirstTokenTimeout time.Duration // 建连 + 等响应头
	MaxRetries        int           // 仅 429 生效，退避后最多重试一次（D-03）
}

type openAICompat struct {
	target Target
	http   *http.Client
	opt    ClientOptions
}

func New(target Target, opt ClientOptions) Client {
	if target.Provider == "mock" {
		return &mockClient{target: target, delay: 35 * time.Millisecond}
	}
	if opt.RequestTimeout <= 0 {
		opt.RequestTimeout = 300 * time.Second
	}
	if opt.FirstTokenTimeout <= 0 {
		opt.FirstTokenTimeout = 60 * time.Second
	}
	proxy := http.ProxyFromEnvironment
	if opt.Proxy != nil {
		if opt.Proxy.String() == "" {
			proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
		} else {
			proxy = http.ProxyURL(opt.Proxy)
		}
	}
	transport := &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opt.FirstTokenTimeout,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}
	if opt.Proxy != nil && opt.Proxy.String() != "" {
		transport.MaxIdleConnsPerHost = 2
	}
	if opt.InsecureSkipTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &openAICompat{
		target: target,
		opt:    opt,
		http:   &http.Client{Transport: transport},
	}
}

func (c *openAICompat) Target() Target { return c.target }

// Stream 把上游的 SSE 逐块回调出去。只有「一个字节都没收到就 429」才重试，
// 已经开始吐内容再重试会把半句话重复一遍，比失败更糟。
func (c *openAICompat) Stream(ctx context.Context, req Request, onChunk func(Chunk) error) (Result, error) {
	// 空密钥在本地拦掉：真发出去只会拿到上游一句
	// 「auth header format should be Bearer sk-...」，401 还会被归成协议错误，
	// 排查方向直接跑偏。不配密钥是部署问题，没必要占用一次往返。
	if c.target.APIKey == "" {
		return Result{}, &ConfigError{Model: c.target.Name, KeyEnv: c.target.KeyEnv}
	}

	var lastErr error
	for attempt := 0; attempt <= c.opt.MaxRetries; attempt++ {
		res, streamed, err := c.streamOnce(ctx, req, onChunk)
		if err == nil {
			return res, nil
		}
		lastErr = err

		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || streamed {
			return res, err
		}
		if attempt == c.opt.MaxRetries {
			break
		}
		delay := apiErr.RetryAfter
		if delay <= 0 {
			delay = time.Duration(attempt+1) * time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
	return Result{}, lastErr
}

func (c *openAICompat) streamOnce(ctx context.Context, req Request, onChunk func(Chunk) error) (Result, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.opt.RequestTimeout)
	defer cancel()

	wire, err := c.buildRequestV3(req)
	if err != nil {
		return Result{}, false, fmt.Errorf("构造模型请求失败: %w", err)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return Result{}, false, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := strings.TrimRight(c.target.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, false, fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.target.APIKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 已开始写但没读到响应：归类交给上层（Classify）
		return Result{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Result{HTTPStatus: resp.StatusCode}, false, &APIError{
			Status:     resp.StatusCode,
			Body:       strings.TrimSpace(string(snippet)),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	res := Result{HTTPStatus: resp.StatusCode, PrefixFingerprint: staticPrefixFingerprint(wire)}
	finished := false
	finishReason := ""
	var usage Usage
	var content strings.Builder
	calls := newCallAssembler()
	streamed := false

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 单行上限 1MB，防止超长 chunk 截断

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue // 注释帧（心跳）跳过
		}
		if !strings.HasPrefix(line, "data:") {
			continue // event: / id: 等字段本轮不需要
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			if data == "[DONE]" {
				break
			}
			continue
		}

		var chunk wireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return res, streamed, errors.New("解析上游流失败：响应格式不符合 Chat Completions 协议")
		}
		if chunk.Usage != nil {
			usage = Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, CachedTokens: chunk.Usage.cached()}
			res.Usage = usage
			u := usage
			if err := onChunk(Chunk{Usage: &u}); err != nil {
				return res, streamed, err
			}
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta.Content
			finish := ""
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
				finished = true
				finishReason = finish
			}
			if delta != "" {
				content.WriteString(delta)
			}
			if delta == "" && finish == "" && len(choice.Delta.ToolCalls) == 0 {
				continue
			}
			if delta != "" || len(choice.Delta.ToolCalls) > 0 {
				// 工具片段也算「已经开始吐内容」：这时候重试会把前面的调用再发一遍。
				streamed = true
			}
			if err := onChunk(Chunk{Delta: delta, FinishReason: finish}); err != nil {
				return res, streamed, err
			}
			for _, tc := range choice.Delta.ToolCalls {
				d := calls.add(tc)
				if err := onChunk(Chunk{ToolCall: d}); err != nil {
					return res, streamed, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return res, streamed, fmt.Errorf("读取上游流失败: %w", err)
	}

	if !finished {
		return res, streamed, io.ErrUnexpectedEOF
	}
	if finishReason != "stop" && finishReason != "tool_calls" {
		return res, streamed, fmt.Errorf("上游未正常完成：finish_reason=%s", finishReason)
	}
	for _, idx := range calls.order {
		call := calls.byIndex[idx]
		if strings.TrimSpace(call.Arguments) == "" {
			call.Arguments = "{}"
		}
		if call.ID == "" || call.Name == "" || !json.Valid([]byte(call.Arguments)) {
			return res, streamed, errors.New("上游工具调用缺少标识、名称或合法 JSON 参数")
		}
	}
	res.Usage = usage
	res.Content = content.String()
	res.ToolCalls = calls.list()
	return res, streamed, nil
}

// callAssembler 按 index 归并流式工具调用。
// 上游会把一次调用拆成若干帧（首帧给 id 与 name，后续帧只给 arguments 片段），
// 而且不同 index 的帧可能交错，所以必须按 index 分开累加，不能"来一帧建一个"。
type callAssembler struct {
	byIndex map[int]*domain.ToolCall
	order   []int
}

func newCallAssembler() *callAssembler {
	return &callAssembler{byIndex: map[int]*domain.ToolCall{}}
}

func (a *callAssembler) add(tc wireToolCall) *ToolCallDelta {
	call, ok := a.byIndex[tc.Index]
	if !ok {
		call = &domain.ToolCall{}
		a.byIndex[tc.Index] = call
		a.order = append(a.order, tc.Index)
	}
	if tc.ID != "" {
		call.ID = tc.ID
	}
	if tc.Function.Name != "" {
		call.Name = tc.Function.Name
	}
	call.Arguments += tc.Function.Arguments
	return &ToolCallDelta{
		Index:     tc.Index,
		ID:        tc.ID,
		Name:      tc.Function.Name,
		Arguments: tc.Function.Arguments,
	}
}

func (a *callAssembler) list() []domain.ToolCall {
	if len(a.order) == 0 {
		return nil
	}
	out := make([]domain.ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		call := a.byIndex[idx]
		// id 缺失的调用没法回灌（tool 消息必须带 tool_call_id），直接丢弃，
		// 比让整个 Run 因为上游格式问题失败要好。
		if call.ID == "" || call.Name == "" {
			continue
		}
		if call.Arguments == "" {
			call.Arguments = "{}"
		}
		out = append(out, *call)
	}
	return out
}

type wireRequest struct {
	MaxTokens           int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Model               string        `json:"model"`
	Messages            []wireMessage `json:"messages"`
	Stream              bool          `json:"stream"`
	StreamOptions       *streamOption `json:"stream_options,omitempty"`
	// Tools 为空时整个字段省略：部分供应商对「空数组 tools」也会报参数错。
	Tools []wireTool `json:"tools,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"` // 恒为 function，OpenAI 目前只有这一种
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type wireToolCall struct {
	// Index 只在流式增量里有值，用来标识「这是第几个调用」；回灌历史消息时不发。
	Index    int              `json:"index,omitempty"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireCallFunction `json:"function"`
}

type wireCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type streamOption struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
}
type wireUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptCacheHit      int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u wireUsage) cached() int64 {
	if u.PromptCacheHit > 0 {
		return u.PromptCacheHit
	}
	return u.PromptTokensDetails.CachedTokens
}

func (c *openAICompat) buildRequest(req Request) wireRequest {
	wire := make([]wireMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := string(m.Role)
		if role == "" {
			role = string(domain.RoleUser)
		}
		wm := wireMessage{Role: role, Content: m.Content}
		if len(m.ToolCalls) > 0 {
			wm.ToolCalls = make([]wireToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: wireCallFunction{Name: tc.Name, Arguments: tc.Arguments},
				})
			}
		}
		if m.Role == domain.RoleTool {
			wm.ToolCallID = m.ToolCallID
			wm.Name = m.Name
		}
		wire = append(wire, wm)
	}

	// 没有工具时保持 nil：omitempty 才会真的把这个字段省掉。
	// 发一个空数组 []，有的供应商会当成「声明了 0 个工具」并拒绝请求。
	var tools []wireTool
	if len(req.Tools) > 0 {
		tools = make([]wireTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, wireTool{
				Type: "function",
				Function: wireFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
	}

	return c.withOutputLimit(wireRequest{
		Model:    c.target.UpstreamModel,
		Messages: wire,
		Stream:   true,
		// 不带这个参数，多数供应商在流式模式下不返回 usage，token 统计会全是 0。
		StreamOptions: &streamOption{IncludeUsage: true},
		Tools:         tools,
	}, req.MaxOutputTokens)
}

// OpenAI 使用 max_completion_tokens，其他兼容协议使用 max_tokens，避免同时发送。
func (c *openAICompat) withOutputLimit(w wireRequest, limit int) wireRequest {
	if limit > 0 {
		if strings.EqualFold(c.target.Provider, "openai") {
			w.MaxCompletionTokens = limit
		} else {
			w.MaxTokens = limit
		}
	}
	return w
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
