package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// 上游 SSE 的解析必须经得住：空帧、心跳注释、usage 收尾帧、[DONE]。
func TestStreamParsesOpenAISSE(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("路径 = %s，期望 /v1/chat/completions", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体不是 JSON: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("stream 字段应为 true，实际 %v", body["stream"])
		}
		if _, ok := body["stream_options"]; !ok {
			t.Error("缺少 stream_options，供应商不会返回 usage")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		frames := []string{
			": keep-alive",
			`data: {"choices":[{"delta":{"content":"你"}}]}`,
			``,
			`data: {"choices":[{"delta":{"content":"好"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
			`data: [DONE]`,
		}
		for _, f := range frames {
			fmt.Fprintf(w, "%s\n\n", f)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := New(Target{
		Name:          "test",
		BaseURL:       srv.URL + "/v1",
		APIKey:        "test-key",
		UpstreamModel: "upstream-1",
	}, ClientOptions{})

	var text strings.Builder
	res, err := c.Stream(context.Background(), Request{Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}}}, func(ch Chunk) error {
		text.WriteString(ch.Delta)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if got := text.String(); got != "你好" {
		t.Fatalf("拼接结果 = %q，期望 %q", got, "你好")
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v，期望 10/2", res.Usage)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

// 4xx 不重试，且要能被归成 protocol，让 run 立刻失败而不是挂住。
func TestStreamNon200IsProtocolError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	c := New(Target{Name: "t", BaseURL: srv.URL, APIKey: "test-key", UpstreamModel: "m"}, ClientOptions{MaxRetries: 1})
	_, err := c.Stream(context.Background(), Request{}, func(Chunk) error { return nil })
	if err == nil {
		t.Fatal("期望报错")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("错误类型不对: %v", err)
	}
	if got := Classify(err); got != domain.ErrProtocol {
		t.Fatalf("分类 = %s，期望 protocol", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("4xx 不应该重试，实际请求 %d 次", n)
	}
}

// 429 且一个字都没收到时，按 D-03 退避后重试一次。
func TestStreamRetries429Once(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New(Target{Name: "t", BaseURL: srv.URL, APIKey: "test-key", UpstreamModel: "m"}, ClientOptions{MaxRetries: 1})
	var text strings.Builder
	if _, err := c.Stream(context.Background(), Request{}, func(ch Chunk) error {
		text.WriteString(ch.Delta)
		return nil
	}); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if text.String() != "ok" {
		t.Fatalf("内容 = %q", text.String())
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("应共请求 2 次，实际 %d", n)
	}
}

func asAPIError(err error, target **APIError) bool {
	for err != nil {
		if e, ok := err.(*APIError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// 工具调用在流里是分片的：name 只在首帧出现，arguments 被拆成多段，
// 而且不同 index 的帧会交错。适配层必须按 index 归并，不能「来一帧建一个调用」。
func TestStreamAssemblesToolCallsByIndex(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&sent)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		frames := []string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calc","arguments":""}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"expres"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"now","arguments":"{}"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"sion\":\"(1+2)*3\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}
		for _, f := range frames {
			fmt.Fprintf(w, "%s\n\n", f)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := New(Target{Name: "t", BaseURL: srv.URL, APIKey: "test-key", UpstreamModel: "m", SupportsToolCall: true}, ClientOptions{})
	res, err := c.Stream(context.Background(), Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "算一下"}},
		Tools: []ToolSpec{{
			Name:        "calc",
			Description: "算数",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	}, func(Chunk) error { return nil })
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}

	if len(res.ToolCalls) != 2 {
		t.Fatalf("工具调用数 = %d，期望 2（按 index 归并，别按帧数数）", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Name != "calc" || res.ToolCalls[0].Arguments != `{"expression":"(1+2)*3"}` {
		t.Fatalf("第 1 个调用拼装错误: %+v", res.ToolCalls[0])
	}
	if res.ToolCalls[1].Name != "now" || res.ToolCalls[1].ID != "call_2" {
		t.Fatalf("第 2 个调用拼装错误: %+v", res.ToolCalls[1])
	}

	// 请求体得带上 tools，否则上游永远不会发起调用。
	tools, ok := sent["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("请求体缺少 tools: %v", sent["tools"])
	}
}

// 模型发起调用的那条 assistant 消息要原样回灌，tool 消息要带 tool_call_id，
// 否则第二轮上游会因为「tool 消息找不到对应调用」直接 400。
func TestBuildRequestCarriesToolHistory(t *testing.T) {
	c := New(Target{Name: "t", UpstreamModel: "m"}, ClientOptions{}).(*openAICompat)
	req := c.buildRequest(Request{Messages: []domain.Message{
		{Role: domain.RoleUser, Content: "现在几点"},
		{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{
			{ID: "call_1", Name: "now", Arguments: `{"timezone":"Asia/Shanghai"}`},
		}},
		{Role: domain.RoleTool, ToolCallID: "call_1", Name: "now", Content: "2026-11-22 22:00:00"},
	}})

	if len(req.Messages) != 3 {
		t.Fatalf("消息数 = %d", len(req.Messages))
	}
	if len(req.Messages[1].ToolCalls) != 1 || req.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant 消息丢了 tool_calls: %+v", req.Messages[1])
	}
	if req.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool 消息丢了 tool_call_id: %+v", req.Messages[2])
	}
	// 没有工具时不能发空数组：部分供应商会因此报参数错。
	if req.Tools != nil {
		t.Fatalf("无工具时 tools 应为 nil，实际 %+v", req.Tools)
	}
}

// 空密钥不该把请求发出去：发出去只会换来上游一句 401，
// 还会被 Classify 归成 protocol，把排查方向带偏。这里断言请求数=0。
func TestStreamRejectsEmptyKeyLocally(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Target{Name: "no-key-model", BaseURL: srv.URL, KeyEnv: "OPENAI_API_KEY"}, ClientOptions{})
	_, err := c.Stream(context.Background(), Request{}, func(Chunk) error { return nil })
	if err == nil {
		t.Fatal("空密钥应直接报错")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("错误类型应为 *ConfigError，实际 %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("错误信息应点出该设哪个环境变量: %v", err)
	}
	if hits != 0 {
		t.Fatalf("空密钥不应发出请求，实际发了 %d 次", hits)
	}
	if got := Classify(err); got != domain.ErrProtocol {
		t.Fatalf("分类 = %s，期望 protocol", got)
	}
}

func TestStreamRejectsEarlyEOFAndLengthLimit(t *testing.T) {
	for _, frame := range []string{
		`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, frame)
		}))
		c := New(Target{Name: "test", BaseURL: srv.URL, APIKey: "test-key", UpstreamModel: "m"}, ClientOptions{})
		_, err := c.Stream(context.Background(), Request{}, func(Chunk) error { return nil })
		srv.Close()
		if err == nil {
			t.Fatal("截断的流不得报告成功")
		}
	}
}
func TestUpstreamErrorDoesNotExposeResponseBody(t *testing.T) {
	e := &APIError{Status: 401, Body: "private-provider-diagnostic"}
	if strings.Contains(e.Error(), e.Body) {
		t.Fatal("不得公开上游错误正文")
	}
}

func TestBuildRequestKeepsSystemRole(t *testing.T) {
	c := New(Target{Name: "t", UpstreamModel: "m"}, ClientOptions{}).(*openAICompat)
	req := c.buildRequest(Request{Messages: []domain.Message{
		{Role: domain.RoleSystem, Content: "只用一句话回答"},
		{Role: domain.RoleUser, Content: "介绍一下自己"},
	}})

	if len(req.Messages) != 2 {
		t.Fatalf("消息数 = %d", len(req.Messages))
	}
	if req.Messages[0].Role != "system" {
		t.Fatalf("首条 role = %q，期望 system", req.Messages[0].Role)
	}
	if req.Messages[0].Content != "只用一句话回答" {
		t.Fatalf("system 内容被改写: %q", req.Messages[0].Content)
	}
	if req.Messages[1].Role != "user" {
		t.Fatalf("第二条 role = %q，期望 user", req.Messages[1].Role)
	}
}

func TestStreamSendsProviderOutputLimit(t *testing.T) {
	for _, tc := range []struct {
		provider string
		wantKey  string
		otherKey string
	}{
		{provider: "openai", wantKey: "max_completion_tokens", otherKey: "max_tokens"},
		{provider: "deepseek", wantKey: "max_tokens", otherKey: "max_completion_tokens"},
		{provider: "compatible", wantKey: "max_tokens", otherKey: "max_completion_tokens"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if string(body[tc.wantKey]) != "123" {
					t.Errorf("%s=%s，期望123", tc.wantKey, body[tc.wantKey])
				}
				if _, exists := body[tc.otherKey]; exists {
					t.Errorf("不应同时发送%s", tc.otherKey)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			c := New(Target{Provider: tc.provider, BaseURL: srv.URL, APIKey: "test", UpstreamModel: "m"}, ClientOptions{})
			_, err := c.Stream(context.Background(), Request{MaxOutputTokens: 123}, func(Chunk) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamPreservesUsageAfterLaterError(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tail          string
		callbackError bool
	}{
		{name: "malformed chunk", tail: "data: invalid-json\n\n"},
		{name: "callback error", callbackError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
				fmt.Fprint(w, tc.tail)
			}))
			defer srv.Close()
			c := New(Target{BaseURL: srv.URL, APIKey: "test", UpstreamModel: "m"}, ClientOptions{})
			res, err := c.Stream(context.Background(), Request{}, func(chunk Chunk) error {
				if tc.callbackError && chunk.Usage != nil {
					return context.Canceled
				}
				return nil
			})
			if err == nil || res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
				t.Fatalf("流报错丢失已知用量: usage=%+v，err=%v", res.Usage, err)
			}
		})
	}
}

func TestStreamReadsCachedTokens(t *testing.T) {
	for _, usage := range []string{`{"prompt_tokens":20,"completion_tokens":3,"prompt_cache_hit_tokens":12}`, `{"prompt_tokens":20,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":12}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", usage)
		}))
		c := New(Target{BaseURL: srv.URL, APIKey: "fixture-key", UpstreamModel: "fixture-model"}, ClientOptions{})
		res, err := c.Stream(context.Background(), Request{}, func(Chunk) error { return nil })
		srv.Close()
		if err != nil || res.Usage.CachedTokens != 12 || res.Usage.InputTokens != 20 {
			t.Fatalf("缓存用量解析错误：%+v %v", res.Usage, err)
		}
	}
}

func TestStreamNormalizesMissingToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-empty\",\"function\":{\"name\":\"now\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(Target{BaseURL: server.URL, APIKey: "fixture-key", UpstreamModel: "fixture-model"}, ClientOptions{})
	result, err := client.Stream(context.Background(), Request{}, func(Chunk) error { return nil })
	if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("空参数未规范化：%+v err=%v", result, err)
	}
}
